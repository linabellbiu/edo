package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"edo/internal/model"
	"edo/internal/pipeline"
)

const pipelineLogSubprotocol = "edo-pipeline-logs-v1"

type pipelineLogEvent struct {
	Type    string                  `json:"type"`
	Logs    []model.PipelineRunLog  `json:"logs,omitempty"`
	Status  model.PipelineRunStatus `json:"status,omitempty"`
	Message string                  `json:"message,omitempty"`
}

func (h pipelineHandler) listRunLogs(c *gin.Context) {
	afterID, err := pipelineLogAfterID(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_pipeline_log_cursor", "流水线日志游标无效")
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "200"))
	logs, status, err := h.service.ListRunLogs(c.Request.Context(), c.Param("id"), afterID, limit)
	if err != nil {
		h.logger.Error("读取流水线执行日志失败", "operation", "pipeline_log_list", "request_id", requestIDFrom(c), "pipeline_run_id", c.Param("id"), "err", err)
		if errors.Is(err, pipeline.ErrPipelineRunNotFound) {
			writeError(c, http.StatusNotFound, "pipeline_run_not_found", pipeline.ErrPipelineRunNotFound.Error())
			return
		}
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"logs": logs, "status": status, "next_after_id": lastPipelineLogID(logs, afterID)})
}

