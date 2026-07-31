package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"zrt/internal/deployment"
	"zrt/internal/model"
	"zrt/internal/pipeline"
)

type pipelineHandler struct {
	service *pipeline.Service
	logger  *slog.Logger
}

type applicationRequest struct {
	Name                string `json:"name" binding:"required,max=128"`
	Description         string `json:"description" binding:"max=500"`
	RepositoryID        string `json:"repository_id" binding:"required,max=36"`
	PollIntervalSeconds int    `json:"poll_interval_seconds" binding:"omitempty,oneof=3 5 10 60"`
}

type workflowCreateRequest struct {
	Name               string `json:"name" binding:"max=128"`
	WorkflowTemplateID string `json:"workflow_template_id" binding:"max=36"`
	PresetKey          string `json:"preset_key" binding:"max=32"`
}

type workflowRequest struct {
	SchemaVersion uint16                `json:"schema_version" binding:"required,eq=1"`
	Name          string                `json:"name" binding:"required,max=128"`
	Revision      uint64                `json:"revision"`
	Activate      bool                  `json:"activate"`
	Source        model.WorkflowNode    `json:"source" binding:"required"`
	Stages        []model.WorkflowStage `json:"stages" binding:"max=50"`
}

type workflowTemplateRequest struct {
	SchemaVersion uint16                `json:"schema_version" binding:"required,eq=1"`
	Name          string                `json:"name" binding:"required,max=128"`
	Description   string                `json:"description" binding:"max=500"`
	Revision      uint64                `json:"revision"`
	Activate      bool                  `json:"activate"`
	Source        model.WorkflowNode    `json:"source" binding:"required"`
	Stages        []model.WorkflowStage `json:"stages" binding:"max=50"`
}

type executeRunRequest struct {
	Ref          string `json:"ref" binding:"max=512"`
	CommitSHA    string `json:"commit_sha" binding:"max=64"`
	SourceNodeID string `json:"source_node_id" binding:"max=64"`
}

type buildPlanRequest struct {
	Name                 string              `json:"name" binding:"required,max=128"`
	Kind                 model.BuildPlanKind `json:"kind" binding:"required,max=16"`
	Description          string              `json:"description" binding:"max=500"`
	Script               string              `json:"script" binding:"max=262144"`
	DockerfilePath       string              `json:"dockerfile_path" binding:"max=512"`
	ContextPath          string              `json:"context_path" binding:"max=512"`
	WorkingDirectory     string              `json:"working_directory" binding:"max=512"`
	ArtifactPath         string              `json:"artifact_path" binding:"max=512"`
	RuntimeImage         string              `json:"runtime_image" binding:"max=512"`
	ImageRegistryID      string              `json:"image_registry_id" binding:"max=36"`
	TargetStage          string              `json:"target_stage" binding:"max=128"`
	Platform             string              `json:"platform" binding:"max=64"`
	Pull                 *bool               `json:"pull"`
	CacheEnabled         *bool               `json:"cache_enabled"`
	BuildArgs            map[string]string   `json:"build_args" binding:"max=100"`
	EnvironmentVariables map[string]string   `json:"environment_variables" binding:"max=100"`
	TimeoutSeconds       int                 `json:"timeout_seconds" binding:"omitempty,min=30,max=7200"`
}

type registryRequest struct {
	Name              string                 `json:"name" binding:"required,max=128"`
	Provider          model.RegistryProvider `json:"provider" binding:"required,max=24"`
	Endpoint          string                 `json:"endpoint" binding:"max=1024"`
	Namespace         string                 `json:"namespace" binding:"max=255"`
	Username          string                 `json:"username" binding:"max=255"`
	Credential        *string                `json:"credential" binding:"omitempty,max=65536"`
	AllowInsecureHTTP bool                   `json:"allow_insecure_http"`
}

