package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"edo/internal/model"
	"edo/internal/pipeline"
)

type releasePlanRequest struct {
	Name        string                          `json:"name" binding:"max=128"`
	Version     string                          `json:"version" binding:"max=64"`
	Description string                          `json:"description" binding:"max=500"`
	Status      model.ReleasePlanStatus         `json:"status" binding:"omitempty,max=16"`
	Groups      []releasePlanCreateGroupRequest `json:"groups" binding:"required,min=1,max=50,dive"`
}

type releasePlanCreateGroupRequest struct {
	Name          string                          `json:"name" binding:"required,max=128"`
	Mode          model.ReleaseGroupMode          `json:"mode" binding:"omitempty,max=16"`
	FailurePolicy model.ReleaseGroupFailurePolicy `json:"failure_policy" binding:"omitempty,max=16"`
	Applications  []releaseApplicationRequest     `json:"applications" binding:"max=50,dive"`
}

type releasePlanUpdateRequest struct {
	Name        string `json:"name" binding:"max=128"`
	Version     string `json:"version" binding:"max=64"`
	Description string `json:"description" binding:"max=500"`
}

type releaseApplicationRequest struct {
	ApplicationID string                             `json:"application_id" binding:"required,max=36"`
	ManualDeploy  bool                               `json:"manual_deploy"`
	SourceType    model.ReleaseApplicationSourceType `json:"source_type" binding:"omitempty,oneof=branch commit"`
	SourceValue   string                             `json:"source_value" binding:"max=255"`
}

type releaseGroupRequest struct {
	Name              string                          `json:"name" binding:"required,max=128"`
	Mode              model.ReleaseGroupMode          `json:"mode" binding:"omitempty,max=16"`
	FailurePolicy     model.ReleaseGroupFailurePolicy `json:"failure_policy" binding:"omitempty,max=16"`
	ApplicationIDs    []string                        `json:"application_ids" binding:"omitempty,max=50,dive,max=36"`
	Applications      []releaseApplicationRequest     `json:"applications" binding:"omitempty,max=50,dive"`
	DependsOnGroupIDs []string                        `json:"depends_on_group_ids" binding:"omitempty,max=50,dive,max=36"`
}

type releasePlanStatusRequest struct {
	Active *bool `json:"active" binding:"required"`
}

type releasePlanConfigurationRequest struct {
	Description string                                 `json:"description" binding:"max=500"`
	Groups      []releasePlanConfigurationGroupRequest `json:"groups" binding:"max=50,dive"`
}

type releasePlanConfigurationGroupRequest struct {
	ID                string                          `json:"id" binding:"max=36"`
	Name              string                          `json:"name" binding:"required,max=128"`
	Mode              model.ReleaseGroupMode          `json:"mode" binding:"omitempty,max=16"`
	FailurePolicy     model.ReleaseGroupFailurePolicy `json:"failure_policy" binding:"omitempty,max=16"`
	Applications      []releaseApplicationRequest     `json:"applications" binding:"max=50,dive"`
	DependsOnGroupIDs []string                        `json:"depends_on_group_ids" binding:"max=50,dive,max=36"`
}

type releasePlanExecutionRequest struct {
	RequestID             string                                 `json:"request_id" binding:"required,max=128"`
	ExpectedPlanUpdatedAt time.Time                              `json:"expected_plan_updated_at" binding:"required"`
	Selections            []releasePlanExecutionSelectionRequest `json:"selections" binding:"required,min=1,max=50,dive"`
}

type releasePlanExecutionSelectionRequest struct {
	ReleaseGroupApplicationID string `json:"release_group_application_id" binding:"required,max=36"`
	WorkflowID                string `json:"workflow_id" binding:"required,max=36"`
	ExpectedWorkflowRevision  uint64 `json:"expected_workflow_revision" binding:"required"`
	SourceNodeID              string `json:"source_node_id" binding:"required,max=64"`
	Ref                       string `json:"ref" binding:"required,max=512"`
	CommitSHA                 string `json:"commit_sha" binding:"required,max=64"`
}

