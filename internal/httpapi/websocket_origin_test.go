package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"edo/internal/database"
)

func TestWebSocketRejectsOtherOriginWithValidSession(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	server := httptest.NewServer(router)
	defer server.Close()
	cookie := realtimeLogin(t, router)
	header := http.Header{
		"Cookie":           {cookie.String()},
		"X-EDO-Project-ID": {database.DefaultProjectID(database.DefaultDepartmentID)},
		// 同站点的另一个端口仍是不同来源，不能依赖 SameSite Cookie 阻止它。
		"Origin": {"http://127.0.0.1:1"},
	}
	dialer := websocket.Dialer{Subprotocols: []string{stateSubprotocol}, HandshakeTimeout: 3 * time.Second}
	connection, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/events/ws", header)
	if connection != nil {
		_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		_ = connection.Close()
	}
	if response == nil {
		t.Fatalf("未取得握手响应: %v", err)
	}
	defer response.Body.Close()
	if err == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("有效登录态的跨来源连接未被拒绝: status=%d", response.StatusCode)
	}
}

func TestWebSocketOriginPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		host    string
		origins []string
		allowed bool
	}{
		{"非浏览器", "edo.example:8443", nil, true},
		{"HTTP同来源", "edo.example:8443", []string{"http://edo.example:8443"}, true},
		{"HTTPS反向代理", "edo.example:8443", []string{"https://edo.example:8443"}, true},
		{"主机名大小写", "EDO.example:8443", []string{"https://edo.EXAMPLE:8443"}, true},
		{"IPv6", "[::1]:8443", []string{"http://[::1]:8443"}, true},
		{"不同端口", "edo.example:8443", []string{"https://edo.example:8444"}, false},
		{"不同子域", "edo.example:8443", []string{"https://other.example:8443"}, false},
		{"伪造转发主机", "edo.example:8443", []string{"https://attacker.example:8443"}, false},
		{"相似后缀", "edo.example:8443", []string{"https://edo.example.attacker:8443"}, false},
		{"显式空来源", "edo.example:8443", []string{""}, false},
		{"不透明来源", "edo.example:8443", []string{"null"}, false},
		{"无主机名", "edo.example:8443", []string{"https:///edo.example:8443"}, false},
		{"缺少协议", "edo.example:8443", []string{"//edo.example:8443"}, false},
		{"不支持的协议", "edo.example:8443", []string{"file://edo.example:8443"}, false},
		{"URL带凭据", "edo.example:8443", []string{"https://person:private@edo.example:8443"}, false},
		{"路径", "edo.example:8443", []string{"https://edo.example:8443/"}, false},
		{"空查询", "edo.example:8443", []string{"https://edo.example:8443?"}, false},
		{"查询", "edo.example:8443", []string{"https://edo.example:8443?key=value"}, false},
		{"空片段", "edo.example:8443", []string{"https://edo.example:8443#"}, false},
		{"片段", "edo.example:8443", []string{"https://edo.example:8443#value"}, false},
		{"多来源", "edo.example:8443", []string{"https://edo.example:8443", "https://other.example:8443"}, false},
		{"逗号列表", "edo.example:8443", []string{"https://edo.example:8443, https://other.example:8443"}, false},
		{"原始非ASCII", "edo.example:8443", []string{"https://édo.example:8443"}, false},
		{"转义非法", "edo.example:8443", []string{"https://%xx.example:8443"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://edo.example:8443/", nil)
			request.Host = tc.host
			if tc.origins != nil {
				request.Header["Origin"] = tc.origins
			}
			request.Header.Set("X-Forwarded-Host", "attacker.example:8443")
			request.Header.Set("Forwarded", "host=attacker.example:8443")
			if allowedWebSocketOrigin(request) != tc.allowed {
				t.Fatalf("来源校验不符合预期: allowed=%t", tc.allowed)
			}
		})
	}
}

