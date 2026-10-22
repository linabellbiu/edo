package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/realtime"
)

const stateSubprotocol = "edo-state-v1"

type stateEvent struct {
	Type      string   `json:"type"`
	Resources []string `json:"resources,omitempty"`
}

func realtimeMutation(bus *realtime.Bus) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if bus == nil || c.Writer.Status() >= 300 || c.Request.Method == http.MethodGet {
			return
		}
		parts := strings.Split(strings.TrimPrefix(c.FullPath(), "/api/v1/"), "/")
		if len(parts) == 0 {
			return
		}
		resource := strings.ReplaceAll(parts[0], "-", "_")
		bus.Changed(c.Request.Context(), resource, c.Param("id"))
	}
}

// 长连接继续检查会话、权限与部门；调动部门或注销后不沿用握手时的权限。
func streamAuthorization(deps Dependencies, c *gin.Context, permissions ...string) func() bool {
	initial, ok := currentUser(c)
	if !ok {
		return func() bool { return false }
	}
	identity := *initial
	initialScope, _ := database.DepartmentScopeFromContext(c.Request.Context())
	token, _ := c.Cookie(deps.AuthConfig.CookieName)
	return func() bool {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		session, err := deps.Sessions.Get(ctx, token)
		if err == nil {
			var user *model.User
			user, err = deps.Accounts.FindByID(ctx, session.UserID)
			if err == nil && (user.ID != identity.ID || !user.IsActive || session.AuthVersion != user.AuthVersion || user.DepartmentID != identity.DepartmentID || user.IsSuperuser != identity.IsSuperuser) {
				err = errors.New("实时会话的用户或部门已变化")
			}
			if err == nil && initialScope.ProjectID != "" {
				project, scopeErr := deps.Access.ResolveProject(ctx, user, initialScope.ProjectID)
				if scopeErr != nil {
					err = scopeErr
				} else if !project.IsActive {
					err = errors.New("项目已停用")
				}
			}
			if err == nil {
				var allowed bool
				for _, permission := range permissions {
					allowed, err = deps.Access.HasPermission(ctx, user, permission)
					if err != nil || allowed {
						break
					}
				}
				if err == nil && !allowed {
					err = errors.New("实时会话的读取权限已撤销")
				}
			}
		}
		if err != nil {
			deps.Logger.Warn("实时会话授权已失效", "operation", "realtime_authorization", "user_id", identity.ID, "err", err)
		}
		return err == nil
	}
}

