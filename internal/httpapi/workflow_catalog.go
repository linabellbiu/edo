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
