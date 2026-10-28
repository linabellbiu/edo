package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"edo/internal/cicd"
)

func (h pipelineHandler) listDeliveryVariables(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	items, err := h.service.Delivery().ListVariables(c.Request.Context())
	if err != nil {
		h.writeDeliveryError(c, "delivery_variable_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"variables": items})
}

func (h pipelineHandler) saveDeliveryVariable(c *gin.Context) {
	var input cicd.VariableInput
	if err := c.ShouldBindJSON(&input); err != nil {
		h.logger.Warn("变量请求格式无效", "operation", "delivery_variable_bind", "err", err)
		writeError(c, http.StatusBadRequest, "invalid_variable", cicd.ErrInvalidVariable.Error())
		return
	}
	actor, _ := currentUser(c)
	item, err := h.service.Delivery().SaveVariable(c.Request.Context(), actor.ID, c.Param("id"), input)
	if err != nil {
		h.writeDeliveryError(c, "delivery_variable_save", err)
		return
	}
	setAuditResourceID(c, item.ID)
	c.JSON(http.StatusOK, gin.H{"variable": item})
}

func (h pipelineHandler) deleteDeliveryVariable(c *gin.Context) {
	version, err := strconv.ParseUint(c.Query("version"), 10, 64)
	if err != nil || version == 0 {
		h.logger.Warn("删除变量缺少版本", "operation", "delivery_variable_delete_bind", "err", err)
		writeError(c, http.StatusBadRequest, "invalid_variable", cicd.ErrInvalidVariable.Error())
		return
	}
	if err = h.service.Delivery().DeleteVariable(c.Request.Context(), c.Param("id"), version); err != nil {
		h.writeDeliveryError(c, "delivery_variable_delete", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h pipelineHandler) listPipelineNodeRuns(c *gin.Context) {
	items, err := h.service.ListNodeRuns(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, "pipeline_node_runs_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"tasks": items})
}

func (h pipelineHandler) listPipelineCheckReports(c *gin.Context) {
	items, err := h.service.ListCheckReports(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, "pipeline_reports_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"reports": items})
}

func (h pipelineHandler) writeDeliveryError(c *gin.Context, operation string, err error) {
	h.logger.Error("交付配置请求失败", "operation", operation, "err", err)
	status := http.StatusInternalServerError
	message := cicd.ErrStorage.Error()
	switch {
	case errors.Is(err, cicd.ErrExecutionAuthorization):
		status = http.StatusForbidden
		message = "没有操作当前项目交付配置的权限"
	case errors.Is(err, cicd.ErrScope), errors.Is(err, cicd.ErrInvalidVariable), errors.Is(err, cicd.ErrInvalidDatabase), errors.Is(err, cicd.ErrDatabaseConnect), errors.Is(err, cicd.ErrInvalidPolicy):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, cicd.ErrVariableConflict):
		status = http.StatusConflict
		message = err.Error()
	case errors.Is(err, cicd.ErrVariableUnavailable):
		status = http.StatusNotFound
		message = err.Error()
	}
	writeError(c, status, "delivery_configuration_failed", message)
}

func (h pipelineHandler) listApplicationDatabases(c *gin.Context) {
	items, err := h.service.Delivery().ListDatabases(c.Request.Context())
	if err != nil {
		h.writeDeliveryError(c, "application_database_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"databases": items})
}
func (h pipelineHandler) saveApplicationDatabase(c *gin.Context) {
	var input cicd.DatabaseInput
	if err := c.ShouldBindJSON(&input); err != nil {
		h.logger.Warn("数据库请求格式无效", "operation", "application_database_bind", "err", err)
		writeError(c, 400, "invalid_database", cicd.ErrInvalidDatabase.Error())
		return
	}
	actor, _ := currentUser(c)
	item, err := h.service.Delivery().SaveDatabase(c.Request.Context(), actor.ID, c.Param("id"), input, strings.HasSuffix(c.FullPath(), "/test"))
	if err != nil {
		h.writeDeliveryError(c, "application_database_save", err)
		return
	}
	setAuditResourceID(c, item.ID)
	c.JSON(200, gin.H{"database": item})
}
func (h pipelineHandler) deleteApplicationDatabase(c *gin.Context) {
	version, err := strconv.ParseUint(c.Query("version"), 10, 64)
	if err != nil || version == 0 {
		h.logger.Warn("删除数据库缺少版本", "operation", "application_database_delete_bind", "err", err)
		writeError(c, 400, "invalid_database", cicd.ErrInvalidDatabase.Error())
		return
	}
	if err = h.service.Delivery().DeleteDatabase(c.Request.Context(), c.Param("id"), version); err != nil {
		h.writeDeliveryError(c, "application_database_delete", err)
		return
	}
	c.Status(204)
}
func (h pipelineHandler) listEnvironmentPolicies(c *gin.Context) {
	items, err := h.service.Delivery().ListPolicies(c.Request.Context())
	if err != nil {
		h.writeDeliveryError(c, "environment_policy_list", err)
		return
	}
	c.JSON(200, gin.H{"policies": items})
}
func (h pipelineHandler) saveEnvironmentPolicy(c *gin.Context) {
	var input cicd.PolicyInput
	if err := c.ShouldBindJSON(&input); err != nil {
		h.logger.Warn("环境保护请求格式无效", "operation", "environment_policy_bind", "err", err)
		writeError(c, 400, "invalid_policy", cicd.ErrInvalidPolicy.Error())
		return
	}
	actor, _ := currentUser(c)
	item, err := h.service.Delivery().SavePolicy(c.Request.Context(), actor.ID, c.Param("id"), input)
	if err != nil {
		h.writeDeliveryError(c, "environment_policy_save", err)
		return
	}
	setAuditResourceID(c, item.ID)
	c.JSON(200, gin.H{"policy": item})
}
func (h pipelineHandler) listPolicyReviewers(c *gin.Context) {
	items, err := h.service.Delivery().Reviewers(c.Request.Context())
	if err != nil {
		h.writeDeliveryError(c, "environment_policy_reviewers", err)
		return
	}
	c.JSON(200, gin.H{"reviewers": items})
}
func (h pipelineHandler) listDatabaseMigrations(c *gin.Context) {
	items, err := h.service.DatabaseMigrations(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeDeliveryError(c, "database_migration_list", err)
		return
	}
	c.JSON(200, gin.H{"migrations": items})
}
func (h pipelineHandler) listCheckDeliveries(c *gin.Context) {
	items, err := h.service.CheckDeliveries(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, "repository_check_list", err)
		return
	}
	c.JSON(200, gin.H{"deliveries": items})
}
