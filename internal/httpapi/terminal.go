package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"zrt/internal/audit"
	"zrt/internal/model"
	terminalservice "zrt/internal/terminal"
)

const terminalSubprotocol = "zrt-terminal-v1"

type terminalHandler struct {
	service *terminalservice.Service
	audits  *audit.Service
	logger  *slog.Logger
}

type terminalControl struct {
	Type    string `json:"type"`
	Columns uint16 `json:"columns"`
	Rows    uint16 `json:"rows"`
}

type terminalEvent struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

type terminalOutbound struct {
	messageType int
	payload     []byte
	ack         chan error
}

func (h terminalHandler) docker(c *gin.Context) {
	h.open(c, "docker", c.Param("endpoint_id")+":"+c.Param("container_id"), map[string]any{
		"endpoint_id": c.Param("endpoint_id"), "container_id": c.Param("container_id"),
	}, func(ctx context.Context, shell string, size terminalservice.Size) (terminalservice.Session, error) {
		return h.service.OpenDocker(ctx, c.Param("endpoint_id"), c.Param("container_id"), shell, size)
	})
}

func (h terminalHandler) kubernetes(c *gin.Context) {
	resourceID := c.Param("cluster_id") + ":" + c.Param("namespace") + ":" + c.Param("pod") + ":" + c.Param("container")
	h.open(c, "kubernetes", resourceID, map[string]any{
		"cluster_id": c.Param("cluster_id"), "namespace": c.Param("namespace"),
		"pod": c.Param("pod"), "container": c.Param("container"),
	}, func(ctx context.Context, shell string, size terminalservice.Size) (terminalservice.Session, error) {
		return h.service.OpenKubernetes(
			ctx, c.Param("cluster_id"), c.Param("namespace"), c.Param("pod"), c.Param("container"), shell, size,
		)
	})
}

func (h terminalHandler) open(
	c *gin.Context,
	targetType, resourceID string,
	metadata map[string]any,
	opener func(context.Context, string, terminalservice.Size) (terminalservice.Session, error),
) {
	metadata["target_type"] = targetType
	shell := c.DefaultQuery("shell", "sh")
	size, err := parseTerminalSize(c)
	if err != nil || !requestedSubprotocol(c.Request) {
		h.logger.Warn("终端握手参数无效", "operation", "terminal_handshake_validate", "request_id", requestIDFrom(c), "target_type", targetType, "err", err)
		writeError(c, http.StatusBadRequest, "invalid_terminal_request", terminalservice.ErrInvalidRequest.Error())
		return
	}
	if !terminalOriginAllowed(c.Request) {
		h.logger.Warn("拒绝来源不匹配的终端连接", "operation", "terminal_origin", "request_id", requestIDFrom(c), "host", c.Request.Host)
		writeError(c, http.StatusForbidden, "origin_mismatch", "请求来源校验失败")
		return
	}

	upgrader := websocket.Upgrader{
		HandshakeTimeout:  10 * time.Second,
		Subprotocols:      []string{terminalSubprotocol},
		EnableCompression: false,
		CheckOrigin:       terminalOriginAllowed,
	}
	connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.logger.Warn("WebSocket 终端握手失败", "operation", "terminal_upgrade", "request_id", requestIDFrom(c), "target_type", targetType, "err", err)
		return
	}
	defer connection.Close()

	sessionID := uuid.NewString()
	sessionContext, cancel := context.WithTimeout(c.Request.Context(), h.service.MaxDuration())
	defer cancel()
	session, err := opener(sessionContext, shell, size)
	if err != nil {
		h.logger.Error("打开容器终端失败", "operation", "terminal_open", "request_id", requestIDFrom(c), "session_id", sessionID, "target_type", targetType, "resource_id", resourceID, "err", err)
		h.writeTerminalEvent(connection, terminalEvent{Type: "error", Message: "终端连接失败，请检查目标容器状态"})
		h.recordTerminalAudit(c, "terminal.open", resourceID, sessionID, model.AuditFailed, metadata, 0)
		return
	}
	sessionClosed := false
	defer func() {
		if sessionClosed {
			return
		}
		if closeErr := session.Close(); closeErr != nil {
			h.logger.Warn("关闭容器终端资源失败", "operation", "terminal_close_resource", "request_id", requestIDFrom(c), "session_id", sessionID, "err", closeErr)
		}
	}()

	metadata["shell"] = shell
	metadata["columns"] = size.Columns
	metadata["rows"] = size.Rows
	h.recordTerminalAudit(c, "terminal.open", resourceID, sessionID, model.AuditSucceeded, metadata, 0)
	started := time.Now()
	bridgeErr := h.bridge(sessionContext, connection, session)
	duration := time.Since(started)
	cancel()
	if closeErr := session.Close(); closeErr != nil {
		h.logger.Warn("结束容器终端时释放资源失败", "operation", "terminal_close_resource", "request_id", requestIDFrom(c), "session_id", sessionID, "err", closeErr)
	}
	sessionClosed = true
	result := model.AuditSucceeded
	if bridgeErr != nil && !isExpectedTerminalClose(bridgeErr) {
		result = model.AuditFailed
		h.logger.Warn("容器终端会话异常结束", "operation", "terminal_bridge", "request_id", requestIDFrom(c), "session_id", sessionID, "target_type", targetType, "resource_id", resourceID, "err", bridgeErr)
	}
	h.recordTerminalAudit(c, "terminal.close", resourceID, sessionID, result, metadata, duration)
}