type deploymentPlanRequest struct {
	Name             string                      `json:"name" binding:"required,max=128"`
	Kind             model.DeploymentPlanKind    `json:"kind" binding:"required,max=16"`
	DeploymentTarget *deploymentTargetRequest    `json:"deployment_target" binding:"required"`
	Description      string                      `json:"description" binding:"max=500"`
	Script           string                      `json:"script" binding:"max=262144"`
	ComposeYAML      string                      `json:"compose_yaml" binding:"max=524288"`
	ServiceName      string                      `json:"service_name" binding:"max=255"`
	DockerConfig     model.DockerContainerConfig `json:"docker_config"`
	TimeoutSeconds   int                         `json:"timeout_seconds" binding:"omitempty,min=30,max=3600"`
}

type imageRegistryResponse struct {
	model.ImageRegistry
	HasCredential bool `json:"has_credential"`
}

func (h pipelineHandler) listApplications(c *gin.Context) {
	applications, err := h.service.ListApplications(c.Request.Context())
	if err != nil {
		h.writeError(c, "application_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"applications": applications})
}

func (h pipelineHandler) createApplication(c *gin.Context) {
	var request applicationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_application", pipeline.ErrInvalidApplication.Error())
		return
	}
	actor, _ := currentUser(c)
	application, err := h.service.CreateApplication(c.Request.Context(), actor.ID, toApplicationInput(request))
	if err != nil {
		h.writeError(c, "application_create", err)
		return
	}
	setAuditResourceID(c, application.ID)
	c.JSON(http.StatusCreated, gin.H{"application": application})
}

func (h pipelineHandler) updateApplication(c *gin.Context) {
	var request applicationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_application", pipeline.ErrInvalidApplication.Error())
		return
	}
	application, err := h.service.UpdateApplication(c.Request.Context(), c.Param("id"), toApplicationInput(request))
	if err != nil {
		h.writeError(c, "application_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"application": application})
}

func (h pipelineHandler) setApplicationStatus(c *gin.Context) {
	var request runtimeStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		writeError(c, http.StatusBadRequest, "invalid_application_status", "应用状态格式无效")
		return
	}
	if err := h.service.SetApplicationActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeError(c, "application_status", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h pipelineHandler) syncApplication(c *gin.Context) {
	application, run, err := h.service.SyncApplication(c.Request.Context(), c.Param("id"), "manual_sync")
	if err != nil {
		h.writeError(c, "application_sync", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"application": application, "pipeline_run": run})
}

func (h pipelineHandler) listApplicationRefs(c *gin.Context) {
	refs, err := h.service.ListWorkflowRefs(c.Request.Context(), c.Param("id"), c.Param("workflow_id"))
	if err != nil {
		h.writeError(c, "application_refs", err)
		return
	}
	c.JSON(http.StatusOK, refs)
}

func (h pipelineHandler) prepareRun(c *gin.Context) {
	actor, _ := currentUser(c)
	run, err := h.service.PrepareWorkflowRun(c.Request.Context(), c.Param("id"), c.Param("workflow_id"), actor.ID)
	if err != nil && !errors.Is(err, pipeline.ErrPipelineIncomplete) {
		h.writeError(c, "pipeline_prepare", err)
		return
	}
	if errors.Is(err, pipeline.ErrPipelineIncomplete) {
		message := err.Error()
		if run != nil && run.Message != "" {
			message = run.Message
		}
		c.JSON(http.StatusConflict, gin.H{"code": "pipeline_incomplete", "message": message, "pipeline_run": run, "request_id": requestIDFrom(c)})
		return
	}
	setAuditResourceID(c, run.ID)
	c.JSON(http.StatusCreated, gin.H{"pipeline_run": run})
}

func (h pipelineHandler) listWorkflows(c *gin.Context) {
	workflows, err := h.service.ListApplicationWorkflows(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, "workflow_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"workflows": workflows})
}

func (h pipelineHandler) createWorkflow(c *gin.Context) {
	var request workflowCreateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_workflow", pipeline.ErrInvalidWorkflow.Error())
		return
	}
	actor, _ := currentUser(c)
	result, err := h.service.CreateApplicationWorkflow(c.Request.Context(), c.Param("id"), actor.ID, pipeline.WorkflowCreateInput{
		Name: request.Name, WorkflowTemplateID: request.WorkflowTemplateID, PresetKey: request.PresetKey,
	})
	if err != nil {
		h.writeError(c, "workflow_create", err)
		return
	}
	setAuditResourceID(c, result.Workflow.ID)
	c.JSON(http.StatusCreated, result)
}

func (h pipelineHandler) getWorkflow(c *gin.Context) {
	result, err := h.service.GetApplicationWorkflow(c.Request.Context(), c.Param("id"), c.Param("workflow_id"))
	if err != nil {
		h.writeError(c, "workflow_get", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h pipelineHandler) validateWorkflow(c *gin.Context) {
	var request workflowRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_workflow", pipeline.ErrInvalidWorkflow.Error())
		return
	}
	result, err := h.service.ValidateApplicationWorkflow(c.Request.Context(), c.Param("id"), c.Param("workflow_id"), toWorkflowInput(request))
	if err != nil {
		h.writeError(c, "workflow_validate", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h pipelineHandler) saveWorkflow(c *gin.Context) {
	var request workflowRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_workflow", pipeline.ErrInvalidWorkflow.Error())
		return
	}
	actor, _ := currentUser(c)
	result, err := h.service.SaveApplicationWorkflow(c.Request.Context(), c.Param("id"), c.Param("workflow_id"), actor.ID, toWorkflowInput(request))
	if errors.Is(err, pipeline.ErrInvalidWorkflow) && result != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"code": "invalid_workflow", "message": err.Error(), "workflow": result.Workflow,
			"valid": result.Valid, "issues": result.Issues, "request_id": requestIDFrom(c),
		})
		return
	}
	if err != nil {
		h.writeError(c, "workflow_save", err)
		return
	}
	setAuditResourceID(c, result.Workflow.ID)
	c.JSON(http.StatusOK, result)
}

func (h pipelineHandler) deleteWorkflow(c *gin.Context) {
	if err := h.service.DeleteApplicationWorkflow(c.Request.Context(), c.Param("id"), c.Param("workflow_id")); err != nil {
		h.writeError(c, "workflow_delete", err)
		return
	}
	setAuditResourceID(c, c.Param("workflow_id"))
	c.Status(http.StatusNoContent)
}

func (h pipelineHandler) listWorkflowTemplates(c *gin.Context) {
	templates, err := h.service.ListWorkflowTemplates(c.Request.Context())
	if err != nil {
		h.writeError(c, "workflow_template_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"workflow_templates": templates})
}

func (h pipelineHandler) getWorkflowTemplate(c *gin.Context) {
	result, err := h.service.GetWorkflowTemplate(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, "workflow_template_get", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h pipelineHandler) validateWorkflowTemplate(c *gin.Context) {
	var request workflowTemplateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_workflow_template", pipeline.ErrInvalidWorkflow.Error())
		return
	}
	result, err := h.service.ValidateWorkflowTemplate(c.Request.Context(), toWorkflowTemplateInput(request))
	if err != nil {
		h.writeError(c, "workflow_template_validate", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h pipelineHandler) createWorkflowTemplate(c *gin.Context) {
	var request workflowTemplateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_workflow_template", pipeline.ErrInvalidWorkflow.Error())
		return
	}
	actor, _ := currentUser(c)
	result, err := h.service.CreateWorkflowTemplate(c.Request.Context(), actor.ID, toWorkflowTemplateInput(request))
	if errors.Is(err, pipeline.ErrInvalidWorkflow) && result != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "invalid_workflow_template", "message": err.Error(), "workflow_template": result.WorkflowTemplate, "valid": false, "issues": result.Issues, "request_id": requestIDFrom(c)})
		return
	}
	if err != nil {
		h.writeError(c, "workflow_template_create", err)
		return
	}
	setAuditResourceID(c, result.WorkflowTemplate.ID)
	c.JSON(http.StatusCreated, result)
}

func (h pipelineHandler) saveWorkflowTemplate(c *gin.Context) {
	var request workflowTemplateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_workflow_template", pipeline.ErrInvalidWorkflow.Error())
		return
	}
	actor, _ := currentUser(c)
	result, err := h.service.SaveWorkflowTemplate(c.Request.Context(), c.Param("id"), actor.ID, toWorkflowTemplateInput(request))
	if errors.Is(err, pipeline.ErrInvalidWorkflow) && result != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "invalid_workflow_template", "message": err.Error(), "workflow_template": result.WorkflowTemplate, "valid": false, "issues": result.Issues, "request_id": requestIDFrom(c)})
		return
	}
	if err != nil {
		h.writeError(c, "workflow_template_save", err)
		return
	}
	setAuditResourceID(c, result.WorkflowTemplate.ID)
	c.JSON(http.StatusOK, result)
}