func TestWebSocketOriginGateStopsBeforeResourceAndRedactsInput(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	called := 0
	router := gin.New()
	router.Use(requestID(), requireWebSocketOrigin(logger))
	router.GET("/resource", func(c *gin.Context) { called++; c.Status(http.StatusNoContent) })
	const marker = "origin-private-canary"
	request := httptest.NewRequest(http.MethodGet, "http://edo.example/resource", nil)
	request.Header.Set("Connection", "keep-alive, Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Origin", "https://person:"+marker+"@edo.example")
	request.Header.Set("Cookie", "session="+marker)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || called != 0 || !strings.Contains(response.Body.String(), `"code":"websocket_origin_denied"`) {
		t.Fatalf("来源校验未在资源处理前拒绝: status=%d calls=%d", response.Code, called)
	}
	if strings.Contains(output.String(), marker) || strings.Contains(response.Body.String(), marker) || !strings.Contains(output.String(), `"operation":"websocket_origin"`) {
		t.Fatal("来源拒绝缺少安全诊断或回显了请求头内容")
	}

	// 普通 HTTP 请求不由 WebSocket 门禁处理；其会话、权限和 CORS 语义保持独立。
	request = httptest.NewRequest(http.MethodGet, "http://edo.example/resource", nil)
	request.Header.Set("Origin", "https://other.example")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || called != 1 {
		t.Fatal("普通 HTTP 被 WebSocket 来源规则误拦")
	}
}

func TestAllRegisteredWebSocketRoutesRejectUntrustedOriginBeforeLookup(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	cookie := realtimeLogin(t, router)
	count := 0
	for _, route := range router.Routes() {
		if route.Method != http.MethodGet || !strings.HasSuffix(route.Path, "/ws") {
			continue
		}
		count++
		parts := strings.Split(route.Path, "/")
		for index, part := range parts {
			if strings.HasPrefix(part, ":") {
				parts[index] = "origin-test-target"
			}
		}
		request := httptest.NewRequest(http.MethodGet, "http://edo.example"+strings.Join(parts, "/"), nil)
		request.AddCookie(cookie)
		request.Header.Set("X-EDO-Project-ID", database.DefaultProjectID(database.DefaultDepartmentID))
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "websocket")
		request.Header.Set("Origin", "http://edo.example:18080")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"websocket_origin_denied"`) {
			t.Errorf("来源不可信时仍进入资源查询: route=%s status=%d", route.Path, response.Code)
		}
	}
	if count != 8 {
		t.Fatalf("WebSocket 路由数量变化，需复核门禁覆盖: %d", count)
	}
	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/auth/me", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatal("拒绝跨来源连接不应更改合法会话")
	}
}

func TestWebSocketSameOriginAndNonBrowserStillRequireAuthentication(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	server := httptest.NewServer(router)
	defer server.Close()
	cookie := realtimeLogin(t, router)
	for _, browser := range []bool{false, true} {
		header := http.Header{}
		if browser {
			header.Set("Origin", server.URL)
		}
		dialer := websocket.Dialer{Subprotocols: []string{stateSubprotocol}, HandshakeTimeout: 3 * time.Second}
		url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/events/ws"
		connection, response, err := dialer.Dial(url, header)
		if connection != nil {
			_ = connection.Close()
		}
		if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("来源校验被误作身份授权: browser=%t", browser)
		}
		if response.Body != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		header.Set("Cookie", cookie.String())
		header.Set("X-EDO-Project-ID", database.DefaultProjectID(database.DefaultDepartmentID))
		connection, response, err = dialer.Dial(url, header)
		if err != nil {
			t.Fatalf("有效会话的正常来源连接失败: browser=%t err=%v", browser, err)
		}
		var event stateEvent
		_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
		if err := connection.ReadJSON(&event); err != nil || event.Type != "resync" {
			t.Fatalf("合法连接未取得状态同步: browser=%t", browser)
		}
		_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		_ = connection.Close()
	}
}
