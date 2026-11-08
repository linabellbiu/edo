package httpapi

import (
	"net/http"

	"edo/internal/shellcheck"
	"github.com/gin-gonic/gin"
)

func (h pipelineHandler) checkDeploymentScript(c *gin.Context) {
	var request struct {
		Script string `json:"script" binding:"max=262144"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("部署脚本检查请求无效", "operation", "deployment_script_check", "err", "请求格式或大小无效")
		writeError(c, http.StatusBadRequest, "invalid_deployment_script", "脚本检查请求无效，正文不能超过 256 KiB")
		return
	}
	issues := shellcheck.Check(request.Script)
	if len(issues) > 0 {
		h.logger.Info("部署脚本语法检查未通过", "operation", "deployment_script_check", "line", issues[0].Line, "column", issues[0].Column)
	}
	c.JSON(http.StatusOK, gin.H{"valid": len(issues) == 0, "issues": issues})
}

func (h pipelineHandler) deploymentRegistryOptions(c *gin.Context) {
	options, err := h.service.DeploymentRegistryOptions(c.Request.Context(), c.Param("id"), c.Query("image"))
	if err != nil {
		h.writeError(c, "deployment_registry_options", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"options": options})
}
