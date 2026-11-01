package httpapi

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	terminalservice "edo/internal/terminal"
)

// 取消或到期优先于其触发的后端关闭错误，活动会话仍保留真实退出原因。
func TestTerminalBackendExitPriority(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	backendError := errors.New("public backend failure fixture")
	for _, test := range []struct {
		name         string
		ctx          context.Context
		backendError error
		message      string
		want         error
	}{
		{"取消后lease关闭管道", canceled, io.ErrClosedPipe, "终端会话已结束", context.Canceled},
		{"取消后EOF", canceled, io.EOF, "终端会话已结束", context.Canceled},
		{"取消后正常退出", canceled, nil, "终端会话已结束", context.Canceled},
		{"期限后lease关闭管道", deadline, io.ErrClosedPipe, "终端会话已达到最长持续时间", context.DeadlineExceeded},
		{"期限后EOF", deadline, io.EOF, "终端会话已达到最长持续时间", context.DeadlineExceeded},
		{"期限后正常退出", deadline, nil, "终端会话已达到最长持续时间", context.DeadlineExceeded},
		{"未取消真实异常", context.Background(), backendError, "终端进程异常退出", backendError},
		{"未取消EOF", context.Background(), io.EOF, "终端进程已退出", io.EOF},
		{"未取消正常退出", context.Background(), nil, "终端进程已退出", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			event, got := terminalBackendExit(test.ctx, test.backendError)
			if event.Type != "exit" || event.Message != test.message || !errors.Is(got, test.want) {
				t.Fatalf("终端结束优先级不符: event=%+v err=%v want=%v", event, got, test.want)
			}
			if strings.Contains(event.Message, backendError.Error()) {
				t.Fatal("内部后端错误泄漏到退出事件")
			}
		})
	}
}

// 内存Hijacker只供真实Gorilla握手/关闭帧使用；不监听端口、DNS或连接运行环境。
type terminalMemoryWriter struct {
	connection net.Conn
	reader     *bufio.Reader
	header     http.Header
}

func (w *terminalMemoryWriter) Header() http.Header       { return w.header }
func (*terminalMemoryWriter) WriteHeader(int)             {}
func (*terminalMemoryWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *terminalMemoryWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.connection, bufio.NewReadWriter(w.reader, bufio.NewWriter(w.connection)), nil
}

// 验真实open收尾：即使bridge正常因取消结束，Close失败仍须以1011/稳定回收失败文案胜出。
func TestTerminalBackendExitCleanupFailureStillWins(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "回收成功", true: "回收失败"}[fail], func(t *testing.T) {
			serverPipe, clientPipe := net.Pipe()
			_ = serverPipe.SetDeadline(time.Now().Add(4 * time.Second))
			_ = clientPipe.SetDeadline(time.Now().Add(4 * time.Second))
			session := &cleanupResultSession{fakeTerminalSession: newFakeTerminalSession()}
			if fail {
				session.cleanupErr = errors.New("private cleanup diagnostic fixture")
			}
			defer session.fakeTerminalSession.Close()
			defer serverPipe.Close()
			defer clientPipe.Close()
			h := terminalHandler{service: terminalservice.NewService(nil, nil, time.Minute), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			cancelReady := make(chan context.CancelFunc, 1)
			router := gin.New()
			router.GET("/terminal", func(c *gin.Context) {
				ctx, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
				cancelReady <- cancel
				h.open(c, "docker", "public-memory-fixture", map[string]any{}, func(context.Context, string, terminalservice.Size) (terminalservice.Session, error) {
					return session, nil
				})
			})
			done := make(chan error, 1)
			go func() {
				reader := bufio.NewReader(serverPipe)
				request, err := http.ReadRequest(reader)
				if err == nil {
					writer := &terminalMemoryWriter{connection: serverPipe, reader: reader, header: make(http.Header)}
					router.ServeHTTP(writer, request)
				}
				done <- err
			}()
			connection, _, err := websocket.NewClient(clientPipe, &url.URL{Scheme: "ws", Host: "fixture.invalid", Path: "/terminal"}, http.Header{"Sec-Websocket-Protocol": []string{terminalSubprotocol}}, 1024, 1024)
			if err != nil {
				t.Fatal("内存WebSocket握手失败", err)
			}
			defer connection.Close()
			_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
			var event terminalEvent
			if err := connection.ReadJSON(&event); err != nil || event.Type != "ready" {
				t.Fatal("内存终端没有真实ready", err)
			}
			(<-cancelReady)()
			if err := connection.ReadJSON(&event); err != nil || event.Type != "exit" || event.Message != "终端会话已结束" {
				t.Fatalf("取消后没有准确exit: event=%+v err=%v", event, err)
			}
			_, _, err = connection.ReadMessage()
			var closed *websocket.CloseError
			wantCode, wantMessage := websocket.CloseNormalClosure, "终端会话已结束"
			if fail {
				wantCode, wantMessage = websocket.CloseInternalServerErr, "终端资源释放失败，请联系管理员"
			}
			if !errors.As(err, &closed) || closed.Code != wantCode || closed.Text != wantMessage {
				t.Fatalf("回收错误优先级被取消吞掉: err=%v want=%d/%s", err, wantCode, wantMessage)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal("内存处理器异常结束", err)
				}
			case <-time.After(time.Second):
				t.Fatal("内存终端处理器没有收口")
			}
			if session.calls.Load() != 1 {
				t.Fatalf("重复回收底层会话: %d", session.calls.Load())
			}
		})
	}
}
