package httpapi

import (
	"edo/internal/pipeline"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h pipelineHandler) composeServices(c *gin.Context) {
	var request pipeline.ComposeServicesInput
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("Compose 服务解析请求无效", "operation", "compose_services_bind", "application_id", c.Param("id"), "err", err)
		h.writeError(c, "compose_services", pipeline.ErrComposeSource)
		return
	}
	result, err := h.service.PreviewComposeServices(c.Request.Context(), c.Param("id"), request)
	if err != nil {
		h.writeError(c, "compose_services", err)
		return
	}
	c.JSON(http.StatusOK, result)
}
