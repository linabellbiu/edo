package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/auth"
	"edo/internal/cache"
	"edo/internal/config"
	"edo/internal/model"
)

// 自有授权通知不依赖业务读取权限，也不能被旧项目筛选阻断。
func TestAuthEventsWebSocketOnlyRequiresOwnSession(t *testing.T) {
	var deps Dependencies
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, _ *gorm.DB) { deps = *value })
	defer cleanup()
	user, err := deps.Accounts.CreateUser(context.Background(), "auth-events-reader", "授权通知测试", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.Access.SetUserPermissions(context.Background(), user.ID, access.UserPermissionOverrides{Allow: []string{access.PermissionAuditRead}}); err != nil {
		t.Fatal(err)
	}
	cookie := loginForDepartmentTest(t, router, user.Username)
	server := httptest.NewServer(router)
	defer server.Close()
	header := http.Header{"Cookie": {cookie.String()}, "Origin": {server.URL}}
	dialer := websocket.Dialer{Subprotocols: []string{"edo-auth-v1"}, HandshakeTimeout: 3 * time.Second}
	connection, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/auth/events/ws?project_id=removed-project&user_id=another-user", header)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("合法自有会话未能订阅授权通知: status=%d err=%v", status, err)
	}
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, payload, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]json.RawMessage
	if err := json.Unmarshal(payload, &event); err != nil || len(event) != 1 || string(event["type"]) != `"resync"` {
		t.Fatalf("自有通知应仅包含重验信号，不携带其他用户或业务数据: %s", payload)
	}
	if connection.Subprotocol() != "edo-auth-v1" {
		t.Fatal("未协商自有授权通知协议")
	}
	_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	if _, _, err := connection.ReadMessage(); !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
		t.Fatalf("自有授权通知未正常关闭: %v", err)
	}
}

type authEventsDialObserver struct{ connections chan net.Conn }

func (observer authEventsDialObserver) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		connection, err := next(ctx, network, address)
		if err == nil {
			select {
			case observer.connections <- connection:
			default:
			}
		}
		return connection, err
	}
}
func (authEventsDialObserver) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }
func (authEventsDialObserver) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestAuthEventsSubscriptionFailureSuspendsEvenWhenSessionStoreWorks(t *testing.T) {
	var deps Dependencies
	var client *cache.Redis
	connections := make(chan net.Conn, 8)
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, _ *gorm.DB) {
		redisServer := miniredis.RunT(t)
		var err error
		client, err = cache.Open(context.Background(), config.Redis{URL: "redis://" + redisServer.Addr(), KeyPrefix: "edo:auth-events-isolated-disconnect:", Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		value.Sessions = auth.NewSessionStore(client, time.Hour)
		deps = *value
	})
	defer cleanup()
	user, err := deps.Accounts.CreateUser(context.Background(), "auth-events-disconnect", "订阅断连", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := deps.Sessions.Create(context.Background(), user.ID, user.AuthVersion)
	if err != nil {
		t.Fatal(err)
	}
	// 普通命令池已由 Open/PING 与 Create 预热；SDK Hook 只观察后续新建的专用订阅连接。
	client.Client().AddHook(authEventsDialObserver{connections: connections})
	server := httptest.NewServer(router)
	defer server.Close()
	cookie := &http.Cookie{Name: deps.AuthConfig.CookieName, Value: token}
	connection := dialRealtime(t, server.URL, "/api/v1/auth/events/ws", "edo-auth-v1", cookie)
	defer connection.Close()
	authEventsRead(t, connection, "resync")
	var subscriptionConnection net.Conn
	select {
	case subscriptionConnection = <-connections:
	case <-time.After(time.Second):
		t.Fatal("未观察到专用订阅连接")
	}
	if len(connections) != 0 {
		t.Fatal("存在额外新连接，不能准确注入单订阅故障")
	}
	if _, err := deps.Sessions.Get(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if err := subscriptionConnection.Close(); err != nil {
		t.Fatal(err)
	}
	authEventsClose(t, connection, websocket.CloseTryAgainLater)
	if _, err := deps.Sessions.Get(context.Background(), token); err != nil {
		t.Fatalf("只断订阅不得损坏普通会话: %v", err)
	}
	// 用正常入口重新建立全部订阅，首帧要求重验而不是沿用旧授权快照。
	reconnected := dialRealtime(t, server.URL, "/api/v1/auth/events/ws", "edo-auth-v1", cookie)
	defer reconnected.Close()
	authEventsRead(t, reconnected, "resync")
	_ = reconnected.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	authEventsClose(t, reconnected, websocket.CloseNormalClosure)
}

func TestAuthEventsPeerCloseTextDoesNotEnterLogs(t *testing.T) {
	var logs bytes.Buffer
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, _ *gorm.DB) {
		value.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	defer cleanup()
	cookie := realtimeLogin(t, router)
	handled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r); close(handled) }))
	defer server.Close()
	connection := dialRealtime(t, server.URL, "/api/v1/auth/events/ws", "edo-auth-v1", cookie)
	defer connection.Close()
	authEventsRead(t, connection, "resync")
	const canary = "synthetic-peer-close-private-canary"
	_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, canary), time.Now().Add(time.Second))
	authEventsClose(t, connection, websocket.CloseNormalClosure)
	select {
	case <-handled:
	case <-time.After(3 * time.Second):
		t.Fatal("关闭后处理器未退出")
	}
	if strings.Contains(logs.String(), canary) || strings.Contains(logs.String(), cookie.Value) {
		t.Fatal("客户端关闭理由或会话凭据进入日志")
	}
	if !strings.Contains(logs.String(), `"close_code":1000`) {
		t.Fatal("安全关闭码诊断缺失")
	}
}

