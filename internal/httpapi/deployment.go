package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"zrt/internal/deployment"
	"zrt/internal/model"
)

type deploymentHandler struct {
	service *deployment.Service
	logger  *slog.Logger
}

type deploymentTargetRequest struct {
	Name           string                   `json:"name" binding:"required,max=128"`
	Description    string                   `json:"description" binding:"max=500"`
	Platform       model.DeploymentPlatform `json:"platform" binding:"required,max=16"`
	Environment    model.EnvironmentType    `json:"environment" binding:"required,max=16"`
	RuntimeID      string                   `json:"runtime_id" binding:"required,max=36"`
	Namespace      string                   `json:"namespace" binding:"max=253"`
	WorkloadName   string                   `json:"workload_name" binding:"required,max=253"`
	ContainerName  string                   `json:"container_name" binding:"max=253"`
	RolloutTimeout int                      `json:"rollout_timeout" binding:"omitempty,min=30,max=3600"`
}

type deploymentRequest struct {
	TargetID string `json:"target_id" binding:"required,max=36"`
	Image    string `json:"image" binding:"required,max=1024"`
}

func (h deploymentHandler) listTargets(c *gin.Context) {
	targets, err := h.service.ListTargets(c.Request.Context())
	if err != nil {
		h.logger.Error("查询发布目标失败", "operation", "deployment_target_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"targets": targets})
}

func (h deploymentHandler) listEnvironments(c *gin.Context) {
	environments, err := h.service.ListTargets(c.Request.Context())
	if err != nil {
		h.logger.Error("查询环境失败", "operation", "environment_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"environments": environments})
}

func (h deploymentHandler) createTarget(c *gin.Context) {
	var request deploymentTargetRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建发布目标参数无效", "operation", "deployment_target_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", deployment.ErrInvalidTarget.Error())
		return
	}
	actor, _ := currentUser(c)
	target, err := h.service.CreateTarget(c.Request.Context(), actor.ID, toDeploymentTargetInput(request))
	if err != nil {
		h.writeTargetError(c, "deployment_target_create", err)
		return
	}
	setAuditResourceID(c, target.ID)
	c.JSON(http.StatusCreated, gin.H{"target": target})
}

func (h deploymentHandler) createEnvironment(c *gin.Context) {
	var request deploymentTargetRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建环境参数无效", "operation", "environment_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", deployment.ErrInvalidTarget.Error())
		return
	}
	actor, _ := currentUser(c)
	environment, err := h.service.CreateTarget(c.Request.Context(), actor.ID, toDeploymentTargetInput(request))
	if err != nil {
		h.writeTargetError(c, "environment_create", err)
		return
	}
	setAuditResourceID(c, environment.ID)
	c.JSON(http.StatusCreated, gin.H{"environment": environment})
}

func (h deploymentHandler) updateTarget(c *gin.Context) {
	var request deploymentTargetRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新发布目标参数无效", "operation", "deployment_target_update_bind", "request_id", requestIDFrom(c), "target_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", deployment.ErrInvalidTarget.Error())
		return
	}
	target, err := h.service.UpdateTarget(c.Request.Context(), c.Param("id"), toDeploymentTargetInput(request))
	if err != nil {
		h.writeTargetError(c, "deployment_target_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"target": target})
}

func (h deploymentHandler) updateEnvironment(c *gin.Context) {
	var request deploymentTargetRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新环境参数无效", "operation", "environment_update_bind", "request_id", requestIDFrom(c), "environment_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", deployment.ErrInvalidTarget.Error())
		return
	}
	environment, err := h.service.UpdateTarget(c.Request.Context(), c.Param("id"), toDeploymentTargetInput(request))
	if err != nil {
		h.writeTargetError(c, "environment_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"environment": environment})
}

func (h deploymentHandler) setTargetStatus(c *gin.Context) {
	var request runtimeStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改发布目标状态参数无效", "operation", "deployment_target_status_bind", "request_id", requestIDFrom(c), "target_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", "发布目标状态格式无效")
		return
	}
	if err := h.service.SetTargetActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeTargetError(c, "deployment_target_status", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h deploymentHandler) setEnvironmentStatus(c *gin.Context) {
	var request runtimeStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改环境状态参数无效", "operation", "environment_status_bind", "request_id", requestIDFrom(c), "environment_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", "环境状态格式无效")
		return
	}
	if err := h.service.SetTargetActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeTargetError(c, "environment_status", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h deploymentHandler) list(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	records, err := h.service.List(c.Request.Context(), limit)
	if err != nil {
		h.logger.Error("查询发布记录失败", "operation", "deployment_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deployments": records})
}

func (h deploymentHandler) request(c *gin.Context) {
	var request deploymentRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建发布申请参数无效", "operation", "deployment_request_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", "发布申请参数无效")
		return
	}
	actor, _ := currentUser(c)
	record, err := h.service.Request(c.Request.Context(), actor.ID, deployment.RequestInput{TargetID: request.TargetID, Image: request.Image})
	if err != nil {
		h.writeDeploymentError(c, "deployment_request", err)
		return
	}
	setAuditResourceID(c, record.ID)
	c.JSON(http.StatusCreated, gin.H{"deployment": record})
}

func (h deploymentHandler) rollback(c *gin.Context) {
	actor, _ := currentUser(c)
	record, err := h.service.Rollback(c.Request.Context(), c.Param("id"), actor.ID)
	if err != nil {
		h.writeDeploymentError(c, "deployment_rollback", err)
		return
	}
	setAuditResourceID(c, record.ID)
	c.JSON(http.StatusAccepted, gin.H{"deployment": record})
}

func (h deploymentHandler) writeTargetError(c *gin.Context, operation string, err error) {
	h.logger.Warn("发布目标操作失败", "operation", operation, "request_id", requestIDFrom(c), "target_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, deployment.ErrInvalidTarget):
		writeError(c, http.StatusBadRequest, "invalid_deployment_target", deployment.ErrInvalidTarget.Error())
	case errors.Is(err, deployment.ErrTargetExists):
		writeError(c, http.StatusConflict, "deployment_target_exists", deployment.ErrTargetExists.Error())
	case errors.Is(err, deployment.ErrTargetNotFound):
		writeError(c, http.StatusNotFound, "deployment_target_not_found", deployment.ErrTargetNotFound.Error())
	default:
		writeInternalError(c)
	}
}

func (h deploymentHandler) writeDeploymentError(c *gin.Context, operation string, err error) {
	h.logger.Warn("发布操作失败", "operation", operation, "request_id", requestIDFrom(c), "deployment_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, deployment.ErrTargetNotFound):
		writeError(c, http.StatusNotFound, "deployment_target_not_found", deployment.ErrTargetNotFound.Error())
	case errors.Is(err, deployment.ErrInvalidImage):
		writeError(c, http.StatusBadRequest, "invalid_image", deployment.ErrInvalidImage.Error())
	case errors.Is(err, deployment.ErrImmutableImageRequired):
		writeError(c, http.StatusBadRequest, "immutable_image_required", deployment.ErrImmutableImageRequired.Error())
	case errors.Is(err, deployment.ErrDeploymentNotFound):
		writeError(c, http.StatusNotFound, "deployment_not_found", deployment.ErrDeploymentNotFound.Error())
	case errors.Is(err, deployment.ErrInvalidDeploymentState):
		writeError(c, http.StatusConflict, "invalid_deployment_state", deployment.ErrInvalidDeploymentState.Error())
	case errors.Is(err, deployment.ErrRollbackUnavailable):
		writeError(c, http.StatusConflict, "rollback_unavailable", deployment.ErrRollbackUnavailable.Error())
	default:
		writeInternalError(c)
	}
}

func toDeploymentTargetInput(request deploymentTargetRequest) deployment.TargetInput {
	return deployment.TargetInput{
		Name: request.Name, Description: request.Description,
		Platform: request.Platform, Environment: request.Environment,
		RuntimeID: request.RuntimeID, Namespace: request.Namespace,
		WorkloadName: request.WorkloadName, ContainerName: request.ContainerName,
		RolloutTimeout: request.RolloutTimeout,
	}
}
