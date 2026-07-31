package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	terminalservice "edo/internal/terminal"
)

func TestTerminalBridgeCarriesInputOutputAndResize(t *testing.T) {
	session := newFakeTerminalSession()
	handler := terminalHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	bridgeResult := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upgrader := websocket.Upgrader{Subprotocols: []string{terminalSubprotocol}, CheckOrigin: func(*http.Request) bool { return true }}
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			bridgeResult <- err
			return
		}
		defer connection.Close()
		bridgeResult <- handler.bridge(request.Context(), connection, session)
	}))
	defer server.Close()

	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http")
	dialer := websocket.Dialer{Subprotocols: []string{terminalSubprotocol}}
	connection, _, err := dialer.Dial(websocketURL, nil)
	if err != nil {
		t.Fatalf("连接测试终端失败: %v", err)
	}
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
	messageType, ready, err := connection.ReadMessage()
	if err != nil || messageType != websocket.TextMessage || !strings.Contains(string(ready), `"type":"ready"`) {
		t.Fatalf("终端 ready 事件错误: type=%d payload=%s err=%v", messageType, ready, err)
	}

	if err := connection.WriteMessage(websocket.BinaryMessage, []byte("whoami\n")); err != nil {
		t.Fatalf("发送终端输入失败: %v", err)
	}
	select {
	case input := <-session.inputs:
		if string(input) != "whoami\n" {
			t.Fatalf("终端输入内容错误: %q", input)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待终端输入超时")
	}

	if _, err := session.outputWriter.Write([]byte("edo-user\r\n")); err != nil {
		t.Fatalf("写入终端模拟输出失败: %v", err)
	}
	messageType, output, err := connection.ReadMessage()
	if err != nil || messageType != websocket.BinaryMessage || string(output) != "edo-user\r\n" {
		t.Fatalf("终端输出内容错误: type=%d payload=%q err=%v", messageType, output, err)
	}

	if err := connection.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","columns":160,"rows":48}`)); err != nil {
		t.Fatalf("发送终端尺寸失败: %v", err)
	}
	select {
	case size := <-session.sizes:
		if size.Columns != 160 || size.Rows != 48 {
			t.Fatalf("终端尺寸错误: %+v", size)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待终端尺寸变更超时")
	}

	_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	select {
	case err := <-bridgeResult:
		if !isExpectedTerminalClose(err) {
			t.Fatalf("终端未正常关闭: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待终端关闭超时")
	}
}

func TestTerminalSubprotocolValidation(t *testing.T) {
	request := &http.Request{Header: make(http.Header)}
	request.Header.Set("Sec-WebSocket-Protocol", terminalSubprotocol)
	if !requestedSubprotocol(request) {
		t.Fatal("正确的终端子协议被拒绝")
	}
	request.Header.Set("Sec-WebSocket-Protocol", "other")
	if requestedSubprotocol(request) {
		t.Fatal("错误的终端子协议未被拒绝")
	}
}

type fakeTerminalSession struct {
	outputReader *io.PipeReader
	outputWriter *io.PipeWriter
	inputs       chan []byte
	sizes        chan terminalservice.Size
	closeOnce    sync.Once
}

func newFakeTerminalSession() *fakeTerminalSession {
	reader, writer := io.Pipe()
	return &fakeTerminalSession{
		outputReader: reader, outputWriter: writer,
		inputs: make(chan []byte, 2), sizes: make(chan terminalservice.Size, 2),
	}
}

func (s *fakeTerminalSession) Read(buffer []byte) (int, error) {
	return s.outputReader.Read(buffer)
}

func (s *fakeTerminalSession) Write(buffer []byte) (int, error) {
	s.inputs <- append([]byte(nil), buffer...)
	return len(buffer), nil
}

func (s *fakeTerminalSession) Resize(_ context.Context, size terminalservice.Size) error {
	s.sizes <- size
	return nil
}

func (s *fakeTerminalSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = errorsJoin(s.outputReader.Close(), s.outputWriter.Close())
	})
	return err
}

func errorsJoin(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