func authEventsRead(t *testing.T, connection *websocket.Conn, expected string) {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(4 * time.Second))
	_, payload, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]string
	if err := json.Unmarshal(payload, &value); err != nil || len(value) != 1 || value["type"] != expected {
		t.Fatalf("授权通知内容无效: expected=%s payload=%s err=%v", expected, payload, err)
	}
}

func authEventsClose(t *testing.T, connection *websocket.Conn, code int) {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(4 * time.Second))
	_, _, err := connection.ReadMessage()
	if !websocket.IsCloseError(err, code) {
		t.Fatalf("授权通知关闭状态错误: want=%d err=%v", code, err)
	}
}

func TestAuthEventsNormalPermissionChangeAndSessionLogout(t *testing.T) {
	var deps Dependencies
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, _ *gorm.DB) { deps = *value })
	defer cleanup()
	user, err := deps.Accounts.CreateUser(context.Background(), "auth-events-live", "权限即时变更", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.Access.SetUserPermissions(context.Background(), user.ID, access.UserPermissionOverrides{Allow: []string{access.PermissionAuditRead}}); err != nil {
		t.Fatal(err)
	}
	cookie := loginForDepartmentTest(t, router, user.Username)
	other := loginForDepartmentTest(t, router, user.Username)
	admin := realtimeLogin(t, router)
	server := httptest.NewServer(router)
	defer server.Close()
	first := dialRealtime(t, server.URL, "/api/v1/auth/events/ws", "edo-auth-v1", cookie)
	defer first.Close()
	second := dialRealtime(t, server.URL, "/api/v1/auth/events/ws", "edo-auth-v1", other)
	defer second.Close()
	authEventsRead(t, first, "resync")
	authEventsRead(t, second, "resync")
	changed := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+user.ID+"/permissions", map[string]any{"allow": []string{}, "deny": []string{access.PermissionAuditRead}}, admin)
	if changed.Code != http.StatusNoContent {
		t.Fatalf("正常权限修改失败: status=%d body=%s", changed.Code, changed.Body.String())
	}
	authEventsRead(t, first, "changed")
	authEventsRead(t, second, "changed")
	profile := sessionBoundaryRequest(t, router, "/api/v1/auth/me", cookie)
	if profile.Code != http.StatusOK || strings.Contains(profile.Body.String(), `"audit.read"`) {
		t.Fatalf("重验仍返回已撤销权限: status=%d body=%s", profile.Code, profile.Body.String())
	}
	logout := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/logout", nil, cookie)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("正常退出失败: %d", logout.Code)
	}
	authEventsClose(t, first, websocket.ClosePolicyViolation)
	// 退出只结束该设备，另一设备仍能重验，且不会收到他人的会话通知。
	deps.Sessions.Invalidations().PolicyChanged(context.Background())
	authEventsRead(t, second, "changed")
	_ = second.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	authEventsClose(t, second, websocket.CloseNormalClosure)
}

