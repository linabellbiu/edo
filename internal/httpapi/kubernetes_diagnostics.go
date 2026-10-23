package httpapi

import (
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h clusterHandler) resourceDiagnostics(c *gin.Context) {
	result, err := h.kube.ResourceDiagnostics(c.Request.Context(), c.Param("id"), c.Param("resource"), c.Query("namespace"), c.Param("name"), c.Query("uid"))
	if err != nil {
		h.resourceError(c, "kubernetes_diagnostics", err)
		return
	}
	for part, err := range result.Errors {
		h.logger.Warn("读取资源诊断部分失败", "operation", "kubernetes_diagnostics", "cluster_id", c.Param("id"), "resource", c.Param("resource"), "namespace", c.Query("namespace"), "name", c.Param("name"), "part", part, "err", err)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}
