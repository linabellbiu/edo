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

func (h pipelineHandler) detectComposeFile(c *gin.Context) {
	var request struct {
		Ref string `json:"ref" binding:"max=1024"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.WarnContext(c.Request.Context(), "Compose 文件检测请求无效", "operation", "compose_file_detect_bind", "application_id", c.Param("id"), "err", err)
		h.writeError(c, "compose_file_detect", pipeline.ErrComposeSource)
		return
	}
	result, err := h.service.DetectRepositoryComposeFile(c.Request.Context(), c.Param("id"), request.Ref)
	if err != nil {
		h.writeError(c, "compose_file_detect", err)
		return
	}
	c.JSON(http.StatusOK, result)
}