func (h pipelineHandler) listReleasePlans(c *gin.Context) {
	plans, err := h.service.ListReleasePlans(c.Request.Context())
	if err != nil {
		h.writeError(c, "release_plan_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"release_plans": plans})
}

func (h pipelineHandler) getReleasePlan(c *gin.Context) {
	plan, err := h.service.FindReleasePlan(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, "release_plan_get", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"release_plan": plan})
}

func (h pipelineHandler) createReleasePlan(c *gin.Context) {
	var request releasePlanRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("发布计划请求格式无效", "operation", "release_plan_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_release_plan", pipeline.ErrInvalidReleasePlan.Error())
		return
	}
	applicationCount := 0
	groups := make([]pipeline.ReleaseGroupInput, 0, len(request.Groups))
	for _, group := range request.Groups {
		applicationCount += len(group.Applications)
		groups = append(groups, pipeline.ReleaseGroupInput{
			Name: group.Name, Mode: group.Mode, FailurePolicy: group.FailurePolicy,
			Applications: toReleaseApplicationInputs(group.Applications),
		})
	}
	if applicationCount == 0 {
		h.logger.Warn("创建发布计划的发布组未配置应用", "operation", "release_plan_create_validate", "request_id", requestIDFrom(c))
		writeError(c, http.StatusBadRequest, "invalid_release_plan", pipeline.ErrInvalidReleasePlan.Error())
		return
	}
	actor, _ := currentUser(c)
	plan, err := h.service.CreateReleasePlan(c.Request.Context(), actor.ID, pipeline.ReleasePlanInput{
		Name: request.Name, Version: request.Version, Description: request.Description, Status: request.Status,
		Groups: groups,
	})
	if err != nil {
		h.writeError(c, "release_plan_create", err)
		return
	}
	setAuditResourceID(c, plan.ID)
	c.JSON(http.StatusCreated, gin.H{"release_plan": plan})
}

func (h pipelineHandler) updateReleasePlan(c *gin.Context) {
	var request releasePlanUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新发布计划请求格式无效", "operation", "release_plan_update_bind", "request_id", requestIDFrom(c), "release_plan_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_release_plan", pipeline.ErrInvalidReleasePlan.Error())
		return
	}
	actor, _ := currentUser(c)
	plan, err := h.service.UpdateReleasePlan(c.Request.Context(), c.Param("id"), actor.ID, pipeline.ReleasePlanInput{
		Name: request.Name, Version: request.Version, Description: request.Description,
	})
	if err != nil {
		h.writeError(c, "release_plan_update", err)
		return
	}
	setAuditResourceID(c, plan.ID)
	c.JSON(http.StatusOK, gin.H{"release_plan": plan})
}

func (h pipelineHandler) setReleasePlanStatus(c *gin.Context) {
	var request releasePlanStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("发布计划启用状态参数无效", "operation", "release_plan_status_bind", "request_id", requestIDFrom(c), "release_plan_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_release_plan_status", pipeline.ErrInvalidReleasePlan.Error())
		return
	}
	actor, _ := currentUser(c)
	plan, err := h.service.SetReleasePlanActive(c.Request.Context(), c.Param("id"), actor.ID, *request.Active)
	if err != nil {
		h.writeError(c, "release_plan_status_update", err)
		return
	}
	setAuditResourceID(c, plan.ID)
	c.JSON(http.StatusOK, gin.H{"release_plan": plan})
}

func (h pipelineHandler) saveReleasePlanConfiguration(c *gin.Context) {
	var request releasePlanConfigurationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("发布计划批量配置参数无效", "operation", "release_plan_configuration_bind", "request_id", requestIDFrom(c), "release_plan_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_release_plan_configuration", pipeline.ErrInvalidReleasePlan.Error())
		return
	}
	groups := make([]pipeline.ReleaseGroupConfigurationInput, 0, len(request.Groups))
	for _, group := range request.Groups {
		groups = append(groups, pipeline.ReleaseGroupConfigurationInput{
			ID: group.ID, Name: group.Name, Mode: group.Mode, FailurePolicy: group.FailurePolicy,
			Applications: toReleaseApplicationInputs(group.Applications), DependsOnGroupIDs: group.DependsOnGroupIDs,
		})
	}
	actor, _ := currentUser(c)
	plan, err := h.service.SaveReleasePlanConfiguration(c.Request.Context(), c.Param("id"), actor.ID, pipeline.ReleasePlanConfigurationInput{
		Description: request.Description, Groups: groups,
	})
	if err != nil {
		h.writeError(c, "release_plan_configuration_update", err)
		return
	}
	setAuditResourceID(c, plan.ID)
	c.JSON(http.StatusOK, gin.H{"release_plan": plan})
}

