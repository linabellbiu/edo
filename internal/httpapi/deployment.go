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
