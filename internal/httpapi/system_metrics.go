package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"zrt/internal/systemmetrics"
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