func (h pipelineHandler) deleteWorkflowTemplate(c *gin.Context) {
	if err := h.service.DeleteWorkflowTemplate(c.Request.Context(), c.Param("id")); err != nil {
		h.writeError(c, "workflow_template_delete", err)
		return
	}
	setAuditResourceID(c, c.Param("id"))
	c.Status(http.StatusNoContent)
}

func (h pipelineHandler) advanceRun(c *gin.Context) {
	actor, _ := currentUser(c)
	run, err := h.service.AdvanceRun(c.Request.Context(), c.Param("id"), actor.ID, "")
	if err != nil {
		h.writeError(c, "workflow_run_advance", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"pipeline_run": run})
}

func (h pipelineHandler) executeRun(c *gin.Context) {
	var request executeRunRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(c, http.StatusBadRequest, "invalid_manual_commit", pipeline.ErrManualCommitRequired.Error())
			return
		}
	}
	actor, _ := currentUser(c)
	run, err := h.service.ExecuteRun(
		c.Request.Context(), c.Param("id"), actor.ID, request.Ref, request.CommitSHA,
		request.SourceNodeID,
	)
	if err != nil {
		h.writeError(c, "workflow_run_execute", err)
		return
	}
	setAuditResourceID(c, run.ID)
	c.JSON(http.StatusOK, gin.H{"pipeline_run": run})
}

