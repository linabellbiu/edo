package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"edo/internal/deployment"
	"edo/internal/model"
)

type deploymentHandler struct {
	service *deployment.Service
	logger  *slog.Logger
}

type deploymentTargetRequest struct {
	Name             string                   `json:"name" binding:"required,max=128"`
	Description      string                   `json:"description" binding:"max=500"`
	Platform         model.DeploymentPlatform `json:"platform" binding:"required,max=16"`
	EnvironmentID    string                   `json:"environment_id" binding:"max=36"`
	HostID           string                   `json:"host_id" binding:"max=36"`
	RuntimeID        string                   `json:"runtime_id" binding:"max=36"`
	WorkingDirectory string                   `json:"working_directory" binding:"max=1024"`
	Namespace        string                   `json:"namespace" binding:"max=253"`
	WorkloadName     string                   `json:"workload_name" binding:"max=253"`
	ContainerName    string                   `json:"container_name" binding:"max=253"`
	RolloutTimeout   int                      `json:"rollout_timeout" binding:"omitempty,min=30,max=3600"`
}

type deploymentScaleRequest struct {
	Replicas *int32 `json:"replicas" binding:"required,min=0,max=1000"`
}

type deploymentInstanceControlRequest struct {
	RestartScript  string `json:"restart_script" binding:"max=262144"`
	StopScript     string `json:"stop_script" binding:"max=262144"`
	TimeoutSeconds int    `json:"timeout_seconds" binding:"required,min=30,max=3600"`
}

func (h deploymentHandler) list(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	var records []model.DeploymentRecord
	var err error
	if pipelineRunID := c.Query("pipeline_run_id"); pipelineRunID != "" {
		records, err = h.service.ListForPipelineRun(c.Request.Context(), pipelineRunID, limit)
	} else {
		records, err = h.service.List(c.Request.Context(), limit)
	}
	if err != nil {
		h.logger.Error("查询发布记录失败", "operation", "deployment_list", "request_id", requestIDFrom(c),
			"pipeline_run_id", c.Query("pipeline_run_id"), "err", err)
		if errors.Is(err, deployment.ErrInvalidDeploymentState) {
			writeError(c, http.StatusBadRequest, "invalid_pipeline_run", "流水线运行标识无效")
			return
		}
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deployments": records})
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

func (h deploymentHandler) runtimeState(c *gin.Context) {
	state, err := h.service.RuntimeState(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeRuntimeError(c, "deployment_runtime_state", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"runtime": state})
}

func (h deploymentHandler) runtimeConfiguration(c *gin.Context) {
	configuration, err := h.service.RuntimeConfiguration(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeRuntimeError(c, "deployment_runtime_configuration_read", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"configuration": configuration})
}

func (h deploymentHandler) saveRuntimeConfiguration(c *gin.Context) {
	var request deploymentInstanceControlRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_runtime_configuration", deployment.ErrRuntimeControlInvalid.Error())
		return
	}
	actor, _ := currentUser(c)
	configuration, err := h.service.SaveRuntimeConfiguration(c.Request.Context(), c.Param("id"), actor.ID,
		deployment.InstanceControlConfigurationInput{
			RestartScript: request.RestartScript, StopScript: request.StopScript, TimeoutSeconds: request.TimeoutSeconds,
		})
	if err != nil {
		h.writeRuntimeError(c, "deployment_runtime_configuration_update", err)
		return
	}
	setAuditResourceID(c, c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"configuration": configuration})
}

func (h deploymentHandler) restart(c *gin.Context) {
	h.controlRuntime(c, deployment.RuntimeControlInput{Action: "restart"})
}

func (h deploymentHandler) stop(c *gin.Context) {
	h.controlRuntime(c, deployment.RuntimeControlInput{Action: "stop"})
}

