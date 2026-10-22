package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

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
	c.JSON(http.StatusOK, h.service.Current(c.Request.Context()))
}

func (h systemMetricsHandler) history(c *gin.Context) {
	window, err := strconv.Atoi(c.DefaultQuery("window", "900"))
	after, afterErr := strconv.ParseInt(c.DefaultQuery("after", "0"), 10, 64)
	if err != nil || afterErr != nil || (window != 900 && window != 3600 && window != 21600 && window != 86400 && window != 259200) {
		h.logger.Warn("监控历史查询参数无效", "operation", "system_metrics_history", "window", c.Query("window"), "after", c.Query("after"), "err", errors.Join(err, afterErr))
		writeError(c, http.StatusBadRequest, "invalid_range", systemmetrics.ErrInvalidHistoryRange.Error())
		return
	}
	if h.service == nil {
		h.logger.Error("监控历史服务未初始化", "operation", "system_metrics_history")
		writeError(c, http.StatusServiceUnavailable, "metrics_history_unavailable", systemmetrics.ErrHistoryUnavailable.Error())
		return
	}
	history, err := h.service.History(c.Request.Context(), time.Duration(window)*time.Second, after)
	if err != nil {
		h.logger.Error("查询监控历史失败", "operation", "system_metrics_history", "window", window, "after", after, "err", err)
		if errors.Is(err, systemmetrics.ErrInvalidHistoryRange) {
			writeError(c, http.StatusBadRequest, "invalid_range", systemmetrics.ErrInvalidHistoryRange.Error())
			return
		}
		writeError(c, http.StatusServiceUnavailable, "metrics_history_unavailable", systemmetrics.ErrHistoryUnavailable.Error())
		return
	}
	c.JSON(http.StatusOK, history)
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
