package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"edo/internal/model"
	terminalservice "edo/internal/terminal"
)

func TestTerminalClientCloseAudit(t *testing.T) {
	for _, test := range []struct {
		name   string
		rawEOF bool
	}{
		{name: "正常关闭帧1000"},
		{name: "底层连接直接断开", rawEOF: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newStreamBoundaryFixture(t)
			resourceID := uuid.NewString()
			session := newClientCloseAuditIdleSession()
			handlerDone := make(chan struct{})
			handler := terminalHandler{
				service: terminalservice.NewService(nil, nil, time.Minute), audits: f.deps.Audits, logger: f.deps.Logger,
				streamAuth: func(c *gin.Context) func() bool { return streamAuthorization(f.deps, c, "terminal.open") },
			}
			// 仅替换终端后端，保留正式鉴权、open、bridge、Close 与审计链。
			f.router.GET("/api/v1/terminals/client-close-audit/ws",
				requireAuth(f.deps.Accounts, f.deps.Sessions, f.deps.Logger, f.deps.AuthConfig.CookieName),
				projectScope(f.deps.Access, f.deps.Logger),
				requirePermission(f.deps.Access, f.deps.Logger, "terminal.open"),
				func(c *gin.Context) {
					defer close(handlerDone)
					session.requestContext = c.Request.Context()
					handler.open(c, "docker", resourceID, map[string]any{}, func(context.Context, string, terminalservice.Size) (terminalservice.Session, error) {
						return session, nil
					})
				},
			)
			server := httptest.NewServer(f.router)
			var connection *websocket.Conn
			t.Cleanup(func() {
				if connection != nil {
					_ = connection.Close()
				}
				// 失败时只释放测试协程；不调用 Close 来制造被测关闭证据。
				session.releaseForCleanup()
				server.CloseClientConnections()
				server.Close()
			})
			header := http.Header{"Cookie": []string{f.cookie.String()}, "X-EDO-Project-ID": []string{f.project.ID}}
			dialer := websocket.Dialer{Subprotocols: []string{terminalSubprotocol}, HandshakeTimeout: 3 * time.Second}
			var err error
			connection, _, err = dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/terminals/client-close-audit/ws", header)
			if err != nil {
				t.Fatalf("建立测试终端失败: %v", err)
			}
			_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
			var event terminalEvent
			if err := connection.ReadJSON(&event); err != nil || event.Type != "ready" {
				t.Fatalf("终端未就绪: type=%q err=%v", event.Type, err)
			}
			waitClientCloseAuditSignal(t, session.readStarted, "后端读取没有开始")
			if session.closeCalls.Load() != 0 {
				t.Fatal("客户端关闭前后端会话已被关闭")
			}
			select {
			case <-session.readExited:
				t.Fatal("客户端关闭前后端读取已自行结束")
			default:
			}

			readAudits := func() []model.AuditLog {
				t.Helper()
				var entries []model.AuditLog
				if err := f.db.WithContext(f.scope()).Where("resource_type = ? AND resource_id = ?", "terminal", resourceID).Order("id ASC").Find(&entries).Error; err != nil {
					t.Fatalf("读取本会话审计失败: %v", err)
				}
				return entries
			}
			opened := readAudits()
			if len(opened) != 1 || opened[0].Action != "terminal.open" || opened[0].Result != model.AuditSucceeded {
				t.Fatalf("关闭前应只有一次成功打开审计: count=%d", len(opened))
			}
			openedSessionID := clientCloseAuditSessionID(t, opened[0])
			if test.rawEOF {
				err = connection.UnderlyingConn().Close()
			} else {
				err = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(3*time.Second))
			}
			if err != nil {
				t.Fatalf("客户端发起关闭失败: %v", err)
			}

			// 必须在任何 Cleanup 执行前证明 bridge 返回并真实调用底层 Close。
			waitClientCloseAuditSignal(t, session.closeCalled, "客户端关闭后未调用会话 Close")
			waitClientCloseAuditSignal(t, session.readExited, "会话 Close 后阻塞读取未退出")
			waitClientCloseAuditSignal(t, handlerDone, "终端处理器与关闭审计未完成")
			if got := session.closeCalls.Load(); got != 1 {
				t.Fatalf("会话 Close 调用次数不为一次: got=%d", got)
			}
			closed := readAudits()
			if len(closed) != 2 {
				t.Fatalf("关闭后应保留原打开审计及唯一关闭审计: count=%d", len(closed))
			}
			if closed[0].ID != opened[0].ID || closed[0].Action != "terminal.open" {
				t.Fatalf("原打开审计没有保留或顺序错误: same_id=%t action=%q", closed[0].ID == opened[0].ID, closed[0].Action)
			}
			if closed[1].Action != "terminal.close" {
				t.Fatalf("关闭审计动作错误: action=%q", closed[1].Action)
			}
			if !test.rawEOF {
				if closed[1].Result != model.AuditSucceeded {
					t.Fatalf("正常关闭帧的审计结果错误: result=%q", closed[1].Result)
				}
			} else {
				switch closed[1].Result {
				case model.AuditSucceeded:
					// HTTP 复用读取器可能先取消原请求，使 bridge 先选择取消分支。
					if !session.requestCanceledAtClose.Load() {
						t.Fatal("原始断连记录成功，但 Close 入口的原请求未取消")
					}
				case model.AuditFailed:
					// bridge 先取得 WebSocket 1006 时，保留异常关闭分类。
				default:
					t.Fatalf("原始断连的审计结果无效: result=%q", closed[1].Result)
				}
			}
			for _, entry := range closed {
				if entry.ActorUserID == nil || *entry.ActorUserID != f.user.ID || clientCloseAuditSessionID(t, entry) != openedSessionID {
					t.Fatal("打开与关闭审计没有绑定同一用户、资源和会话")
				}
			}
			t.Logf("关闭链已完成: raw_eof=%t close_calls=%d request_canceled_at_close=%t audit_result=%q", test.rawEOF, session.closeCalls.Load(), session.requestCanceledAtClose.Load(), closed[1].Result)
			if !test.rawEOF {
				_, _, err = connection.ReadMessage()
				if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
					t.Fatalf("正常客户端关闭未收到正常关闭帧: %v", err)
				}
			}
		})
	}
}