func (h pipelineHandler) listExecutionLogs(c *gin.Context) {
	beforeID, err := strconv.ParseUint(strings.TrimSpace(c.DefaultQuery("before_id", "0")), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_log_cursor", "日志游标无效")
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if limit < 1 || limit > 500 {
		limit = 100
	}
	logs, err := h.service.ListExecutionLogs(c.Request.Context(), pipeline.ExecutionLogFilter{
		BeforeID: beforeID,
		Limit:    limit,
		Level:    c.Query("level"),
		Query:    c.Query("query"),
	})
	if err != nil {
		h.logger.Error("读取流水线日志中心失败", "operation", "execution_log_list", "request_id", requestIDFrom(c), "err", err)
		if errors.Is(err, pipeline.ErrInvalidExecutionLogFilter) {
			writeError(c, http.StatusBadRequest, "invalid_log_filter", err.Error())
			return
		}
		writeInternalError(c)
		return
	}
	nextBeforeID := uint64(0)
	if len(logs) > 0 {
		nextBeforeID = logs[len(logs)-1].ID
	}
	c.JSON(http.StatusOK, gin.H{"items": logs, "next_before_id": nextBeforeID, "has_more": len(logs) == limit})
}

func (h pipelineHandler) streamRunLogs(c *gin.Context) {
	afterID, err := pipelineLogAfterID(c)
	if err != nil || !pipelineLogProtocolRequested(c.Request) {
		h.logger.Warn("流水线日志握手参数无效", "operation", "pipeline_log_handshake", "request_id", requestIDFrom(c), "pipeline_run_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_pipeline_log_request", "流水线日志连接参数无效")
		return
	}
	// 在握手和订阅前检查运行归属，不能先建立跨部门日志通道。
	if _, _, err := h.service.ListRunLogs(c.Request.Context(), c.Param("id"), afterID, 1); err != nil {
		h.logger.Warn("检查流水线日志访问失败", "operation", "pipeline_log_access", "pipeline_run_id", c.Param("id"), "err", err)
		if errors.Is(err, pipeline.ErrPipelineRunNotFound) {
			writeError(c, http.StatusNotFound, "pipeline_run_not_found", pipeline.ErrPipelineRunNotFound.Error())
		} else {
			writeInternalError(c)
		}
		return
	}
	var updates <-chan any
	if h.realtime != nil {
		subscription, err := h.realtime.SubscribeLogs(c.Request.Context(), c.Param("id"))
		if err != nil {
			h.logger.Warn("订阅流水线日志失败，使用低频补同步", "operation", "pipeline_log_subscribe", "pipeline_run_id", c.Param("id"), "err", err)
		} else {
			defer subscription.Close()
			updates = subscription.ChannelWithSubscriptions(redis.WithChannelSize(64))
		}
	}
	upgrader := websocket.Upgrader{
		HandshakeTimeout:  10 * time.Second,
		Subprotocols:      []string{pipelineLogSubprotocol},
		EnableCompression: true,
		CheckOrigin:       func(*http.Request) bool { return true },
	}
	connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.logger.Warn("升级流水线日志 WebSocket 失败", "operation", "pipeline_log_upgrade", "request_id", requestIDFrom(c), "pipeline_run_id", c.Param("id"), "err", err)
		return
	}
	defer connection.Close()
	logger := h.logger.With("operation", "pipeline_log_stream", "request_id", requestIDFrom(c), "pipeline_run_id", c.Param("id"))
	done := readSocketControl(connection, logger)
	defer closeSocketNormally(connection, done)
	write := func(event pipelineLogEvent) error {
		err := writePipelineLogEvent(connection, event)
		if err != nil {
			logger.Warn("发送流水线日志事件失败", "event_type", event.Type, "err", err)
		}
		return err
	}
	authorized := func() bool { return true }
	if h.streamAuth != nil {
		authorized = h.streamAuth(c)
	}

	// 日志落库后唤醒；低频补读覆盖 Pub/Sub 暂时不可用和遗漏通知。
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()
	var previousStatus model.PipelineRunStatus
	for {
		logs, status, readErr := h.service.ListRunLogs(c.Request.Context(), c.Param("id"), afterID, 200)
		if readErr != nil {
			h.logger.Error("推送流水线执行日志失败", "operation", "pipeline_log_stream", "request_id", requestIDFrom(c), "pipeline_run_id", c.Param("id"), "err", readErr)
			_ = write(pipelineLogEvent{Type: "error", Message: "读取流水线日志失败，请稍后重试"})
			return
		}
		if len(logs) > 0 {
			afterID = lastPipelineLogID(logs, afterID)
			if err := write(pipelineLogEvent{Type: "logs", Logs: logs, Status: status}); err != nil {
				return
			}
		} else if previousStatus != status {
			if err := write(pipelineLogEvent{Type: "status", Status: status}); err != nil {
				return
			}
		}
		previousStatus = status
		if pipelineLogTerminal(status) && len(logs) < 200 {
			_ = write(pipelineLogEvent{Type: "complete", Status: status})
			return
		}
		if len(logs) == 200 {
			continue
		}
		select {
		case <-done:
			return
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
			if !authorized() {
				closeSocketPolicy(connection)
				return
			}
		case _, open := <-updates:
			if !open {
				updates = nil
			}
		case <-pingTicker.C:
			if !authorized() {
				closeSocketPolicy(connection)
				return
			}
			if err := write(pipelineLogEvent{Type: "heartbeat", Status: status}); err != nil {
				return
			}
			if err := connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				logger.Warn("发送流水线日志心跳失败", "err", err)
				return
			}
		}
	}
}

func pipelineLogTerminal(status model.PipelineRunStatus) bool {
	return status == model.PipelineRunSucceeded || status == model.PipelineRunFailed || status == model.PipelineRunSkipped || status == model.PipelineRunCanceled
}

func pipelineLogAfterID(c *gin.Context) (uint64, error) {
	value := strings.TrimSpace(c.DefaultQuery("after_id", "0"))
	return strconv.ParseUint(value, 10, 64)
}

func pipelineLogProtocolRequested(request *http.Request) bool {
	for _, protocol := range websocket.Subprotocols(request) {
		if protocol == pipelineLogSubprotocol {
			return true
		}
	}
	return false
}

func lastPipelineLogID(logs []model.PipelineRunLog, fallback uint64) uint64 {
	if len(logs) == 0 {
		return fallback
	}
	return logs[len(logs)-1].ID
}

func writePipelineLogEvent(connection *websocket.Conn, event pipelineLogEvent) error {
	if err := connection.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return connection.WriteJSON(event)
}