func (h pipelineHandler) retryRun(c *gin.Context) {
	actor, _ := currentUser(c)
	run, err := h.service.RetryRun(c.Request.Context(), c.Param("id"), actor.ID)
	if err != nil {
		h.writeError(c, "workflow_run_retry", err)
		return
	}
	setAuditResourceID(c, run.ID)
	c.JSON(http.StatusCreated, gin.H{"pipeline_run": run})
}

func (h pipelineHandler) approveRun(c *gin.Context) {
	actor, _ := currentUser(c)
	run, err := h.service.ApproveRun(c.Request.Context(), c.Param("id"), actor.ID)
	if err != nil {
		h.writeError(c, "workflow_run_approve", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"pipeline_run": run})
}

func (h pipelineHandler) listBuildPlans(c *gin.Context) {
	plans, err := h.service.ListBuildPlans(c.Request.Context())
	if err != nil {
		h.writeError(c, "build_plan_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"build_plans": plans})
}

func (h pipelineHandler) createBuildPlan(c *gin.Context) {
	var request buildPlanRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_build_plan", pipeline.ErrInvalidBuildPlan.Error())
		return
	}
	actor, _ := currentUser(c)
	plan, err := h.service.CreateBuildPlan(c.Request.Context(), actor.ID, pipeline.BuildPlanInput{
		Name: request.Name, Kind: request.Kind, Description: request.Description, Script: request.Script,
		DockerfilePath: request.DockerfilePath, ContextPath: request.ContextPath,
		WorkingDirectory: request.WorkingDirectory, ArtifactPath: request.ArtifactPath, RuntimeImage: request.RuntimeImage,
		ImageRegistryID: request.ImageRegistryID, TargetStage: request.TargetStage, Platform: request.Platform,
		Pull: request.Pull, CacheEnabled: request.CacheEnabled, BuildArgs: request.BuildArgs,
		EnvironmentVariables: request.EnvironmentVariables, TimeoutSeconds: request.TimeoutSeconds,
	})
	if err != nil {
		h.writeError(c, "build_plan_create", err)
		return
	}
	setAuditResourceID(c, plan.ID)
	c.JSON(http.StatusCreated, gin.H{"build_plan": plan})
}

func (h pipelineHandler) updateBuildPlan(c *gin.Context) {
	var request buildPlanRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_build_plan", pipeline.ErrInvalidBuildPlan.Error())
		return
	}
	plan, err := h.service.UpdateBuildPlan(c.Request.Context(), c.Param("id"), pipeline.BuildPlanInput{
		Name: request.Name, Kind: request.Kind, Description: request.Description, Script: request.Script,
		DockerfilePath: request.DockerfilePath, ContextPath: request.ContextPath,
		WorkingDirectory: request.WorkingDirectory, ArtifactPath: request.ArtifactPath, RuntimeImage: request.RuntimeImage,
		ImageRegistryID: request.ImageRegistryID, TargetStage: request.TargetStage, Platform: request.Platform,
		Pull: request.Pull, CacheEnabled: request.CacheEnabled, BuildArgs: request.BuildArgs,
		EnvironmentVariables: request.EnvironmentVariables, TimeoutSeconds: request.TimeoutSeconds,
	})
	if err != nil {
		h.writeError(c, "build_plan_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"build_plan": plan})
}

func (h pipelineHandler) setBuildPlanStatus(c *gin.Context) {
	var request runtimeStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		writeError(c, http.StatusBadRequest, "invalid_build_plan_status", "构建方案状态格式无效")
		return
	}
	if err := h.service.SetBuildPlanActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeError(c, "build_plan_status", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h pipelineHandler) deleteBuildPlan(c *gin.Context) {
	if err := h.service.DeleteBuildPlan(c.Request.Context(), c.Param("id")); err != nil {
		h.writeError(c, "build_plan_delete", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h pipelineHandler) listRegistries(c *gin.Context) {
	registries, err := h.service.ListRegistries(c.Request.Context())
	if err != nil {
		h.writeError(c, "image_registry_list", err)
		return
	}
	result := make([]imageRegistryResponse, 0, len(registries))
	for i := range registries {
		result = append(result, imageRegistryResponse{ImageRegistry: registries[i], HasCredential: registries[i].CredentialCiphertext != ""})
	}
	c.JSON(http.StatusOK, gin.H{"image_registries": result})
}

func (h pipelineHandler) createRegistry(c *gin.Context) {
	var request registryRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_image_registry", pipeline.ErrInvalidRegistry.Error())
		return
	}
	actor, _ := currentUser(c)
	registry, err := h.service.CreateRegistry(c.Request.Context(), actor.ID, toRegistryInput(request))
	if err != nil {
		h.writeError(c, "image_registry_create", err)
		return
	}
	setAuditResourceID(c, registry.ID)
	c.JSON(http.StatusCreated, gin.H{"image_registry": imageRegistryResponse{ImageRegistry: *registry, HasCredential: registry.CredentialCiphertext != ""}})
}

func (h pipelineHandler) testRegistry(c *gin.Context) {
	var request registryRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_image_registry", pipeline.ErrInvalidRegistry.Error())
		return
	}
	if err := h.service.TestRegistry(c.Request.Context(), toRegistryInput(request)); err != nil {
		h.writeError(c, "image_registry_test", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "镜像仓库登录成功"})
}

func (h pipelineHandler) listDeploymentPlans(c *gin.Context) {
	plans, err := h.service.ListDeploymentPlans(c.Request.Context())
	if err != nil {
		h.writeError(c, "deployment_plan_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deployment_plans": plans})
}

func (h pipelineHandler) createDeploymentPlan(c *gin.Context) {
	var request deploymentPlanRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建部署方案参数无效", "operation", "deployment_plan_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_deployment_plan", pipeline.ErrInvalidDeploymentPlan.Error())
		return
	}
	actor, _ := currentUser(c)
	target := toDeploymentTargetInput(*request.DeploymentTarget)
	plan, err := h.service.CreateDeploymentPlan(c.Request.Context(), actor.ID, pipeline.DeploymentPlanInput{
		Name: request.Name, Kind: request.Kind, DeploymentTarget: &target,
		Description: request.Description, Script: request.Script,
		ComposeYAML: request.ComposeYAML,
		ServiceName: request.ServiceName, DockerConfig: request.DockerConfig, TimeoutSeconds: request.TimeoutSeconds,
	})
	if err != nil {
		h.writeError(c, "deployment_plan_create", err)
		return
	}
	setAuditResourceID(c, plan.ID)
	c.JSON(http.StatusCreated, gin.H{"deployment_plan": plan})
}

func (h pipelineHandler) updateDeploymentPlan(c *gin.Context) {
	var request deploymentPlanRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新部署方案参数无效", "operation", "deployment_plan_update_bind", "request_id", requestIDFrom(c), "deployment_plan_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_deployment_plan", pipeline.ErrInvalidDeploymentPlan.Error())
		return
	}
	target := toDeploymentTargetInput(*request.DeploymentTarget)
	plan, err := h.service.UpdateDeploymentPlan(c.Request.Context(), c.Param("id"), pipeline.DeploymentPlanInput{
		Name: request.Name, Kind: request.Kind, DeploymentTarget: &target,
		Description: request.Description, Script: request.Script,
		ComposeYAML: request.ComposeYAML,
		ServiceName: request.ServiceName, DockerConfig: request.DockerConfig, TimeoutSeconds: request.TimeoutSeconds,
	})
	if err != nil {
		h.writeError(c, "deployment_plan_update", err)
		return
	}
	setAuditResourceID(c, plan.ID)
	c.JSON(http.StatusOK, gin.H{"deployment_plan": plan})
}