func (h terminalHandler) bridge(ctx context.Context, connection *websocket.Conn, session terminalservice.Session) error {
	connection.SetReadLimit(64 * 1024)
	_ = connection.SetReadDeadline(time.Now().Add(75 * time.Second))
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(75 * time.Second))
	})

	outbound := make(chan terminalOutbound, 64)
	clientDone := make(chan error, 1)
	backendDone := make(chan error, 1)
	writerDone := make(chan error, 1)
	go readTerminalClient(ctx, connection, session, clientDone)
	go readTerminalOutput(ctx, session, outbound, backendDone)
	go writeTerminalClient(ctx, connection, outbound, writerDone)
	if err := sendTerminalEvent(ctx, outbound, terminalEvent{Type: "ready"}); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		message := "终端会话已结束"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			message = "终端会话已达到最长持续时间"
		}
		_ = sendTerminalEvent(context.Background(), outbound, terminalEvent{Type: "exit", Message: message})
		return ctx.Err()
	case err := <-clientDone:
		return err
	case err := <-backendDone:
		message := "终端进程已退出"
		if err != nil && !errors.Is(err, io.EOF) {
			message = "终端进程异常退出"
		}
		_ = sendTerminalEvent(context.Background(), outbound, terminalEvent{Type: "exit", Message: message})
		return err
	case err := <-writerDone:
		return err
	}
}

func readTerminalClient(ctx context.Context, connection *websocket.Conn, session terminalservice.Session, done chan<- error) {
	for {
		messageType, payload, err := connection.ReadMessage()
		if err != nil {
			done <- err
			return
		}
		switch messageType {
		case websocket.BinaryMessage:
			if len(payload) == 0 {
				continue
			}
			if err := writeTerminalInput(session, payload); err != nil {
				done <- fmt.Errorf("写入终端输入失败: %w", err)
				return
			}
		case websocket.TextMessage:
			var control terminalControl
			if err := json.Unmarshal(payload, &control); err != nil || control.Type != "resize" {
				done <- terminalservice.ErrInvalidRequest
				return
			}
			if err := session.Resize(ctx, terminalservice.Size{Columns: control.Columns, Rows: control.Rows}); err != nil {
				done <- err
				return
			}
		default:
			done <- terminalservice.ErrInvalidRequest
			return
		}
	}
}