func (h pipelineHandler) deleteReleasePlan(c *gin.Context) {
	if err := h.service.DeleteReleasePlan(c.Request.Context(), c.Param("id")); err != nil {
		h.writeError(c, "release_plan_delete", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h pipelineHandler) createReleasePlanExecution(c *gin.Context) {
	var request releasePlanExecutionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("发布计划执行请求格式无效", "operation", "release_plan_execution_bind", "request_id", requestIDFrom(c), "release_plan_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_release_plan_execution", pipeline.ErrInvalidReleasePlanExecution.Error())
		return
	}
	selections := make([]pipeline.ReleasePlanExecutionSelection, 0, len(request.Selections))
	for _, selection := range request.Selections {
		selections = append(selections, pipeline.ReleasePlanExecutionSelection{
			ReleaseGroupApplicationID: selection.ReleaseGroupApplicationID,
			WorkflowID:                selection.WorkflowID,
			ExpectedWorkflowRevision:  selection.ExpectedWorkflowRevision,
			SourceNodeID:              selection.SourceNodeID,
			Ref:                       selection.Ref,
			CommitSHA:                 selection.CommitSHA,
		})
	}
	actor, _ := currentUser(c)
	execution, err := h.service.CreateReleasePlanExecution(c.Request.Context(), c.Param("id"), actor.ID, pipeline.ReleasePlanExecutionInput{
		RequestID: request.RequestID, ExpectedPlanUpdatedAt: request.ExpectedPlanUpdatedAt, Selections: selections,
	})
	if err != nil {
		h.writeError(c, "release_plan_execution_create", err)
		return
	}
	setAuditResourceID(c, execution.ID)
	if reconciled, reconcileErr := h.service.ReconcileReleasePlanExecution(c.Request.Context(), execution.ID); reconcileErr == nil {
		execution = reconciled
	} else {
		h.logger.Error("发布计划已创建但首次推进失败", "operation", "release_plan_execution_reconcile", "request_id", requestIDFrom(c), "release_plan_execution_id", execution.ID, "err", reconcileErr)
	}
	c.JSON(http.StatusAccepted, gin.H{"release_plan_execution": execution})
}

func (h pipelineHandler) createReleaseGroup(c *gin.Context) {
	h.saveReleaseGroup(c, "")
}

func (h pipelineHandler) updateReleaseGroup(c *gin.Context) {
	h.saveReleaseGroup(c, c.Param("group_id"))
}

func (h pipelineHandler) saveReleaseGroup(c *gin.Context, groupID string) {
	var request releaseGroupRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("发布组请求格式无效", "operation", "release_group_bind", "request_id", requestIDFrom(c), "release_plan_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_release_group", pipeline.ErrInvalidReleaseGroup.Error())
		return
	}
	input := pipeline.ReleaseGroupInput{
		Name: request.Name, Mode: request.Mode, FailurePolicy: request.FailurePolicy,
		ApplicationIDs: request.ApplicationIDs, Applications: toReleaseApplicationInputs(request.Applications),
		DependsOnGroupIDs: request.DependsOnGroupIDs,
	}
	var (
		plan *model.ReleasePlan
		err  error
	)
	if groupID == "" {
		plan, err = h.service.CreateReleaseGroup(c.Request.Context(), c.Param("id"), input)
	} else {
		plan, err = h.service.UpdateReleaseGroup(c.Request.Context(), c.Param("id"), groupID, input)
	}
	if err != nil {
		h.writeError(c, "release_group_save", err)
		return
	}
	setAuditResourceID(c, c.Param("id"))
	status := http.StatusOK
	if groupID == "" {
		status = http.StatusCreated
	}
	c.JSON(status, gin.H{"release_plan": plan})
}

func toReleaseApplicationInputs(requests []releaseApplicationRequest) []pipeline.ReleaseApplicationInput {
	result := make([]pipeline.ReleaseApplicationInput, 0, len(requests))
	for _, request := range requests {
		result = append(result, pipeline.ReleaseApplicationInput{
			ApplicationID: request.ApplicationID, ManualDeploy: request.ManualDeploy,
			SourceType: request.SourceType, SourceValue: request.SourceValue,
		})
	}
	return result
}

func (h pipelineHandler) deleteReleaseGroup(c *gin.Context) {
	plan, err := h.service.DeleteReleaseGroup(c.Request.Context(), c.Param("id"), c.Param("group_id"))
	if err != nil {
		h.writeError(c, "release_group_delete", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"release_plan": plan})
}