func TestAuthEventsIdentityChangesCloseWithoutBusinessRead(t *testing.T) {
	for _, mode := range []string{"停用", "认证版本", "部门", "删除"} {
		t.Run(mode, func(t *testing.T) {
			var deps Dependencies
			var db *gorm.DB
			router, cleanup := newAuthTestRouter(t, func(value *Dependencies, valueDB *gorm.DB) { deps = *value; db = valueDB })
			defer cleanup()
			user, err := deps.Accounts.CreateUser(context.Background(), "auth-events-identity", "身份失效", "correct horse battery staple")
			if err != nil {
				t.Fatal(err)
			}
			cookie := loginForDepartmentTest(t, router, user.Username)
			server := httptest.NewServer(router)
			defer server.Close()
			connection := dialRealtime(t, server.URL, "/api/v1/auth/events/ws", "edo-auth-v1", cookie)
			defer connection.Close()
			authEventsRead(t, connection, "resync")
			switch mode {
			case "停用":
				err = deps.Accounts.SetActive(context.Background(), user.ID, false)
			case "认证版本":
				err = deps.Accounts.ChangePassword(context.Background(), user.ID, "correct horse battery staple", "changed correct password 123")
			case "部门":
				department := model.Department{ID: "auth-events-other-department", Name: "调入部门", IsActive: true}
				if err = db.Create(&department).Error; err == nil {
					err = deps.Accounts.SetDepartment(context.Background(), user.ID, department.ID)
				}
			case "删除":
				// 删除查询的基础判因单独验证，不把这一项当正常用户删除页面证据。
				err = db.Where("uuid = ?", user.ID).Delete(&model.User{}).Error
				deps.Sessions.Invalidations().UserChanged(context.Background(), user.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			authEventsClose(t, connection, websocket.ClosePolicyViolation)
		})
	}
}

func TestAuthEventsUnavailableIsNotRevocation(t *testing.T) {
	var deps Dependencies
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, valueDB *gorm.DB) { deps = *value; db = valueDB })
	defer cleanup()
	cookie := realtimeLogin(t, router)
	server := httptest.NewServer(router)
	defer server.Close()
	connection := dialRealtime(t, server.URL, "/api/v1/auth/events/ws", "edo-auth-v1", cookie)
	defer connection.Close()
	authEventsRead(t, connection, "resync")
	const callback = "test:auth_events_query_failure"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) { tx.AddError(context.DeadlineExceeded) }); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove(callback)
	deps.Sessions.Invalidations().PolicyChanged(context.Background())
	authEventsClose(t, connection, websocket.CloseTryAgainLater)
}

func TestAuthEventsSessionExpiryDoesNotWaitForHeartbeat(t *testing.T) {
	var deps Dependencies
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, _ *gorm.DB) {
		redisServer := miniredis.RunT(t)
		client, err := cache.Open(context.Background(), config.Redis{URL: "redis://" + redisServer.Addr(), KeyPrefix: "edo:auth-events-expiry:", Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		value.Sessions = auth.NewSessionStore(client, time.Second)
		deps = *value
	})
	defer cleanup()
	user, err := deps.Accounts.CreateUser(context.Background(), "auth-events-expiry", "到期测试", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	// 正常会话存储的绝对到期判因；不修改系统时间或原测试环境。
	token, session, err := deps.Sessions.Create(context.Background(), user.ID, user.AuthVersion)
	if err != nil {
		t.Fatal(err)
	}
	identity := *user
	ctx := context.Background()
	if _, err := authEventsSession(ctx, deps, token, identity); err != nil {
		t.Fatal(err)
	}
	identity.ID = "not-the-session-user"
	if _, err := authEventsSession(ctx, deps, token, identity); !authEventsRevoked(err) {
		t.Fatalf("会话主体变更未拒绝: %v", err)
	}
	if authEventsRevoked(context.DeadlineExceeded) || authEventsRevoked(context.Canceled) {
		t.Fatal("基础设施失败不能当撤权")
	}
	if !authEventsRevoked(auth.ErrSessionNotFound) || session.ExpiresAt.Before(time.Now()) {
		t.Fatal("到期判因异常")
	}
	server := httptest.NewServer(router)
	defer server.Close()
	connection := dialRealtime(t, server.URL, "/api/v1/auth/events/ws", "edo-auth-v1", &http.Cookie{Name: deps.AuthConfig.CookieName, Value: token})
	defer connection.Close()
	authEventsRead(t, connection, "resync")
	started := time.Now()
	authEventsClose(t, connection, websocket.ClosePolicyViolation)
	if time.Since(started) > 2*time.Second {
		t.Fatal("会话绝对到期不应等待20秒心跳")
	}
}