func readTerminalOutput(ctx context.Context, session terminalservice.Session, outbound chan<- terminalOutbound, done chan<- error) {
	buffer := make([]byte, 32*1024)
	for {
		count, err := session.Read(buffer)
		if count > 0 {
			payload := append([]byte(nil), buffer[:count]...)
			select {
			case outbound <- terminalOutbound{messageType: websocket.BinaryMessage, payload: payload}:
			case <-ctx.Done():
				done <- ctx.Err()
				return
			}
		}
		if err != nil {
			done <- err
			return
		}
	}
}

func writeTerminalClient(ctx context.Context, connection *websocket.Conn, outbound <-chan terminalOutbound, done chan<- error) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- ctx.Err()
			return
		case message := <-outbound:
			_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
			err := connection.WriteMessage(message.messageType, message.payload)
			if message.ack != nil {
				message.ack <- err
			}
			if err != nil {
				done <- err
				return
			}
		case <-ticker.C:
			_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := connection.WriteMessage(websocket.PingMessage, nil); err != nil {
				done <- err
				return
			}
		}
	}
}

func sendTerminalEvent(ctx context.Context, outbound chan<- terminalOutbound, event terminalEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	ack := make(chan error, 1)
	select {
	case outbound <- terminalOutbound{messageType: websocket.TextMessage, payload: payload, ack: ack}:
	case <-ctx.Done():
		return ctx.Err()
	}
	waitContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	select {
	case err := <-ack:
		return err
	case <-waitContext.Done():
		return waitContext.Err()
	}
}

func (h terminalHandler) writeTerminalEvent(connection *websocket.Conn, event terminalEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = connection.WriteMessage(websocket.TextMessage, payload)
}

func (h terminalHandler) recordTerminalAudit(
	c *gin.Context,
	action, resourceID, sessionID string,
	result model.AuditResult,
	metadata map[string]any,
	duration time.Duration,
) {
	actor, _ := currentUser(c)
	if actor == nil {
		h.logger.Error("终端审计缺少当前用户", "operation", "terminal_audit_actor", "request_id", requestIDFrom(c), "session_id", sessionID, "action", action)
		return
	}
	values := make(map[string]any, len(metadata)+2)
	for key, value := range metadata {
		values[key] = value
	}
	values["session_id"] = sessionID
	values["duration_ms"] = duration.Milliseconds()
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
	defer cancel()
	if err := h.audits.Record(auditContext, audit.RecordInput{
		ActorUserID: actor.ID, Action: action, ResourceType: "terminal", ResourceID: resourceID,
		Result: result, RequestID: requestIDFrom(c), ClientIP: c.ClientIP(),
		UserAgent: truncateText(c.Request.UserAgent(), 512), Metadata: values,
	}); err != nil {
		h.logger.Error("记录终端审计失败", "operation", "terminal_audit", "request_id", requestIDFrom(c), "session_id", sessionID, "action", action, "err", err)
	}
}

func parseTerminalSize(c *gin.Context) (terminalservice.Size, error) {
	columns, err := strconv.ParseUint(c.DefaultQuery("columns", "120"), 10, 16)
	if err != nil {
		return terminalservice.Size{}, terminalservice.ErrInvalidRequest
	}
	rows, err := strconv.ParseUint(c.DefaultQuery("rows", "30"), 10, 16)
	if err != nil {
		return terminalservice.Size{}, terminalservice.ErrInvalidRequest
	}
	size := terminalservice.Size{Columns: uint16(columns), Rows: uint16(rows)}
	if size.Columns < 20 || size.Columns > 500 || size.Rows < 5 || size.Rows > 200 {
		return terminalservice.Size{}, terminalservice.ErrInvalidRequest
	}
	return size, nil
}

func requestedSubprotocol(request *http.Request) bool {
	for _, protocol := range websocket.Subprotocols(request) {
		if protocol == terminalSubprotocol {
			return true
		}
	}
	return false
}

func terminalOriginAllowed(request *http.Request) bool {
	if strings.EqualFold(request.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host != "" && strings.EqualFold(parsed.Host, request.Host)
}

func isExpectedTerminalClose(err error) bool {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived)
}

func writeTerminalInput(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		count, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if count <= 0 {
			return io.ErrShortWrite
		}
		payload = payload[count:]
	}
	return nil
}
