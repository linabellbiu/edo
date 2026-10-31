package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"edo/internal/auth"
	"edo/internal/model"
)

const authEventsSubprotocol = "edo-auth-v1"

var errAuthEventsIdentityChanged = errors.New("自有授权通知的登录身份已变化")

type authEvent struct {
	Type string `json:"type"`
}

type authInvalidationRead struct {
	event any
	err   error
}

// 通知只属于握手时的当前会话，不以客户端提交的用户或项目作为订阅目标。
func authEventsSession(ctx context.Context, deps Dependencies, token string, identity model.User) (auth.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	session, err := deps.Sessions.Get(ctx, token)
	if err != nil {
		return auth.Session{}, err
	}
	if session.UserID != identity.ID {
		return auth.Session{}, errAuthEventsIdentityChanged
	}
	user, err := deps.Accounts.FindByID(ctx, identity.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return auth.Session{}, errAuthEventsIdentityChanged
	}
	if err != nil {
		return auth.Session{}, err
	}
	if !user.IsActive || user.AuthVersion != session.AuthVersion || user.DepartmentID != identity.DepartmentID || user.IsSuperuser != identity.IsSuperuser {
		return auth.Session{}, errAuthEventsIdentityChanged
	}
	return session, nil
}

func authEventsRevoked(err error) bool {
	return errors.Is(err, auth.ErrSessionNotFound) || errors.Is(err, errAuthEventsIdentityChanged)
}

func streamAuthEvents(deps Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requestedSocketProtocol(c.Request, authEventsSubprotocol) {
			deps.Logger.Warn("授权通知连接协议无效", "operation", "auth_events_handshake", "request_id", requestIDFrom(c))
			writeError(c, http.StatusBadRequest, "invalid_auth_events_protocol", "授权通知连接参数无效")
			return
		}
		user, ok := currentUser(c)
		if !ok || deps.Sessions == nil || deps.Accounts == nil || deps.Sessions.Invalidations() == nil {
			deps.Logger.Error("授权通知依赖不可用", "operation", "auth_events_dependencies", "request_id", requestIDFrom(c))
			writeError(c, http.StatusServiceUnavailable, "auth_events_unavailable", "权限核验暂不可用，请稍后重试")
			return
		}
		identity := *user
		token, _ := c.Cookie(deps.AuthConfig.CookieName)
		logger := deps.Logger.With("operation", "auth_events_stream", "request_id", requestIDFrom(c), "user_id", identity.ID)
		subscription, err := deps.Sessions.Invalidations().Subscribe(c.Request.Context(), identity.ID, token)
		if err != nil {
			logger.Warn("订阅自有授权通知失败", "err", err)
			writeError(c, http.StatusServiceUnavailable, "auth_events_unavailable", "权限核验暂不可用，请稍后重试")
			return
		}
		defer subscription.Close()
		// 先确认全部订阅，再核验身份；握手前发生的撤权不会从订阅间隙漏掉。
		session, err := authEventsSession(c.Request.Context(), deps, token, identity)
		if err != nil {
			logger.Warn("订阅后复核自有会话失败", "err", err)
			if authEventsRevoked(err) {
				unauthorized(c)
			} else {
				writeError(c, http.StatusServiceUnavailable, "auth_events_unavailable", "权限核验暂不可用，请稍后重试")
			}
			return
		}
		upgrader := websocket.Upgrader{HandshakeTimeout: 10 * time.Second, Subprotocols: []string{authEventsSubprotocol}}
		connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			logger.Warn("授权通知连接握手失败", "err", err)
			return
		}
		defer connection.Close()
		done := readSocketControl(connection, logger)
		defer closeSocketNormally(connection, done)
		closeUnavailable := func() {
			_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "权限核验暂不可用，请稍后重试"), time.Now().Add(time.Second))
		}
		validate := func() bool {
			if _, err := authEventsSession(c.Request.Context(), deps, token, identity); err != nil {
				logger.Warn("持续复核自有会话失败", "err", err)
				if authEventsRevoked(err) {
					closeSocketPolicy(connection)
				} else {
					closeUnavailable()
				}
				return false
			}
			return true
		}
		write := func(kind string) bool {
			err := connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err == nil {
				err = connection.WriteJSON(authEvent{Type: kind})
			}
			if err != nil {
				logger.Warn("发送自有授权通知失败", "event_type", kind, "err", err)
			}
			return err == nil
		}
		if !write("resync") {
			return
		}
		// 高级 Channel 接口会吞掉网络错误并自动重连；这里必须让页面立即暂停旧授权。
		streamContext, cancel := context.WithCancel(c.Request.Context())
		messages := make(chan authInvalidationRead, 1)
		receiverDone := make(chan struct{})
		go func() {
			defer close(receiverDone)
			for {
				event, err := subscription.ReceiveTimeout(streamContext, 30*time.Second)
				select {
				case messages <- authInvalidationRead{event: event, err: err}:
				case <-streamContext.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
		defer func() {
			cancel()
			// Close 先解除阻塞读取，再等待本连接唯一读取协程退出。
			_ = subscription.Close()
			<-receiverDone
		}()
		expiry := time.NewTimer(time.Until(session.ExpiresAt))
		defer expiry.Stop()
		heartbeat := time.NewTicker(20 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-c.Request.Context().Done():
				return
			case <-done:
				return
			case <-expiry.C:
				logger.Info("自有授权通知会话已到期")
				closeSocketPolicy(connection)
				return
			case incoming := <-messages:
				if incoming.err != nil {
					if c.Request.Context().Err() != nil {
						return
					}
					logger.Warn("自有授权订阅已断开", "err", incoming.err)
					closeUnavailable()
					return
				}
				kind := ""
				switch event := incoming.event.(type) {
				case *redis.Subscription:
					kind = "resync"
				case *redis.Message:
					if event.Payload == "stale" {
						kind = "changed"
					}
				}
				if kind != "" && (!validate() || !write(kind)) {
					return
				}
			case <-heartbeat.C:
				if !validate() || !write("heartbeat") {
					return
				}
				pingContext, pingCancel := context.WithTimeout(streamContext, 3*time.Second)
				err := subscription.Ping(pingContext)
				pingCancel()
				if err != nil {
					logger.Warn("核验自有授权订阅心跳失败", "err", err)
					closeUnavailable()
					return
				}
				if err := connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					logger.Warn("发送自有授权通知心跳失败", "err", err)
					return
				}
			}
		}
	}
}
