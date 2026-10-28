package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRouterClientIPTrustBoundary(t *testing.T) {
	for _, test := range []struct {
		name         string
		proxies      []string
		peer         string
		forwardedFor string
		realIP       string
		want         string
	}{
		{name: "默认不信任伪造头", peer: "192.0.2.10:1234", forwardedFor: "198.51.100.20", realIP: "198.51.100.21", want: "192.0.2.10"},
		{name: "明确可信IPv4代理", proxies: []string{"192.0.2.10"}, peer: "192.0.2.10:1234", forwardedFor: "198.51.100.20", want: "198.51.100.20"},
		{name: "可信CIDR", proxies: []string{"192.0.2.0/28"}, peer: "192.0.2.10:1234", realIP: "198.51.100.20", want: "198.51.100.20"},
		{name: "不可信私网构建来源", proxies: []string{"192.0.2.10"}, peer: "172.30.0.2:1234", forwardedFor: "198.51.100.20", realIP: "198.51.100.21", want: "172.30.0.2"},
		{name: "恶意XFF左侧不越过真实来源", proxies: []string{"192.0.2.10"}, peer: "192.0.2.10:1234", forwardedFor: "198.51.100.99, 198.51.100.20", want: "198.51.100.20"},
		{name: "可信代理链", proxies: []string{"192.0.2.10", "192.0.2.11"}, peer: "192.0.2.10:1234", forwardedFor: "198.51.100.20, 192.0.2.11", want: "198.51.100.20"},
		{name: "非法转发值回退连接来源", proxies: []string{"192.0.2.10"}, peer: "192.0.2.10:1234", forwardedFor: "bad-client-ip", realIP: "invalid", want: "192.0.2.10"},
		{name: "明确可信IPv6代理", proxies: []string{"2001:db8::/64"}, peer: "[2001:db8::10]:1234", forwardedFor: "2001:db8:1::20", want: "2001:db8:1::20"},
		{name: "非法配置不能保留部分信任", proxies: []string{"192.0.2.10", "invalid"}, peer: "192.0.2.10:1234", forwardedFor: "198.51.100.20", want: "192.0.2.10"},
		{name: "全网配置失败闭合", proxies: []string{"0.0.0.0/0"}, peer: "192.0.2.10:1234", forwardedFor: "198.51.100.20", want: "192.0.2.10"},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := NewRouter(Dependencies{Environment: "test", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TrustedProxies: test.proxies})
			router.GET("/client-ip", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })
			request := httptest.NewRequest(http.MethodGet, "/client-ip", nil)
			request.RemoteAddr = test.peer
			request.Header.Set("X-Forwarded-For", test.forwardedFor)
			request.Header.Set("X-Real-IP", test.realIP)
			request.Header.Set("Forwarded", "for=198.51.100.254")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Body.String() != test.want {
				t.Fatalf("客户端来源越过可信边界: status=%d got=%s want=%s", response.Code, response.Body.String(), test.want)
			}
		})
	}
}
