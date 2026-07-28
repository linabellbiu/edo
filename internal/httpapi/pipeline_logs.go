package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"zrt/internal/model"
	"zrt/internal/pipeline"
)

const pipelineLogSubprotocol = "zrt-pipeline-logs-v1"

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

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()
	for {
		logs, status, readErr := h.service.ListRunLogs(c.Request.Context(), c.Param("id"), afterID, 200)
		if readErr != nil {
			h.logger.Error("推送流水线执行日志失败", "operation", "pipeline_log_stream", "request_id", requestIDFrom(c), "pipeline_run_id", c.Param("id"), "err", readErr)
			_ = writePipelineLogEvent(connection, pipelineLogEvent{Type: "error", Message: "读取流水线日志失败，请稍后重试"})
			return
		}
		if len(logs) > 0 {
			afterID = lastPipelineLogID(logs, afterID)
			if err := writePipelineLogEvent(connection, pipelineLogEvent{Type: "logs", Logs: logs, Status: status}); err != nil {
				return
			}
		}
		if status != model.PipelineRunRunning && len(logs) < 200 {
			_ = writePipelineLogEvent(connection, pipelineLogEvent{Type: "complete", Status: status})
			return
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
		case <-pingTicker.C:
			if err := connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		}
	}
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
