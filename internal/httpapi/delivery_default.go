package httpapi

import (
	"errors"
	"net/http"

	"edo/internal/deployment"
	"edo/internal/model"
	"edo/internal/pipeline"
	"github.com/gin-gonic/gin"
)

func (h pipelineHandler) deliveryDefaults(c *gin.Context) {
	items, err := h.service.GetDeliveryDefaults(c.Request.Context())
	if err != nil {
		h.deliveryError(c, "delivery_defaults_read", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"defaults": items})
}

func (h pipelineHandler) saveDeliveryDefault(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input pipeline.DeliveryDefaultInput
	if err := c.ShouldBindJSON(&input); err != nil {
		h.logger.Warn("默认发布方案请求无效", "operation", "delivery_defaults_validate", "request_id", requestIDFrom(c), "err", err)
		writeError(c, 400, "invalid_request", "请检查默认发布方案配置")
		return
	}
	actor, _ := currentUser(c)
	if err := h.service.SetDeliveryDefault(c.Request.Context(), actor.ID, model.DeploymentPlanKind(c.Param("kind")), input); err != nil {
		h.deliveryError(c, "delivery_defaults_save", err)
		return
	}
	setAuditResourceID(c, c.Param("kind"))
	h.deliveryDefaults(c)
}

func (h pipelineHandler) createDeliveryWorkflow(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32*1024)
	var input struct {
		Kind                   model.DeploymentPlanKind `json:"kind" binding:"required,oneof=docker compose kubernetes script"`
		Name                   string                   `json:"name" binding:"max=128"`
		BuildPlanID            string                   `json:"build_plan_id" binding:"required,max=36"`
		WorkflowTemplateID     string                   `json:"workflow_template_id" binding:"max=36"`
		DeploymentPlanID       string                   `json:"deployment_plan_id" binding:"max=36"`
		ExpectedDefaultVersion uint64                   `json:"expected_default_version"`
		Target                 deploymentTargetRequest  `json:"target" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		h.logger.Warn("创建发布流程请求无效", "operation", "delivery_workflow_validate", "application_id", c.Param("id"), "request_id", requestIDFrom(c), "err", err)
		writeError(c, 400, "invalid_request", "请检查构建方案与运行位置配置")
		return
	}
	actor, _ := currentUser(c)
	result, err := h.service.CreateDeliveryWorkflow(c.Request.Context(), c.Param("id"), actor.ID, pipeline.DeliveryWorkflowInput{Kind: input.Kind, Name: input.Name, BuildPlanID: input.BuildPlanID, WorkflowTemplateID: input.WorkflowTemplateID, DeploymentPlanID: input.DeploymentPlanID, Target: toDeploymentTargetInput(input.Target), ExpectedDefaultVersion: input.ExpectedDefaultVersion})
	if err != nil {
		h.deliveryError(c, "delivery_workflow_create", err)
		return
	}
	setAuditResourceID(c, result.Workflow.ID)
	c.JSON(http.StatusCreated, result)
}

func (h pipelineHandler) deliveryError(c *gin.Context, operation string, err error) {
	h.logger.Warn("发布流程配置操作失败", "operation", operation, "request_id", requestIDFrom(c), "kind", c.Param("kind"), "application_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, pipeline.ErrDeliveryDefaultConflict):
		writeError(c, 409, "delivery_default_conflict", pipeline.ErrDeliveryDefaultConflict.Error())
	case errors.Is(err, pipeline.ErrDeliveryDefaultUnavailable):
		writeError(c, 409, "delivery_default_unavailable", pipeline.ErrDeliveryDefaultUnavailable.Error())
	case errors.Is(err, pipeline.ErrInvalidDeliveryDefault):
		writeError(c, 400, "invalid_delivery_default", pipeline.ErrInvalidDeliveryDefault.Error())
	case errors.Is(err, deployment.ErrInvalidTarget):
		writeError(c, 400, "invalid_deployment_target", deployment.ErrInvalidTarget.Error())
	default:
		h.writeError(c, operation, err)
	}
}
