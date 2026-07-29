package httpapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/moby/moby/api/pkg/stdcopy"

	"zrt/internal/dockerengine"
)

func TestContainerLogsStreamDemultiplexesDockerOutput(t *testing.T) {
	var stream bytes.Buffer
	writeDockerLogFrame(t, &stream, stdcopy.Stdout, "service ready\n")
	writeDockerLogFrame(t, &stream, stdcopy.Stderr, "request failed\n")
	service := &fakeContainerLogService{stream: stream.Bytes()}
	handler := containerLogHandler{
		docker: service,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/docker/endpoints/:id/containers/:container_id/logs/ws", handler.stream)
	server := httptest.NewServer(router)
	defer server.Close()

	dialer := websocket.Dialer{Subprotocols: []string{containerLogSubprotocol}}
	connection, _, err := dialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/docker/endpoints/nas/containers/app/logs/ws?tail=1000&follow=false&timestamps=false",
		nil,
	)
	if err != nil {
		t.Fatalf("连接容器日志失败: %v", err)
	}
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))

	var output strings.Builder
	ready := false
	complete := false
	for !complete {
		messageType, payload, err := connection.ReadMessage()
		if err != nil {
			t.Fatalf("读取容器日志失败: %v", err)
		}
		switch messageType {
		case websocket.BinaryMessage:
			output.Write(payload)
		case websocket.TextMessage:
			ready = ready || strings.Contains(string(payload), `"type":"ready"`)
			complete = strings.Contains(string(payload), `"type":"complete"`)
		}
	}

	if !ready {
		t.Fatal("未收到容器日志 ready 事件")
	}
	if got := output.String(); !strings.Contains(got, "service ready\n") || !strings.Contains(got, "request failed\n") {
		t.Fatalf("容器日志解复用错误: %q", got)
	}
	if service.endpointID != "nas" || service.containerID != "app" {
		t.Fatalf("容器日志目标错误: endpoint=%q container=%q", service.endpointID, service.containerID)
	}
	if service.options.Tail != 1000 || service.options.Follow || service.options.Timestamps {
		t.Fatalf("容器日志选项错误: %+v", service.options)
	}
}

func TestContainerLogsRejectsInvalidOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := containerLogHandler{
		docker: &fakeContainerLogService{},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	router := gin.New()
	router.GET("/docker/endpoints/:id/containers/:container_id/logs/ws", handler.stream)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/docker/endpoints/nas/containers/app/logs/ws?tail=5001", nil)
	request.Header.Set("Sec-WebSocket-Protocol", containerLogSubprotocol)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("无效日志参数状态码错误: %d", recorder.Code)
	}
}

type fakeContainerLogService struct {
	stream      []byte
	endpointID  string
	containerID string
	options     dockerengine.ContainerLogOptions
}

func (service *fakeContainerLogService) ContainerLogs(
	_ context.Context,
	endpointID string,
	containerID string,
	options dockerengine.ContainerLogOptions,
) (*dockerengine.ContainerLogStream, error) {
	service.endpointID = endpointID
	service.containerID = containerID
	service.options = options
	return &dockerengine.ContainerLogStream{
		ReadCloser: io.NopCloser(bytes.NewReader(service.stream)),
	}, nil
}

func writeDockerLogFrame(t *testing.T, target io.Writer, stream stdcopy.StdType, value string) {
	t.Helper()
	header := make([]byte, 8)
	header[0] = byte(stream)
	binary.BigEndian.PutUint32(header[4:], uint32(len(value)))
	if _, err := target.Write(append(header, value...)); err != nil {
		t.Fatalf("写入 Docker 日志帧失败: %v", err)
	}
}
