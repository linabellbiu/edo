package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	terminalservice "edo/internal/terminal"
)

func TestTerminalServerEndSendsExitAndCloseFrame(t *testing.T) {
	causes := []string{"cancel", "deadline", "backend-exit", "backend-error", "open-error"}
	for range 24 {
		causes = append(causes, "cancel-lease")
	}
	for _, cause := range causes {
		t.Run(cause, func(t *testing.T) {
			session := newFakeTerminalSession()
			defer session.Close()
			budget := time.Minute
			if cause == "deadline" {
				budget = 150 * time.Millisecond
			}
			handler := terminalHandler{
				service: terminalservice.NewService(nil, nil, budget),
				logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			cancelReady := make(chan context.CancelFunc, 1)
			handlerDone := make(chan struct{})
			router := gin.New()
			router.GET("/terminal", func(c *gin.Context) {
				ctx, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
				cancelReady <- cancel
				handler.open(c, "docker", "public-fixture", map[string]any{}, func(ctx context.Context, _ string, _ terminalservice.Size) (terminalservice.Session, error) {
					if cause == "open-error" {
						return nil, errors.New("public fixture unavailable")
					}
					if cause == "cancel-lease" {
						context.AfterFunc(ctx, func() { _ = session.Close() })
					}
					return session, nil
				})
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				router.ServeHTTP(w, r)
			}))
			defer server.Close()
			dialer := websocket.Dialer{Subprotocols: []string{terminalSubprotocol}, HandshakeTimeout: time.Second}
			connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/terminal", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			_ = connection.SetReadDeadline(time.Now().Add(4 * time.Second))
			var event terminalEvent
			if err := connection.ReadJSON(&event); err != nil {
				t.Fatal(err)
			}
			wantCode := websocket.CloseNormalClosure
			if cause == "open-error" {
				if event.Type != "error" {
					t.Fatalf("打开失败没有返回安全错误事件: %+v", event)
				}
				wantCode = websocket.CloseInternalServerErr
			} else {
				if event.Type != "ready" {
					t.Fatalf("后端可用时没有先返回 ready: %+v", event)
				}
				switch cause {
				case "cancel", "cancel-lease":
					(<-cancelReady)()
				case "backend-exit":
					_ = session.outputWriter.Close()
				case "backend-error":
					_ = session.outputWriter.CloseWithError(errors.New("public fixture failed"))
					wantCode = websocket.CloseInternalServerErr
				}
				event = terminalEvent{}
				if err := connection.ReadJSON(&event); err != nil || event.Type != "exit" {
					t.Fatalf("服务端结束时没有保留 exit 事件: event=%+v err=%v", event, err)
				}
				if cause == "cancel-lease" && event.Message != "终端会话已结束" {
					t.Fatalf("取消后的后端关闭被误判为进程异常: %+v", event)
				}
			}
			_, _, err = connection.ReadMessage()
			if !websocket.IsCloseError(err, wantCode) {
				t.Fatalf("服务端结束没有发送准确关闭帧: want=%d err=%v", wantCode, err)
			}
			select {
			case <-handlerDone:
			case <-time.After(time.Second):
				t.Fatal("关闭帧发送后处理器未退出")
			}
		})
	}
}

func TestTerminalEventSendBoundsFullQueue(t *testing.T) {
	outbound := make(chan terminalOutbound, 1)
	outbound <- terminalOutbound{messageType: websocket.BinaryMessage, payload: []byte("public fixture")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sendTerminalEvent(ctx, outbound, terminalEvent{Type: "exit"}) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("满队列没有使用发送预算: %v", err)
		}
	case <-time.After(2800 * time.Millisecond):
		cancel()
		<-done
		t.Fatal("终端事件在入队阶段无限等待")
	}
}

func TestTerminalEventSendSharesEnqueueAndAckBudget(t *testing.T) {
	outbound := make(chan terminalOutbound)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sendTerminalEvent(ctx, outbound, terminalEvent{Type: "ready"}) }()
	time.Sleep(1100 * time.Millisecond)
	message := <-outbound
	if message.messageType != websocket.TextMessage || !strings.Contains(string(message.payload), `"type":"ready"`) {
		t.Fatalf("发送预算改变了 ready 协议: %+v", message)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("等待发送确认没有使用剩余预算: %v", err)
		}
	case <-time.After(1500 * time.Millisecond):
		message.ack <- nil
		<-done
		t.Fatal("入队后重新计时，超出了整次发送预算")
	}
}

func TestTerminalEventSendRespectsCancellationWhileAwaitingAck(t *testing.T) {
	outbound := make(chan terminalOutbound)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sendTerminalEvent(ctx, outbound, terminalEvent{Type: "ready"}) }()
	message := <-outbound
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("发送取消没有原样返回上下文错误: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		message.ack <- nil
		<-done
		t.Fatal("发送确认阶段忽略了调用方取消")
	}
}

func TestTerminalCanceledSessionDropsQueuedOutputButSendsExit(t *testing.T) {
	sessionContext, cancelSession := context.WithCancel(context.Background())
	cancelSession()
	if !errors.Is(sessionContext.Err(), context.Canceled) {
		t.Fatal("测试会话没有处于已撤销状态")
	}
	writerContext, stopWriter := context.WithCancel(context.Background())
	defer stopWriter()
	outbound := make(chan terminalOutbound, 2)
	outbound <- terminalOutbound{messageType: websocket.BinaryMessage, payload: []byte("queued public fixture output")}
	ack := make(chan error, 1)
	outbound <- terminalOutbound{messageType: websocket.TextMessage, payload: []byte(`{"type":"exit","message":"终端会话已结束"}`), ack: ack}
	writerDone, handlerDone := make(chan error, 1), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		upgrader := websocket.Upgrader{Subprotocols: []string{terminalSubprotocol}}
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		go writeTerminalClient(writerContext, sessionContext, connection, outbound, writerDone)
		select {
		case <-ack:
		case <-time.After(time.Second):
		}
		stopWriter()
		<-writerDone
		_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	}))
	defer server.Close()
	dialer := websocket.Dialer{Subprotocols: []string{terminalSubprotocol}, HandshakeTimeout: time.Second}
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	messageType, payload, err := connection.ReadMessage()
	if err != nil || messageType != websocket.TextMessage || !strings.Contains(string(payload), `"type":"exit"`) {
		t.Fatalf("会话撤销后仍发出了排队终端输出，或未发送安全退出事件: type=%d err=%v", messageType, err)
	}
	if _, _, err := connection.ReadMessage(); !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
		t.Fatalf("退出事件后仍有输出或没有正常关闭: %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("会话撤销后写协程没有退出")
	}
}
