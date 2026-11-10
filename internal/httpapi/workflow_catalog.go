package httpapi

import (
	"edo/internal/pipeline"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h pipelineHandler) listWorkflowCatalog(c *gin.Context) {
	rows, err := h.service.ListWorkflowCatalog(c.Request.Context())
	if err != nil {
		h.logger.Error("读取流水线列表失败", "operation", "workflow_catalog_list", "err", err)
		writeError(c, http.StatusInternalServerError, "workflow_list_failed", pipeline.ErrWorkflowCatalogUnavailable.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"workflows": rows})
}

func (h pipelineHandler) listHostWorkflows(c *gin.Context) {
	rows, err := h.service.ListHostWorkflows(c.Request.Context())
	if err != nil {
		h.logger.ErrorContext(c.Request.Context(), "读取主机关联流水线失败", "operation", "host_workflows_list", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusInternalServerError, "host_workflows_unavailable", pipeline.ErrHostWorkflowsUnavailable.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"host_workflows": rows})
}
