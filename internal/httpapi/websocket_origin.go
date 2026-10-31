package httpapi

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// 在鉴权和打开运行资源前拒绝跨来源升级；SameSite Cookie 不区分同站点的端口。
func requireWebSocketOrigin(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !websocket.IsWebSocketUpgrade(c.Request) || allowedWebSocketOrigin(c.Request) {
			c.Next()
			return
		}
		// 来源可能包含伪造的凭据，不能把请求头原文写入日志或响应。
		logger.Warn("拒绝跨来源 WebSocket 连接", "operation", "websocket_origin", "request_id", requestIDFrom(c), "route", c.FullPath())
		writeError(c, http.StatusForbidden, "websocket_origin_denied", "不允许跨来源连接")
		c.Abort()
	}
}

func allowedWebSocketOrigin(request *http.Request) bool {
	origins := request.Header.Values("Origin")
	// 非浏览器客户端可不发送 Origin，但仍须通过会话、项目及动作鉴权。
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 || origins[0] == "" {
		return false
	}
	origin := origins[0]
	for _, value := range origin {
		if value > 127 {
			return false
		}
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" {
		return false
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	// 浏览器的来源只有协议与 authority，不接受路径、查询、片段或来源列表。
	if !strings.EqualFold(origin, parsed.Scheme+"://"+parsed.Host) {
		return false
	}
	// 保留端口；只信任实际 Host，不用客户端提供的转发头扩大来源范围。
	return strings.EqualFold(parsed.Host, request.Host)
}