func streamState(deps Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requestedSocketProtocol(c.Request, stateSubprotocol) {
			deps.Logger.Warn("状态连接协议无效", "operation", "realtime_handshake", "request_id", requestIDFrom(c))
			writeError(c, http.StatusBadRequest, "invalid_state_protocol", "状态连接参数无效")
			return
		}
		user, _ := currentUser(c)
		subscription, err := deps.Realtime.SubscribeProject(c.Request.Context())
		if err != nil {
			deps.Logger.Warn("订阅状态通知失败", "operation", "realtime_subscribe", "request_id", requestIDFrom(c), "err", err)
			writeError(c, http.StatusServiceUnavailable, "realtime_unavailable", "实时状态暂不可用，已使用定时同步")
			return
		}
		defer subscription.Close()
		upgrader := websocket.Upgrader{HandshakeTimeout: 10 * time.Second, Subprotocols: []string{stateSubprotocol}, CheckOrigin: func(*http.Request) bool { return true }}
		connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			deps.Logger.Warn("状态连接握手失败", "operation", "realtime_upgrade", "request_id", requestIDFrom(c), "err", err)
			return
		}
		defer connection.Close()
		logger := deps.Logger.With("operation", "realtime_state_stream", "request_id", requestIDFrom(c), "user_id", user.ID)
		done := readSocketControl(connection, logger)
		defer closeSocketNormally(connection, done)
		authorized := streamAuthorization(deps, c, "application.read", "pipeline_run.read", "release_plan.read", "deployment_plan.read", access.PermissionDeploymentRead)
		write := func(event stateEvent) error {
			err := connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err == nil {
				err = connection.WriteJSON(event)
			}
			if err != nil {
				logger.Warn("发送状态事件失败", "event_type", event.Type, "err", err)
			}
			return err
		}
		if err := write(stateEvent{Type: "resync"}); err != nil {
			return
		}
		messages := subscription.ChannelWithSubscriptions(redis.WithChannelSize(64))
		flush := time.NewTicker(300 * time.Millisecond)
		defer flush.Stop()
		heartbeat := time.NewTicker(20 * time.Second)
		defer heartbeat.Stop()
		dirty := make(map[string]bool)
		for {
			select {
			case <-c.Request.Context().Done():
				return
			case <-done:
				return
			case incoming, open := <-messages:
				if !open {
					return
				}
				switch event := incoming.(type) {
				case *redis.Subscription:
					if !authorized() {
						closeSocketPolicy(connection)
						return
					}
					if err := write(stateEvent{Type: "resync"}); err != nil {
						return
					}
				case *redis.Message:
					switch event.Payload {
					case "applications", "application_sync", "pipeline_runs", "release_plans", "deployment_plans", "deployments":
						dirty[event.Payload] = true
					}
				}
			case <-flush.C:
				if len(dirty) == 0 {
					continue
				}
				if !authorized() {
					closeSocketPolicy(connection)
					return
				}
				resources := make([]string, 0, len(dirty))
				for resource := range dirty {
					permission := map[string]string{"applications": "application.read", "application_sync": "application.read", "pipeline_runs": "pipeline_run.read", "release_plans": "release_plan.read", "deployment_plans": "deployment_plan.read"}[resource]
					if resource == "deployments" {
						permission = access.PermissionDeploymentRead
					}
					allowed, permissionErr := deps.Access.HasPermission(c.Request.Context(), user, permission)
					if permissionErr != nil {
						deps.Logger.Warn("检查状态通知权限失败", "operation", "realtime_permission", "user_id", user.ID, "err", permissionErr)
						return
					}
					if allowed {
						resources = append(resources, resource)
					}
				}
				clear(dirty)
				if len(resources) > 0 {
					if err := write(stateEvent{Type: "changed", Resources: resources}); err != nil {
						return
					}
				}
			case <-heartbeat.C:
				if !authorized() {
					closeSocketPolicy(connection)
					return
				}
				if err := write(stateEvent{Type: "heartbeat"}); err != nil {
					return
				}
				if err := connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					logger.Warn("发送状态心跳失败", "err", err)
					return
				}
			}
		}
	}
}

func requestedSocketProtocol(request *http.Request, expected string) bool {
	for _, protocol := range websocket.Subprotocols(request) {
		if protocol == expected {
			return true
		}
	}
	return false
}

// 即使不接收业务输入也必须持续读帧，才能处理 Close/Ping/Pong。
func readSocketControl(connection *websocket.Conn, logger *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	connection.SetReadLimit(4096)
	_ = connection.SetReadDeadline(time.Now().Add(60 * time.Second))
	connection.SetPongHandler(func(string) error { return connection.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	go func() {
		defer close(done)
		for {
			if _, _, err := connection.ReadMessage(); err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.ClosePolicyViolation) {
					logger.Debug("实时连接已关闭", "err", err)
				} else {
					logger.Warn("实时连接异常断开", "err", err)
				}
				return
			}
		}
	}()
	return done
}

func closeSocketNormally(connection *websocket.Conn, done <-chan struct{}) {
	_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	// 等待对端确认关闭，避免代理仍转发关闭帧时底层 TCP 已被销毁。
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func closeSocketPolicy(connection *websocket.Conn) {
	_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "登录或权限已变化，请重新登录"), time.Now().Add(time.Second))
}
