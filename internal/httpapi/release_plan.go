package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"zrt/internal/model"
	"zrt/internal/pipeline"
)

type releasePlanRequest struct {
	Name         string                      `json:"name" binding:"max=128"`
	Version      string                      `json:"version" binding:"max=64"`
	Description  string                      `json:"description" binding:"max=500"`
	Status       model.ReleasePlanStatus     `json:"status" binding:"omitempty,max=16"`
	Applications []releaseApplicationRequest `json:"applications" binding:"omitempty,max=50,dive"`
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
	if len(request.Applications) == 0 {
		h.logger.Warn("创建发布计划未选择应用", "operation", "release_plan_create_validate", "request_id", requestIDFrom(c))
		writeError(c, http.StatusBadRequest, "invalid_release_plan", pipeline.ErrInvalidReleasePlan.Error())
		return
	}
	actor, _ := currentUser(c)
	plan, err := h.service.CreateReleasePlan(c.Request.Context(), actor.ID, pipeline.ReleasePlanInput{
		Name: request.Name, Version: request.Version, Description: request.Description, Status: request.Status,
		Applications: toReleaseApplicationInputs(request.Applications),
	})
	if err != nil {
		h.writeError(c, "release_plan_create", err)
		return
	}
	setAuditResourceID(c, plan.ID)
	c.JSON(http.StatusCreated, gin.H{"release_plan": plan})
}

func (h pipelineHandler) updateReleasePlan(c *gin.Context) {
	var request releasePlanRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新发布计划请求格式无效", "operation", "release_plan_update_bind", "request_id", requestIDFrom(c), "release_plan_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_release_plan", pipeline.ErrInvalidReleasePlan.Error())
		return
	}
	actor, _ := currentUser(c)
	plan, err := h.service.UpdateReleasePlan(c.Request.Context(), c.Param("id"), actor.ID, pipeline.ReleasePlanInput{
		Name: request.Name, Version: request.Version, Description: request.Description, Status: request.Status,
	})
	if err != nil {
		h.writeError(c, "release_plan_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"release_plan": plan})
}

func (h pipelineHandler) deleteReleasePlan(c *gin.Context) {
	if err := h.service.DeleteReleasePlan(c.Request.Context(), c.Param("id")); err != nil {
		h.writeError(c, "release_plan_delete", err)
		return
	}
	c.Status(http.StatusNoContent)
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