func (h deploymentHandler) removeRuntime(c *gin.Context) {
	actor, _ := currentUser(c)
	if err := h.service.RemoveRuntime(c.Request.Context(), c.Param("id"), actor.ID); err != nil {
		h.writeRuntimeError(c, "deployment_runtime_remove", err)
		return
	}
	setAuditResourceID(c, c.Param("id"))
	c.Status(http.StatusNoContent)
}

func (h deploymentHandler) scale(c *gin.Context) {
	var request deploymentScaleRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Replicas == nil {
		writeError(c, http.StatusBadRequest, "invalid_runtime_scale", deployment.ErrRuntimeControlInvalid.Error())
		return
	}
	h.controlRuntime(c, deployment.RuntimeControlInput{Action: "scale", Replicas: request.Replicas})
}

func (h deploymentHandler) controlRuntime(c *gin.Context, input deployment.RuntimeControlInput) {
	actor, _ := currentUser(c)
	state, err := h.service.ControlRuntime(c.Request.Context(), c.Param("id"), actor.ID, input)
	if err != nil {
		h.writeRuntimeError(c, "deployment_runtime_"+input.Action, err)
		return
	}
	setAuditResourceID(c, c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"runtime": state})
}

func (h deploymentHandler) writeRuntimeError(c *gin.Context, operation string, err error) {
	h.logger.Warn("部署实例运行操作失败", "operation", operation, "request_id", requestIDFrom(c),
		"deployment_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, deployment.ErrDeploymentNotFound):
		writeError(c, http.StatusNotFound, "deployment_not_found", deployment.ErrDeploymentNotFound.Error())
	case errors.Is(err, deployment.ErrDeploymentNotCurrent):
		writeError(c, http.StatusConflict, "deployment_not_current", deployment.ErrDeploymentNotCurrent.Error())
	case errors.Is(err, deployment.ErrRuntimeInstanceRemoved):
		writeError(c, http.StatusConflict, "runtime_instance_removed", deployment.ErrRuntimeInstanceRemoved.Error())
	case errors.Is(err, deployment.ErrLifecycleScriptRequired):
		writeError(c, http.StatusConflict, "lifecycle_script_required", deployment.ErrLifecycleScriptRequired.Error())
	case errors.Is(err, deployment.ErrRuntimeControlUnsupported):
		writeError(c, http.StatusUnprocessableEntity, "runtime_control_unsupported", deployment.ErrRuntimeControlUnsupported.Error())
	case errors.Is(err, deployment.ErrRuntimeControlInvalid):
		writeError(c, http.StatusBadRequest, "invalid_runtime_control", deployment.ErrRuntimeControlInvalid.Error())
	case errors.Is(err, deployment.ErrRuntimeStateUnavailable):
		writeError(c, http.StatusBadGateway, "runtime_state_unavailable", deployment.ErrRuntimeStateUnavailable.Error())
	case errors.Is(err, deployment.ErrRuntimeControlFailed):
		writeError(c, http.StatusBadGateway, "runtime_control_failed", deployment.ErrRuntimeControlFailed.Error())
	case errors.Is(err, deployment.ErrRuntimeRemovalFailed):
		writeError(c, http.StatusBadGateway, "runtime_removal_failed", deployment.ErrRuntimeRemovalFailed.Error())
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
	case errors.Is(err, deployment.ErrCommandPipelineRequired):
		writeError(c, http.StatusConflict, "ssh_command_pipeline_required", deployment.ErrCommandPipelineRequired.Error())
	default:
		writeInternalError(c)
	}
}

func toDeploymentTargetInput(request deploymentTargetRequest) deployment.TargetInput {
	return deployment.TargetInput{
		Name: request.Name, Description: request.Description,
		Platform:      request.Platform,
		EnvironmentID: request.EnvironmentID, HostID: request.HostID,
		RuntimeID: request.RuntimeID, WorkingDirectory: request.WorkingDirectory,
		Namespace:    request.Namespace,
		WorkloadName: request.WorkloadName, ContainerName: request.ContainerName,
		RolloutTimeout: request.RolloutTimeout,
	}
}