func (h pipelineHandler) setDeploymentPlanStatus(c *gin.Context) {
	var request runtimeStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改部署方案状态参数无效", "operation", "deployment_plan_status_bind", "request_id", requestIDFrom(c), "deployment_plan_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_deployment_plan_status", "部署方案状态格式无效")
		return
	}
	if err := h.service.SetDeploymentPlanActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeError(c, "deployment_plan_status", err)
		return
	}
	setAuditResourceID(c, c.Param("id"))
	c.Status(http.StatusNoContent)
}

func (h pipelineHandler) deleteDeploymentPlan(c *gin.Context) {
	if err := h.service.DeleteDeploymentPlan(c.Request.Context(), c.Param("id")); err != nil {
		h.writeError(c, "deployment_plan_delete", err)
		return
	}
	setAuditResourceID(c, c.Param("id"))
	c.Status(http.StatusNoContent)
}

func (h pipelineHandler) listRuns(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	runs, err := h.service.ListRuns(c.Request.Context(), limit)
	if err != nil {
		h.writeError(c, "pipeline_run_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"pipeline_runs": runs})
}

func (h pipelineHandler) writeError(c *gin.Context, operation string, err error) {
	h.logger.Warn("持续交付操作失败", "operation", operation, "request_id", requestIDFrom(c), "resource_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, pipeline.ErrReleasePlanNotFound), errors.Is(err, pipeline.ErrReleaseGroupNotFound), errors.Is(err, pipeline.ErrReleasePlanExecutionNotFound):
		writeError(c, http.StatusNotFound, "release_plan_not_found", err.Error())
	case errors.Is(err, pipeline.ErrReleasePlanNotEditable), errors.Is(err, pipeline.ErrReleasePlanDisabled),
		errors.Is(err, pipeline.ErrReleaseApplicationAssigned),
		errors.Is(err, pipeline.ErrReleasePlanExecutionExists),
		errors.Is(err, pipeline.ErrReleasePlanExecutionPlanChanged), errors.Is(err, pipeline.ErrReleasePlanExecutionWorkflowChanged),
		errors.Is(err, pipeline.ErrReleasePlanExecutionVersionChanged):
		writeError(c, http.StatusConflict, "release_plan_not_editable", err.Error())
	case errors.Is(err, pipeline.ErrReleasePlanExists), errors.Is(err, pipeline.ErrReleaseGroupExists):
		writeError(c, http.StatusConflict, "release_plan_exists", err.Error())
	case errors.Is(err, pipeline.ErrInvalidReleasePlan), errors.Is(err, pipeline.ErrInvalidReleaseGroup), errors.Is(err, pipeline.ErrReleaseGroupDependency),
		errors.Is(err, pipeline.ErrInvalidReleasePlanExecution):
		writeError(c, http.StatusBadRequest, "invalid_release_plan", err.Error())
	case errors.Is(err, pipeline.ErrReleasePlanExecutionTemporarilyFailed):
		writeError(c, http.StatusServiceUnavailable, "release_plan_execution_unavailable", err.Error())
	case errors.Is(err, pipeline.ErrRegistryLoginFailed):
		writeError(c, http.StatusUnprocessableEntity, "image_registry_login_failed", pipeline.ErrRegistryLoginFailed.Error())
	case errors.Is(err, pipeline.ErrRegistryConnectionFailed):
		writeError(c, http.StatusBadGateway, "image_registry_connection_failed", pipeline.ErrRegistryConnectionFailed.Error())
	case errors.Is(err, pipeline.ErrInvalidApplication), errors.Is(err, pipeline.ErrInvalidApplicationName), errors.Is(err, pipeline.ErrInvalidBuildPlan),
		errors.Is(err, pipeline.ErrInvalidScriptEnvironment),
		errors.Is(err, pipeline.ErrInvalidRegistry), errors.Is(err, pipeline.ErrInvalidRegistryName),
		errors.Is(err, pipeline.ErrInvalidRegistryProvider), errors.Is(err, pipeline.ErrInvalidRegistryEndpoint), errors.Is(err, pipeline.ErrRegistryProviderEndpoint),
		errors.Is(err, pipeline.ErrInsecureRegistryEndpoint), errors.Is(err, pipeline.ErrInvalidRegistryNamespace),
		errors.Is(err, pipeline.ErrInvalidRegistryUsername), errors.Is(err, pipeline.ErrInvalidRegistrySecret),
		errors.Is(err, pipeline.ErrInvalidDeploymentPlan), errors.Is(err, pipeline.ErrDeploymentPlanTargetMismatch),
		errors.Is(err, pipeline.ErrInvalidWorkflow):
		writeError(c, http.StatusBadRequest, "invalid_delivery_config", err.Error())
	case errors.Is(err, deployment.ErrEnvironmentTargetUnavailable):
		writeError(c, http.StatusBadRequest, "environment_target_unavailable", deployment.ErrEnvironmentTargetUnavailable.Error())
	case errors.Is(err, deployment.ErrEnvironmentTargetAmbiguous):
		writeError(c, http.StatusBadRequest, "environment_target_ambiguous", deployment.ErrEnvironmentTargetAmbiguous.Error())
	case errors.Is(err, deployment.ErrInvalidTarget):
		writeError(c, http.StatusBadRequest, "invalid_deployment_target", deployment.ErrInvalidTarget.Error())
	case errors.Is(err, deployment.ErrTargetExists):
		writeError(c, http.StatusConflict, "deployment_target_exists", deployment.ErrTargetExists.Error())
	case errors.Is(err, deployment.ErrTargetNotFound):
		writeError(c, http.StatusNotFound, "deployment_target_not_found", deployment.ErrTargetNotFound.Error())
	case errors.Is(err, pipeline.ErrApplicationExists), errors.Is(err, pipeline.ErrBuildPlanExists),
		errors.Is(err, pipeline.ErrRegistryExists), errors.Is(err, pipeline.ErrDeploymentPlanExists),
		errors.Is(err, pipeline.ErrWorkflowExists), errors.Is(err, pipeline.ErrWorkflowTemplateExists):
		writeError(c, http.StatusConflict, "delivery_config_exists", err.Error())
	case errors.Is(err, pipeline.ErrBuildPlanInUse):
		writeError(c, http.StatusConflict, "build_plan_in_use", err.Error())
	case errors.Is(err, pipeline.ErrDeploymentPlanInUse):
		writeError(c, http.StatusConflict, "deployment_plan_in_use", err.Error())
	case errors.Is(err, pipeline.ErrApplicationNotFound):
		writeError(c, http.StatusNotFound, "application_not_found", err.Error())
	case errors.Is(err, pipeline.ErrBuildPlanNotFound):
		writeError(c, http.StatusNotFound, "build_plan_not_found", err.Error())
	case errors.Is(err, pipeline.ErrDeploymentPlanNotFound):
		writeError(c, http.StatusNotFound, "deployment_plan_not_found", err.Error())
	case errors.Is(err, pipeline.ErrWorkflowNotFound), errors.Is(err, pipeline.ErrWorkflowTemplateNotFound):
		writeError(c, http.StatusNotFound, "workflow_not_found", err.Error())
	case errors.Is(err, pipeline.ErrPipelineRunNotFound):
		writeError(c, http.StatusNotFound, "pipeline_run_not_found", err.Error())
	case errors.Is(err, pipeline.ErrPipelineRunNotRetryable):
		writeError(c, http.StatusConflict, "pipeline_run_not_retryable", err.Error())
	case errors.Is(err, pipeline.ErrPipelineRunAwaitingReleasePlan):
		writeError(c, http.StatusConflict, "pipeline_run_managed_by_release_plan", err.Error())
	case errors.Is(err, pipeline.ErrManualCommitRequired):
		writeError(c, http.StatusBadRequest, "manual_commit_required", err.Error())
	case errors.Is(err, pipeline.ErrManualCommitNotFound):
		writeError(c, http.StatusConflict, "manual_commit_changed", err.Error())
	case errors.Is(err, pipeline.ErrManualReleaseDisabled):
		writeError(c, http.StatusConflict, "manual_release_disabled", err.Error())
	case errors.Is(err, pipeline.ErrPipelineIncomplete):
		writeError(c, http.StatusUnprocessableEntity, "pipeline_incomplete", err.Error())
	case errors.Is(err, pipeline.ErrWorkflowRevisionConflict), errors.Is(err, pipeline.ErrWorkflowTemplateRevisionConflict):
		writeError(c, http.StatusConflict, "workflow_revision_conflict", err.Error())
	case errors.Is(err, pipeline.ErrWorkflowInUse), errors.Is(err, pipeline.ErrWorkflowTemplateInUse):
		writeError(c, http.StatusConflict, "workflow_template_in_use", err.Error())
	case errors.Is(err, pipeline.ErrWorkflowNotActive), errors.Is(err, pipeline.ErrInvalidWorkflowTransition),
		errors.Is(err, pipeline.ErrWorkflowApprovalRequired), errors.Is(err, pipeline.ErrWorkflowSelfApproval):
		writeError(c, http.StatusConflict, "workflow_transition_denied", err.Error())
	case errors.Is(err, pipeline.ErrPipelineExecutionRunning):
		writeError(c, http.StatusConflict, "pipeline_execution_running", err.Error())
	case errors.Is(err, pipeline.ErrPipelineExecutionConfig):
		writeError(c, http.StatusUnprocessableEntity, "pipeline_execution_config_invalid", err.Error())
	case errors.Is(err, pipeline.ErrPipelineExecutionUnavailable):
		writeError(c, http.StatusServiceUnavailable, "pipeline_execution_unavailable", err.Error())
	default:
		writeInternalError(c)
	}
}

func toRegistryInput(request registryRequest) pipeline.RegistryInput {
	return pipeline.RegistryInput{
		Name: request.Name, Provider: request.Provider, Endpoint: request.Endpoint,
		Namespace: request.Namespace, Username: request.Username, Credential: request.Credential,
		AllowInsecureHTTP: request.AllowInsecureHTTP,
	}
}

func toApplicationInput(request applicationRequest) pipeline.ApplicationInput {
	return pipeline.ApplicationInput{
		Name: request.Name, Description: request.Description, RepositoryID: request.RepositoryID,
		PollIntervalSeconds: request.PollIntervalSeconds,
	}
}

func toWorkflowTemplateInput(request workflowTemplateRequest) pipeline.WorkflowTemplateInput {
	return pipeline.WorkflowTemplateInput{
		Description: request.Description,
		WorkflowInput: pipeline.WorkflowInput{
			SchemaVersion: request.SchemaVersion, Name: request.Name, Revision: request.Revision, Activate: request.Activate,
			Source: request.Source, Stages: request.Stages,
		},
	}
}

func toWorkflowInput(request workflowRequest) pipeline.WorkflowInput {
	return pipeline.WorkflowInput{
		SchemaVersion: request.SchemaVersion, Name: request.Name, Revision: request.Revision, Activate: request.Activate,
		Source: request.Source, Stages: request.Stages,
	}
}
