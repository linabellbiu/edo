package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"edo/internal/systemmetrics"
)

type systemMetricsHandler struct {
	service *systemmetrics.Service
	logger  *slog.Logger
}

func (h systemMetricsHandler) snapshot(c *gin.Context) {
	if h.service == nil {
		h.logger.Error("系统指标服务未初始化", "operation", "system_metrics_snapshot", "request_id", requestIDFrom(c))
		writeError(c, http.StatusServiceUnavailable, "metrics_unavailable", "系统监控暂不可用")
		return
	}
	c.JSON(http.StatusOK, h.service.Snapshot(c.Request.Context()))
}

func (h systemMetricsHandler) purgeDeadLetters(c *gin.Context) {
	if h.service == nil {
		h.logger.Error("系统指标服务未初始化", "operation", "dead_letter_purge", "request_id", requestIDFrom(c))
		writeError(c, http.StatusServiceUnavailable, "dead_letter_queue_unavailable", "死信队列暂不可用")
		return
	}
	purged, err := h.service.PurgeDeadLetters(c.Request.Context())
	if err != nil {
		h.logger.Error("清空死信队列失败", "operation", "dead_letter_purge", "request_id", requestIDFrom(c), "err", err)
		if errors.Is(err, systemmetrics.ErrDeadLetterQueueUnavailable) {
			writeError(c, http.StatusServiceUnavailable, "dead_letter_queue_unavailable", "死信队列暂不可用")
			return
		}
		writeError(c, http.StatusServiceUnavailable, "dead_letter_purge_failed", "清空死信队列失败，请稍后重试")
		return
	}
	setAuditResourceID(c, "dead-letter")
	c.JSON(http.StatusOK, gin.H{"purged_messages": purged})
}
