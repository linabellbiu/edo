package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/moby/moby/api/pkg/stdcopy"

	"zrt/internal/audit"
	"zrt/internal/dockerengine"
	"zrt/internal/model"
)

const containerLogSubprotocol = "zrt-container-logs-v1"

type dockerContainerLogService interface {
	ContainerLogs(context.Context, string, string, dockerengine.ContainerLogOptions) (*dockerengine.ContainerLogStream, error)
}

type containerLogHandler struct {
	docker dockerContainerLogService
	audits *audit.Service
	logger *slog.Logger
}

type containerLogEvent struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

func (h containerLogHandler) stream(c *gin.Context) {
	sessionID := uuid.NewString()
	options, err := parseContainerLogOptions(c)
	if err != nil || !containerLogProtocolRequested(c.Request) {
		h.logger.Warn("容器日志请求参数无效", "operation", "docker_container_logs_validate", "request_id", requestIDFrom(c), "endpoint_id", c.Param("id"), "container_id", c.Param("container_id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_container_logs", dockerengine.ErrInvalidContainerLogs.Error())
		return
	}
	streamContext, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	logs, err := h.docker.ContainerLogs(streamContext, c.Param("id"), c.Param("container_id"), options)
	if err != nil {
		h.logger.Error("打开 Docker 容器日志失败", "operation", "docker_container_logs_open", "request_id", requestIDFrom(c), "endpoint_id", c.Param("id"), "container_id", c.Param("container_id"), "err", err)
		h.recordAudit(c, "container.logs.open", sessionID, options, model.AuditFailed, 0)
		writeError(c, http.StatusBadGateway, "container_logs_unavailable", "无法读取容器日志，请检查容器和 Docker 连接")
		return
	}
	defer logs.Close()

	upgrader := websocket.Upgrader{
		HandshakeTimeout:  10 * time.Second,
		Subprotocols:      []string{containerLogSubprotocol},
		EnableCompression: false,
		CheckOrigin:       func(*http.Request) bool { return true },
	}
	connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.logger.Warn("容器日志 WebSocket 握手失败", "operation", "docker_container_logs_upgrade", "request_id", requestIDFrom(c), "endpoint_id", c.Param("id"), "container_id", c.Param("container_id"), "err", err)
		return
	}
	defer connection.Close()
	writer := &containerLogWriter{connection: connection}
	if err := writer.event(containerLogEvent{Type: "ready"}); err != nil {
		return
	}
	h.recordAudit(c, "container.logs.open", sessionID, options, model.AuditSucceeded, 0)
	started := time.Now()
	result := model.AuditSucceeded
	defer func() {
		h.recordAudit(c, "container.logs.close", sessionID, options, result, time.Since(started))
	}()

	clientDone := make(chan error, 1)
	streamDone := make(chan error, 1)
	go readContainerLogClient(connection, clientDone)
	go func() {
		if logs.TTY {
			_, err = io.Copy(writer, logs)
		} else {
			_, err = stdcopy.StdCopy(writer, writer, logs)
		}
		streamDone <- err
	}()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-streamContext.Done():
			return
		case <-clientDone:
			cancel()
			return
		case err := <-streamDone:
			if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
				result = model.AuditFailed
				h.logger.Warn("读取 Docker 容器日志中断", "operation", "docker_container_logs_stream", "request_id", requestIDFrom(c), "endpoint_id", c.Param("id"), "container_id", c.Param("container_id"), "err", err)
				_ = writer.event(containerLogEvent{Type: "error", Message: "容器日志连接已中断"})
				return
			}
			_ = writer.event(containerLogEvent{Type: "complete", Message: "日志流已结束"})
			return
		case <-ping.C:
			if err := writer.ping(); err != nil {
				cancel()
				return
			}
		}
	}
}

func (h containerLogHandler) recordAudit(
	c *gin.Context,
	action, sessionID string,
	options dockerengine.ContainerLogOptions,
	result model.AuditResult,
	duration time.Duration,
) {
	if h.audits == nil {
		return
	}
	actor, _ := currentUser(c)
	if actor == nil {
		h.logger.Error("容器日志审计缺少当前用户", "operation", "docker_container_logs_audit_actor", "request_id", requestIDFrom(c), "session_id", sessionID, "action", action)
		return
	}
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
	defer cancel()
	if err := h.audits.Record(auditContext, audit.RecordInput{
		ActorUserID: actor.ID, Action: action, ResourceType: "docker_container", ResourceID: c.Param("container_id"),
		Result: result, RequestID: requestIDFrom(c), ClientIP: c.ClientIP(),
		UserAgent: truncateText(c.Request.UserAgent(), 512),
		Metadata: map[string]any{
			"endpoint_id": c.Param("id"), "session_id": sessionID, "tail": options.Tail,
			"follow": options.Follow, "timestamps": options.Timestamps, "duration_ms": duration.Milliseconds(),
		},
	}); err != nil {
		h.logger.Error("记录容器日志审计失败", "operation", "docker_container_logs_audit", "request_id", requestIDFrom(c), "session_id", sessionID, "action", action, "err", err)
	}
}

func parseContainerLogOptions(c *gin.Context) (dockerengine.ContainerLogOptions, error) {
	tail, err := strconv.Atoi(c.DefaultQuery("tail", "500"))
	if err != nil || tail < 1 || tail > 5000 {
		return dockerengine.ContainerLogOptions{}, dockerengine.ErrInvalidContainerLogs
	}
	follow, err := strconv.ParseBool(c.DefaultQuery("follow", "true"))
	if err != nil {
		return dockerengine.ContainerLogOptions{}, dockerengine.ErrInvalidContainerLogs
	}
	timestamps, err := strconv.ParseBool(c.DefaultQuery("timestamps", "true"))
	if err != nil {
		return dockerengine.ContainerLogOptions{}, dockerengine.ErrInvalidContainerLogs
	}
	return dockerengine.ContainerLogOptions{Tail: tail, Follow: follow, Timestamps: timestamps}, nil
}

func containerLogProtocolRequested(request *http.Request) bool {
	for _, protocol := range websocket.Subprotocols(request) {
		if protocol == containerLogSubprotocol {
			return true
		}
	}
	return false
}

func readContainerLogClient(connection *websocket.Conn, done chan<- error) {
	connection.SetReadLimit(1024)
	_ = connection.SetReadDeadline(time.Now().Add(75 * time.Second))
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(75 * time.Second))
	})
	for {
		_, _, err := connection.ReadMessage()
		if err != nil {
			done <- err
			return
		}
	}
}

type containerLogWriter struct {
	connection *websocket.Conn
	mu         sync.Mutex
}

func (writer *containerLogWriter) Write(payload []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	for offset := 0; offset < len(payload); {
		end := min(offset+32*1024, len(payload))
		if err := writer.write(websocket.BinaryMessage, payload[offset:end]); err != nil {
			return offset, err
		}
		offset = end
	}
	return len(payload), nil
}

func (writer *containerLogWriter) event(event containerLogEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.write(websocket.TextMessage, payload)
}

func (writer *containerLogWriter) ping() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
}

func (writer *containerLogWriter) write(messageType int, payload []byte) error {
	if err := writer.connection.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return writer.connection.WriteMessage(messageType, payload)
}
