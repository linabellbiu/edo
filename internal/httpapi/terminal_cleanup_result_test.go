package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/audit"
	"edo/internal/model"
	terminalservice "edo/internal/terminal"
	"edo/internal/testutil"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type cleanupResultSession struct {
	*fakeTerminalSession
	cleanupErr error
	calls      atomic.Int32
}

func (s *cleanupResultSession) Close() error {
	s.calls.Add(1)
	return errors.Join(s.fakeTerminalSession.Close(), s.cleanupErr)
}

func TestTerminalCleanupFailureIsNotReportedAsSuccessfulClose(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "回收成功", true: "回收未确认"}[fail], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db, mock := testutil.NewSQLMock(t)
			actor := &model.User{ID: uuid.NewString()}
			resource := "public-terminal-fixture"
			var logs bytes.Buffer
			h := terminalHandler{service: terminalservice.NewService(nil, nil, time.Minute), audits: audit.NewService(db), logger: slog.New(slog.NewTextHandler(&logs, nil))}
			session := &cleanupResultSession{fakeTerminalSession: newFakeTerminalSession()}
			const diagnostic = "fixture cleanup diagnostic must remain internal"
			wantResult, wantCode := model.AuditSucceeded, websocket.CloseNormalClosure
			wantMessage := "终端会话已结束"
			if fail {
				session.cleanupErr = errors.New(diagnostic)
				wantResult, wantCode = model.AuditFailed, websocket.CloseInternalServerErr
				wantMessage = "终端资源释放失败，请联系管理员"
			}
			for _, entry := range []struct {
				action string
				result model.AuditResult
			}{{"terminal.open", model.AuditSucceeded}, {"terminal.close", wantResult}} {
				mock.ExpectBegin()
				mock.ExpectExec(`INSERT INTO "audit_logs"`).WithArgs(
					sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), actor.ID,
					entry.action, "terminal", resource, string(entry.result), sqlmock.AnyArg(),
					sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
				).WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit()
			}
			done := make(chan struct{})
			router := gin.New()
			router.GET("/terminal", func(c *gin.Context) {
				defer close(done)
				c.Set(currentUserKey, actor)
				h.open(c, "docker", resource, map[string]any{}, func(context.Context, string, terminalservice.Size) (terminalservice.Session, error) {
					return session, nil
				})
			})
			server := httptest.NewServer(router)
			t.Cleanup(func() { _ = session.fakeTerminalSession.Close(); server.CloseClientConnections(); server.Close() })
			dialer := websocket.Dialer{Subprotocols: []string{terminalSubprotocol}, HandshakeTimeout: time.Second}
			connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/terminal", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
			var event terminalEvent
			if err := connection.ReadJSON(&event); err != nil || event.Type != "ready" {
				t.Fatal("模拟终端没有就绪", err)
			}
			_ = session.outputWriter.Close()
			for {
				_, payload, readErr := connection.ReadMessage()
				if strings.Contains(string(payload), diagnostic) {
					t.Fatal("内部回收错误泄漏到终端协议")
				}
				if readErr != nil {
					var closeErr *websocket.CloseError
					if !errors.As(readErr, &closeErr) || closeErr.Code != wantCode || closeErr.Text != wantMessage {
						t.Errorf("关闭结果错误或泄漏诊断: %v", readErr)
					}
					break
				}
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("关闭处理器没有结束")
			}
			if session.calls.Load() != 1 {
				t.Fatalf("重复执行底层回收: %d", session.calls.Load())
			}
			if fail && !strings.Contains(logs.String(), diagnostic) {
				t.Fatal("内部日志缺少原始回收错误")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal("回收结果审计不匹配", err)
			}
		})
	}
}

var _ io.ReadWriteCloser = (*cleanupResultSession)(nil)