func clientCloseAuditSessionID(t *testing.T, entry model.AuditLog) string {
	t.Helper()
	var metadata struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(entry.Metadata, &metadata); err != nil {
		t.Fatalf("读取终端审计会话标识失败: %v", err)
	}
	if _, err := uuid.Parse(metadata.SessionID); err != nil {
		t.Fatal("终端审计缺少有效会话标识")
	}
	return metadata.SessionID
}

func waitClientCloseAuditSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatal(message)
	}
}

type clientCloseAuditIdleSession struct {
	requestContext         context.Context
	readStarted            chan struct{}
	readExited             chan struct{}
	closeCalled            chan struct{}
	released               chan struct{}
	readOnce               sync.Once
	readEndOnce            sync.Once
	closeOnce              sync.Once
	releaseOnce            sync.Once
	closeCalls             atomic.Int32
	requestCanceledAtClose atomic.Bool
}

func newClientCloseAuditIdleSession() *clientCloseAuditIdleSession {
	return &clientCloseAuditIdleSession{
		readStarted: make(chan struct{}), readExited: make(chan struct{}),
		closeCalled: make(chan struct{}), released: make(chan struct{}),
	}
}

func (s *clientCloseAuditIdleSession) Read([]byte) (int, error) {
	s.readOnce.Do(func() { close(s.readStarted) })
	<-s.released
	s.readEndOnce.Do(func() { close(s.readExited) })
	return 0, io.EOF
}

func (*clientCloseAuditIdleSession) Write(payload []byte) (int, error) { return len(payload), nil }

func (*clientCloseAuditIdleSession) Resize(context.Context, terminalservice.Size) error { return nil }

func (s *clientCloseAuditIdleSession) Close() error {
	s.requestCanceledAtClose.Store(s.requestContext != nil && errors.Is(s.requestContext.Err(), context.Canceled))
	s.closeCalls.Add(1)
	s.closeOnce.Do(func() { close(s.closeCalled) })
	s.releaseForCleanup()
	return nil
}

func (s *clientCloseAuditIdleSession) releaseForCleanup() {
	s.releaseOnce.Do(func() { close(s.released) })
}

var _ terminalservice.Session = (*clientCloseAuditIdleSession)(nil)
