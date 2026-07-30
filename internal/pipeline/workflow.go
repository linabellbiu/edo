package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"zrt/internal/model"
	"zrt/internal/repository"
	"zrt/internal/task"
)

var (
	ErrInvalidWorkflow                = errors.New("流水线配置存在错误，请先修正代码源、阶段和任务")
	ErrWorkflowNotFound               = errors.New("应用流水线不存在")
	ErrWorkflowRevisionConflict       = errors.New("应用流水线已被其他人修改，请刷新后再保存")
	ErrWorkflowNotActive              = errors.New("应用流水线尚未启用")
	ErrInvalidWorkflowTransition      = errors.New("当前任务不能这样推进")
	ErrWorkflowApprovalRequired       = errors.New("该流水线运行需要先完成审核")
	ErrWorkflowSelfApproval           = errors.New("执行申请人不能审核自己的流水线运行")
	ErrPipelineRunNotFound            = errors.New("流水线运行不存在")
	ErrPipelineRunNotRetryable        = errors.New("只有失败的流水线运行可以重新执行")
	ErrPipelineRunAwaitingReleasePlan = errors.New("流水线运行由发布计划统一调度")
)

type WorkflowInput struct {
	SchemaVersion uint16
	Name          string
	Revision      uint64
	Activate      bool
	Source        model.WorkflowNode
	Stages        []model.WorkflowStage
}

type WorkflowIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	NodeID  string `json:"node_id,omitempty"`
	StageID string `json:"stage_id,omitempty"`
}

type WorkflowResult struct {
	Workflow *model.ReleaseWorkflow `json:"workflow"`
	Valid    bool                   `json:"valid"`
	Issues   []WorkflowIssue        `json:"issues"`
}

type workflowSnapshot struct {
	SchemaVersion     uint16                                      `json:"schema_version"`
	Source            model.WorkflowNode                          `json:"source"`
	Stages            []model.WorkflowStage                       `json:"stages"`
	BuildPlans        map[string]workflowBuildPlanSnapshot        `json:"build_plans,omitempty"`
	DeploymentPlans   map[string]workflowDeploymentPlanSnapshot   `json:"deployment_plans,omitempty"`
	DeploymentTargets map[string]workflowDeploymentTargetSnapshot `json:"deployment_targets,omitempty"`
	ApprovalEnabled   bool                                        `json:"approval_enabled"`
}

type workflowBuildPlanSnapshot struct {
	ID                   string              `json:"id"`
	Kind                 model.BuildPlanKind `json:"kind"`
	ConfigVersion        uint16              `json:"config_version"`
	Script               string              `json:"script,omitempty"`
	DockerfilePath       string              `json:"dockerfile_path,omitempty"`
	ContextPath          string              `json:"context_path"`
	WorkingDirectory     string              `json:"working_directory"`
	ArtifactPath         string              `json:"artifact_path,omitempty"`
	RuntimeImage         string              `json:"runtime_image,omitempty"`
	ImageRegistryID      string              `json:"image_registry_id,omitempty"`
	TargetStage          string              `json:"target_stage,omitempty"`
	Platform             string              `json:"platform,omitempty"`
	Pull                 bool                `json:"pull"`
	CacheEnabled         bool                `json:"cache_enabled"`
	BuildArgs            map[string]string   `json:"build_args,omitempty"`
	EnvironmentVariables map[string]string   `json:"environment_variables,omitempty"`
	TimeoutSeconds       int                 `json:"timeout_seconds"`
}

type workflowDeploymentPlanSnapshot struct {
	ID             string                      `json:"id"`
	Kind           model.DeploymentPlanKind    `json:"kind"`
	Script         string                      `json:"script,omitempty"`
	ComposeYAML    string                      `json:"compose_yaml,omitempty"`
	ServiceName    string                      `json:"service_name,omitempty"`
	DockerConfig   model.DockerContainerConfig `json:"docker_config,omitempty"`
	TimeoutSeconds int                         `json:"timeout_seconds"`
}

type workflowDeploymentTargetSnapshot struct {
	ID               string                   `json:"id"`
	Name             string                   `json:"name"`
	Platform         model.DeploymentPlatform `json:"platform"`
	EnvironmentID    string                   `json:"environment_id,omitempty"`
	HostID           string                   `json:"host_id,omitempty"`
	RuntimeID        string                   `json:"runtime_id,omitempty"`
	WorkingDirectory string                   `json:"working_directory,omitempty"`
	Namespace        string                   `json:"namespace,omitempty"`
	WorkloadName     string                   `json:"workload_name,omitempty"`
	ContainerName    string                   `json:"container_name,omitempty"`
	RolloutTimeout   int                      `json:"rollout_timeout"`
}

func (s *Service) GetWorkflow(ctx context.Context, applicationID string) (*WorkflowResult, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	workflow, err := s.ensureWorkflow(ctx, application)
	if err != nil {
		return nil, err
	}
	issues := s.validateWorkflow(ctx, application, workflow.SchemaVersion, workflow.Source, workflow.Stages)
	return &WorkflowResult{Workflow: workflow, Valid: len(issues) == 0, Issues: issues}, nil
}

func (s *Service) ValidateWorkflow(ctx context.Context, applicationID string, input WorkflowInput) (*WorkflowResult, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	if err := sanitizeWorkflowInput(&input); err != nil {
		return nil, err
	}
	issues := s.validateWorkflow(ctx, application, input.SchemaVersion, input.Source, input.Stages)
	return &WorkflowResult{Workflow: &model.ReleaseWorkflow{
		ApplicationID: application.ID, SchemaVersion: input.SchemaVersion,
		Name: input.Name, Revision: input.Revision, Source: input.Source, Stages: input.Stages,
	}, Valid: len(issues) == 0, Issues: issues}, nil
}

func (s *Service) SaveWorkflow(ctx context.Context, applicationID, actorID string, input WorkflowInput) (*WorkflowResult, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	if err := sanitizeWorkflowInput(&input); err != nil {
		return nil, err
	}
	issues := s.validateWorkflow(ctx, application, input.SchemaVersion, input.Source, input.Stages)
	if len(issues) > 0 && (input.Activate || hasWorkflowSaveBlockingIssue(issues)) {
		return &WorkflowResult{Workflow: &model.ReleaseWorkflow{
			ApplicationID: application.ID, SchemaVersion: input.SchemaVersion,
			Name: input.Name, Revision: input.Revision, Source: input.Source, Stages: input.Stages,
		}, Valid: false, Issues: issues}, ErrInvalidWorkflow
	}

	now := time.Now().UTC()
	sourceJSON, err := json.Marshal(input.Source)
	if err != nil {
		return nil, ErrInvalidWorkflow
	}
	stagesJSON, err := json.Marshal(input.Stages)
	if err != nil {
		return nil, ErrInvalidWorkflow
	}
	var saved model.ReleaseWorkflow
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.ReleaseWorkflow
		err := tx.First(&existing, "application_id = ?", application.ID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if input.Revision != 0 {
				return ErrWorkflowRevisionConflict
			}
			saved = model.ReleaseWorkflow{
				ID: uuid.NewString(), ApplicationID: application.ID, Name: input.Name,
				SchemaVersion: input.SchemaVersion, Revision: 1, IsActive: input.Activate,
				Source: input.Source, Stages: input.Stages, CreatedBy: actorID, UpdatedBy: actorID,
				CreatedAt: now, UpdatedAt: now,
			}
			return tx.Create(&saved).Error
		}
		if err != nil {
			return err
		}
		if input.Revision != existing.Revision {
			return ErrWorkflowRevisionConflict
		}
		result := tx.Model(&model.ReleaseWorkflow{}).
			Where("id = ? AND revision = ?", existing.ID, existing.Revision).
			Updates(map[string]any{
				"schema_version": input.SchemaVersion, "name": input.Name,
				"source": string(sourceJSON), "stages": string(stagesJSON), "is_active": input.Activate,
				"workflow_template_id": "", "workflow_template_revision": 0,
				"revision": existing.Revision + 1, "updated_by": actorID, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorkflowRevisionConflict
		}
		if existing.WorkflowTemplateID != "" {
			if err := tx.Model(&model.Application{}).Where("id = ?", application.ID).
				Updates(map[string]any{"workflow_template_id": "", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return tx.First(&saved, "id = ?", existing.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &WorkflowResult{Workflow: &saved, Valid: len(issues) == 0, Issues: issues}, nil
}

func (s *Service) ensureWorkflow(ctx context.Context, application *model.Application) (*model.ReleaseWorkflow, error) {
	if application.Workflow != nil && application.Workflow.ID != "" {
		return application.Workflow, nil
	}
	now := time.Now().UTC()
	workflow, err := s.newApplicationWorkflow(ctx, application, application.CreatedBy, now)
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Create(workflow).Error; err != nil {
		if !errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, fmt.Errorf("创建默认应用流水线失败: %w", err)
		}
		if err := s.db.WithContext(ctx).First(workflow, "application_id = ?", application.ID).Error; err != nil {
			return nil, fmt.Errorf("读取应用流水线失败: %w", err)
		}
	}
	return workflow, nil
}

func defaultWorkflow(application *model.Application, actorID string, now time.Time) *model.ReleaseWorkflow {
	branch := strings.TrimSpace(application.Repository.DefaultBranch)
	if branch == "" {
		branch = "main"
	}
	source := model.WorkflowNode{
		ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源",
		Config: model.WorkflowNodeConfig{Branch: branch, Events: []string{"manual", "push"}},
	}
	return &model.ReleaseWorkflow{
		ID: uuid.NewString(), ApplicationID: application.ID, Name: application.Name + "流水线",
		SchemaVersion: model.WorkflowSchemaVersion, Revision: 1, IsActive: false,
		Source: source, Stages: []model.WorkflowStage{},
		CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
}

func sanitizeWorkflowInput(input *WorkflowInput) error {
	input.Name = strings.TrimSpace(input.Name)
	if input.SchemaVersion != model.WorkflowSchemaVersion || input.Name == "" ||
		utf8.RuneCountInString(input.Name) > 128 || len(input.Stages) > 50 {
		return ErrInvalidWorkflow
	}
	if err := sanitizeWorkflowNode(&input.Source); err != nil {
		return err
	}
	taskCount := 0
	for i := range input.Stages {
		stage := &input.Stages[i]
		stage.ID = strings.TrimSpace(stage.ID)
		stage.Name = strings.TrimSpace(stage.Name)
		if stage.ID == "" || len(stage.ID) > 64 || stage.Name == "" || utf8.RuneCountInString(stage.Name) > 128 {
			return ErrInvalidWorkflow
		}
		taskCount += len(stage.Tasks)
		if taskCount > 200 {
			return ErrInvalidWorkflow
		}
		for j := range stage.Tasks {
			if err := sanitizeWorkflowNode(&stage.Tasks[j]); err != nil {
				return err
			}
		}
	}
	return nil
}

func sanitizeWorkflowNode(node *model.WorkflowNode) error {
	node.ID = strings.TrimSpace(node.ID)
	node.Name = strings.TrimSpace(node.Name)
	node.Config.Branch = strings.TrimSpace(node.Config.Branch)
	node.Config.TagPattern = strings.TrimSpace(node.Config.TagPattern)
	node.Config.PRTargetPattern = strings.TrimSpace(node.Config.PRTargetPattern)
	node.Config.PRSourcePattern = strings.TrimSpace(node.Config.PRSourcePattern)
	node.Config.BuildPlanID = strings.TrimSpace(node.Config.BuildPlanID)
	node.Config.DeploymentPlanID = strings.TrimSpace(node.Config.DeploymentPlanID)
	node.Config.RuntimeImage = strings.TrimSpace(node.Config.RuntimeImage)
	node.Config.WorkingDirectory = strings.TrimSpace(node.Config.WorkingDirectory)
	node.Config.Description = strings.TrimSpace(node.Config.Description)
	for i := range node.Config.Events {
		node.Config.Events[i] = strings.TrimSpace(node.Config.Events[i])
	}
	for i := range node.Config.PRActions {
		node.Config.PRActions[i] = strings.ToLower(strings.TrimSpace(node.Config.PRActions[i]))
	}
	if node.Type == model.WorkflowNodeTrigger && containsEvent(node.Config.Events, "pr") {
		if node.Config.PRTargetPattern == "" {
			node.Config.PRTargetPattern = node.Config.Branch
		}
		if node.Config.PRSourcePattern == "" {
			node.Config.PRSourcePattern = "*"
		}
		if len(node.Config.PRActions) == 0 {
			node.Config.PRActions = []string{"opened", "updated", "merged"}
		}
	}
	if node.Type == model.WorkflowNodeShell {
		if node.Config.RuntimeImage == "" {
			node.Config.RuntimeImage = model.DefaultRuntimeImage
		}
		if node.Config.WorkingDirectory == "" {
			node.Config.WorkingDirectory = "."
		}
		if node.Config.TimeoutSeconds == 0 {
			node.Config.TimeoutSeconds = 600
		}
	}
	if node.ID == "" || len(node.ID) > 64 || node.Name == "" || utf8.RuneCountInString(node.Name) > 128 ||
		len(node.Config.Branch) > 512 || len(node.Config.TagPattern) > 512 ||
		len(node.Config.PRTargetPattern) > 512 || len(node.Config.PRSourcePattern) > 512 ||
		len(node.Config.BuildPlanID) > 36 || len(node.Config.DeploymentPlanID) > 36 ||
		len(node.Config.RuntimeImage) > 512 || len(node.Config.WorkingDirectory) > 512 || utf8.RuneCountInString(node.Config.Description) > 500 {
		return ErrInvalidWorkflow
	}
	return nil
}

func (s *Service) validateWorkflow(
	ctx context.Context,
	application *model.Application,
	schemaVersion uint16,
	source model.WorkflowNode,
	stages []model.WorkflowStage,
) []WorkflowIssue {
	_ = application
	issues := make([]WorkflowIssue, 0)
	if schemaVersion != model.WorkflowSchemaVersion {
		return []WorkflowIssue{{Code: "unsupported_schema_version", Message: "流水线结构版本无效"}}
	}
	tasks := workflowTasks(stages)
	buildPlanIDs := make([]string, 0)
	deploymentPlanIDs := make([]string, 0)
	for i := range tasks {
		switch tasks[i].Type {
		case model.WorkflowNodeBuild:
			if tasks[i].Config.BuildPlanID != "" {
				buildPlanIDs = append(buildPlanIDs, tasks[i].Config.BuildPlanID)
			}
		case model.WorkflowNodeDeploy:
			if tasks[i].Config.DeploymentPlanID != "" {
				deploymentPlanIDs = append(deploymentPlanIDs, tasks[i].Config.DeploymentPlanID)
			}
		}
	}
	activeBuildPlans := make(map[string]model.BuildPlan, len(buildPlanIDs))
	if len(buildPlanIDs) > 0 {
		var plans []model.BuildPlan
		if err := s.db.WithContext(ctx).Where("id IN ? AND is_active = ?", buildPlanIDs, true).Find(&plans).Error; err != nil {
			if s.logger != nil {
				s.logger.Error("校验流水线构建方案失败", "operation", "pipeline_workflow_validate_build_plan", "err", err)
			}
		} else {
			for i := range plans {
				activeBuildPlans[plans[i].ID] = plans[i]
			}
		}
	}
	activeDeploymentPlans := make(map[string]model.DeploymentPlan, len(deploymentPlanIDs))
	activeTargets := make(map[string]model.DeploymentTarget)
	if len(deploymentPlanIDs) > 0 {
		var plans []model.DeploymentPlan
		if err := s.db.WithContext(ctx).Where("id IN ? AND is_active = ?", deploymentPlanIDs, true).Find(&plans).Error; err != nil {
			if s.logger != nil {
				s.logger.Error("校验流水线部署方案失败", "operation", "pipeline_workflow_validate_deployment_plan", "err", err)
			}
		} else {
			targetIDs := make([]string, 0, len(plans))
			for i := range plans {
				activeDeploymentPlans[plans[i].ID] = plans[i]
				if plans[i].DeploymentTargetID != "" {
					targetIDs = append(targetIDs, plans[i].DeploymentTargetID)
				}
			}
			if len(targetIDs) > 0 {
				var targets []model.DeploymentTarget
				if err := s.db.WithContext(ctx).Where("id IN ? AND is_active = ?", targetIDs, true).Find(&targets).Error; err == nil {
					for i := range targets {
						activeTargets[targets[i].ID] = targets[i]
					}
				}
			}
		}
	}
	ids := map[string]struct{}{source.ID: {}}
	if source.Type != model.WorkflowNodeTrigger {
		issues = append(issues, WorkflowIssue{Code: "invalid_source_type", Message: "代码源类型无效", NodeID: source.ID})
	}
	if len(source.Config.Events) == 0 {
		issues = append(issues, WorkflowIssue{Code: "missing_event", Message: "代码源至少选择一种启动方式", NodeID: source.ID})
	}
	seenEvents := make(map[string]struct{}, len(source.Config.Events))
	for _, event := range source.Config.Events {
		if event != "manual" && event != "push" && event != "pr" && event != "tag" {
			issues = append(issues, WorkflowIssue{Code: "invalid_event", Message: "代码源包含未知启动方式", NodeID: source.ID})
		}
		if _, duplicate := seenEvents[event]; duplicate {
			issues = append(issues, WorkflowIssue{Code: "duplicate_event", Message: "代码源启动方式不能重复", NodeID: source.ID})
		}
		seenEvents[event] = struct{}{}
	}
	if source.Config.Branch == "" && (containsEvent(source.Config.Events, "push") || containsEvent(source.Config.Events, "pr")) {
		issues = append(issues, WorkflowIssue{Code: "missing_branch", Message: "代码源需要填写监听分支", NodeID: source.ID})
	}
	if source.Config.TagPattern != "" {
		if _, err := path.Match(source.Config.TagPattern, "v1.0.0"); err != nil {
			issues = append(issues, WorkflowIssue{Code: "invalid_tag_pattern", Message: "Tag 匹配规则无效", NodeID: source.ID})
		}
	}
	if containsEvent(source.Config.Events, "pr") {
		if source.Config.PRTargetPattern == "" || source.Config.PRSourcePattern == "" {
			issues = append(issues, WorkflowIssue{Code: "missing_pr_filter", Message: "PR/MR 需要填写来源和目标分支匹配规则", NodeID: source.ID})
		}
		for _, pattern := range []string{source.Config.PRTargetPattern, source.Config.PRSourcePattern} {
			if _, err := path.Match(pattern, "main"); err != nil {
				issues = append(issues, WorkflowIssue{Code: "invalid_pr_pattern", Message: "PR/MR 分支匹配规则无效", NodeID: source.ID})
			}
		}
		seenActions := make(map[string]struct{}, len(source.Config.PRActions))
		if len(source.Config.PRActions) == 0 {
			issues = append(issues, WorkflowIssue{Code: "missing_pr_action", Message: "PR/MR 至少选择一种动作", NodeID: source.ID})
		}
		for _, action := range source.Config.PRActions {
			if action != "opened" && action != "updated" && action != "merged" {
				issues = append(issues, WorkflowIssue{Code: "invalid_pr_action", Message: "PR/MR 包含未知动作", NodeID: source.ID})
			}
			if _, duplicate := seenActions[action]; duplicate {
				issues = append(issues, WorkflowIssue{Code: "duplicate_pr_action", Message: "PR/MR 动作不能重复", NodeID: source.ID})
			}
			seenActions[action] = struct{}{}
		}
	}
	if len(tasks) == 0 {
		issues = append(issues, WorkflowIssue{Code: "missing_task", Message: "流水线至少需要一个任务"})
	}
	stageIDs := make(map[string]struct{}, len(stages))
	for stageIndex := range stages {
		stage := stages[stageIndex]
		if _, duplicate := stageIDs[stage.ID]; duplicate {
			issues = append(issues, WorkflowIssue{Code: "duplicate_stage", Message: "阶段标识重复", StageID: stage.ID})
		}
		stageIDs[stage.ID] = struct{}{}
		if len(stage.Tasks) == 0 {
			issues = append(issues, WorkflowIssue{Code: "empty_stage", Message: fmt.Sprintf("阶段“%s”至少需要一个任务", stage.Name), StageID: stage.ID})
		}
		for taskIndex := range stage.Tasks {
			node := stage.Tasks[taskIndex]
			if _, duplicate := ids[node.ID]; duplicate {
				issues = append(issues, WorkflowIssue{Code: "duplicate_node", Message: "任务标识重复", NodeID: node.ID, StageID: stage.ID})
				continue
			}
			ids[node.ID] = struct{}{}
			switch node.Type {
			case model.WorkflowNodeBuild:
				if _, ok := activeBuildPlans[node.Config.BuildPlanID]; !ok {
					issues = append(issues, WorkflowIssue{Code: "missing_build_plan", Message: fmt.Sprintf("构建任务“%s”需要选择可用的构建方案", node.Name), NodeID: node.ID, StageID: stage.ID})
				}
			case model.WorkflowNodeShell:
				if !validScriptEnvironmentVariables(node.Config.EnvironmentVariables) {
					issues = append(issues, WorkflowIssue{Code: "invalid_shell_task", Message: fmt.Sprintf("脚本任务“%s”的环境变量无效；CI、HOME、TMPDIR 和 ZRT 流水线元数据名称由系统保留", node.Name), NodeID: node.ID, StageID: stage.ID})
				} else if strings.TrimSpace(node.Config.Script) == "" || len(node.Config.Script) > 256*1024 || node.Config.TimeoutSeconds < 30 || node.Config.TimeoutSeconds > 7200 ||
					!validRuntimeImageReference(node.Config.RuntimeImage) {
					issues = append(issues, WorkflowIssue{Code: "invalid_shell_task", Message: fmt.Sprintf("脚本任务“%s”的命令、超时或环境变量无效", node.Name), NodeID: node.ID, StageID: stage.ID})
				}
			case model.WorkflowNodeManual, model.WorkflowNodeApproval:
			case model.WorkflowNodeDeploy:
				plan, ok := activeDeploymentPlans[node.Config.DeploymentPlanID]
				if !ok {
					issues = append(issues, WorkflowIssue{Code: "missing_deployment_plan", Message: fmt.Sprintf("部署任务“%s”需要选择可用的部署方案", node.Name), NodeID: node.ID, StageID: stage.ID})
					break
				}
				target, ok := activeTargets[plan.DeploymentTargetID]
				if !ok {
					issues = append(issues, WorkflowIssue{Code: "missing_deployment_target", Message: fmt.Sprintf("部署任务“%s”的部署方案配置不完整", node.Name), NodeID: node.ID, StageID: stage.ID})
				} else if !deploymentPlanSupportsTarget(plan.Kind, target.Platform) {
					issues = append(issues, WorkflowIssue{Code: "deployment_plan_target_mismatch", Message: fmt.Sprintf("部署任务“%s”的执行方式与目标不匹配", node.Name), NodeID: node.ID, StageID: stage.ID})
				}
			case model.WorkflowNodeTrigger:
				issues = append(issues, WorkflowIssue{Code: "trigger_in_stage", Message: "代码源不能作为阶段任务", NodeID: node.ID, StageID: stage.ID})
			default:
				issues = append(issues, WorkflowIssue{Code: "invalid_node_type", Message: "任务类型无效", NodeID: node.ID, StageID: stage.ID})
			}
		}
	}
	issues = append(issues, validateWorkflowArtifactFlow(tasks, activeBuildPlans, activeDeploymentPlans, activeTargets)...)
	return uniqueIssues(issues)
}

func validateWorkflowArtifactFlow(
	tasks []model.WorkflowNode,
	buildPlans map[string]model.BuildPlan,
	deploymentPlans map[string]model.DeploymentPlan,
	targets map[string]model.DeploymentTarget,
) []WorkflowIssue {
	issues := make([]WorkflowIssue, 0)
	var artifactPlan *model.BuildPlan
	buildTaskSeen := false
	for i := range tasks {
		next := tasks[i]
		switch next.Type {
		case model.WorkflowNodeBuild:
			buildTaskSeen = true
			plan, ok := buildPlans[next.Config.BuildPlanID]
			if !ok {
				artifactPlan = nil
			} else {
				artifactPlan = &plan
			}
		case model.WorkflowNodeDeploy:
			if artifactPlan == nil {
				// 构建任务已存在但方案无效时，前面的 missing_build_plan 已经给出准确原因，
				// 不再追加“缺少构建任务”这一条误导性的重复问题。
				if !buildTaskSeen {
					issues = append(issues, WorkflowIssue{
						Code: "missing_artifact_source", Message: fmt.Sprintf("部署任务“%s”前必须先执行构建制品任务", next.Name), NodeID: next.ID,
					})
				}
				break
			}
			deploymentPlan, planOK := deploymentPlans[next.Config.DeploymentPlanID]
			target, targetOK := targets[deploymentPlan.DeploymentTargetID]
			if !planOK || !targetOK {
				break
			}
			if target.Platform == model.DeploymentSSH && artifactPlan.Kind != model.BuildPlanScript {
				issues = append(issues, WorkflowIssue{
					Code: "artifact_kind_mismatch", Message: fmt.Sprintf("部署任务“%s”需要文件制品，请选择脚本构建方案", next.Name), NodeID: next.ID,
				})
			}
			if (target.Platform == model.DeploymentDocker || target.Platform == model.DeploymentKubernetes) && artifactPlan.Kind != model.BuildPlanDockerfile {
				issues = append(issues, WorkflowIssue{
					Code: "artifact_kind_mismatch", Message: fmt.Sprintf("部署任务“%s”需要镜像制品，请选择 Dockerfile 构建方案", next.Name), NodeID: next.ID,
				})
			}
			if target.Platform == model.DeploymentKubernetes && artifactPlan.Kind == model.BuildPlanDockerfile && artifactPlan.ImageRegistryID == "" {
				issues = append(issues, WorkflowIssue{
					Code: "kubernetes_registry_required", Message: fmt.Sprintf("部署任务“%s”面向 Kubernetes，构建方案必须选择镜像仓库", next.Name), NodeID: next.ID,
				})
			}
		}
	}
	return issues
}

func workflowTasks(stages []model.WorkflowStage) []model.WorkflowNode {
	count := 0
	for i := range stages {
		count += len(stages[i].Tasks)
	}
	result := make([]model.WorkflowNode, 0, count)
	for i := range stages {
		result = append(result, stages[i].Tasks...)
	}
	return result
}

func hasWorkflowIssue(issues []WorkflowIssue, code string) bool {
	for i := range issues {
		if issues[i].Code == code {
			return true
		}
	}
	return false
}

func hasWorkflowSaveBlockingIssue(issues []WorkflowIssue) bool {
	for _, code := range []string{
		"deployment_plan_environment_mismatch",
		"deployment_plan_target_mismatch",
	} {
		if hasWorkflowIssue(issues, code) {
			return true
		}
	}
	return false
}

func uniqueIssues(issues []WorkflowIssue) []WorkflowIssue {
	result := make([]WorkflowIssue, 0, len(issues))
	seen := make(map[string]struct{}, len(issues))
	for i := range issues {
		key := issues[i].Code + "\x00" + issues[i].StageID + "\x00" + issues[i].NodeID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, issues[i])
	}
	return result
}

func containsEvent(events []string, expected string) bool {
	for _, event := range events {
		if event == expected {
			return true
		}
	}
	return false
}

func workflowNodeSupportsManualRelease(node model.WorkflowNode) bool {
	return node.Type == model.WorkflowNodeTrigger && containsEvent(node.Config.Events, "manual")
}

func workflowHasManualReleaseSource(workflow *model.ReleaseWorkflow) bool {
	return workflow != nil && workflowNodeSupportsManualRelease(workflow.Source)
}

func workflowHasApprovalNode(stages []model.WorkflowStage) bool {
	for i := range stages {
		for j := range stages[i].Tasks {
			if stages[i].Tasks[j].Type == model.WorkflowNodeApproval {
				return true
			}
		}
	}
	return false
}

func workflowFindNode(source model.WorkflowNode, stages []model.WorkflowStage, nodeID string) (model.WorkflowNode, bool) {
	if source.ID == nodeID {
		return source, true
	}
	for i := range stages {
		for j := range stages[i].Tasks {
			if stages[i].Tasks[j].ID == nodeID {
				return stages[i].Tasks[j], true
			}
		}
	}
	return model.WorkflowNode{}, false
}

func workflowNextNode(source model.WorkflowNode, stages []model.WorkflowStage, currentID string) (model.WorkflowNode, bool, bool) {
	tasks := workflowTasks(stages)
	if currentID == source.ID {
		if len(tasks) == 0 {
			return model.WorkflowNode{}, false, true
		}
		return tasks[0], true, false
	}
	for i := range tasks {
		if tasks[i].ID != currentID {
			continue
		}
		if i+1 == len(tasks) {
			return model.WorkflowNode{}, false, true
		}
		return tasks[i+1], true, false
	}
	return model.WorkflowNode{}, false, false
}

func cloneWorkflowStages(stages []model.WorkflowStage) []model.WorkflowStage {
	data, _ := json.Marshal(stages)
	var clone []model.WorkflowStage
	_ = json.Unmarshal(data, &clone)
	if clone == nil {
		clone = []model.WorkflowStage{}
	}
	return clone
}

func workflowStageForTask(stages []model.WorkflowStage, nodeID string) (model.WorkflowStage, bool) {
	for i := range stages {
		for j := range stages[i].Tasks {
			if stages[i].Tasks[j].ID == nodeID {
				return stages[i], true
			}
		}
	}
	return model.WorkflowStage{}, false
}

func workflowSnapshotJSON(workflow *model.ReleaseWorkflow) (string, error) {
	if workflow == nil || workflow.SchemaVersion != model.WorkflowSchemaVersion {
		return "", ErrInvalidWorkflow
	}
	data, err := json.Marshal(workflowSnapshot{
		SchemaVersion: workflow.SchemaVersion, Source: workflow.Source, Stages: workflow.Stages,
		ApprovalEnabled: workflowHasApprovalNode(workflow.Stages),
	})
	if err != nil {
		return "", fmt.Errorf("保存流水线快照失败: %w", err)
	}
	return string(data), nil
}

func workflowStageName(stages []model.WorkflowStage, nodeID string) string {
	for i := range stages {
		for j := range stages[i].Tasks {
			if stages[i].Tasks[j].ID == nodeID {
				return stages[i].Name
			}
		}
	}
	return ""
}

func workflowTaskCount(stages []model.WorkflowStage) int {
	count := 0
	for i := range stages {
		count += len(stages[i].Tasks)
	}
	return count
}

func workflowHasTaskType(stages []model.WorkflowStage, taskType model.WorkflowNodeType) bool {
	for i := range stages {
		for j := range stages[i].Tasks {
			if stages[i].Tasks[j].Type == taskType {
				return true
			}
		}
	}
	return false
}

func (s *Service) newResolvedWorkflowRun(
	ctx context.Context,
	application *model.Application,
	workflow *model.ReleaseWorkflow,
	node model.WorkflowNode,
	trigger, ref, commitSHA, actorID, message string,
	now time.Time,
) (*model.PipelineRun, error) {
	return s.newStageResolvedWorkflowRun(ctx, application, workflow, node, trigger, ref, commitSHA, actorID, message, now)
}

func (s *Service) newStageResolvedWorkflowRun(
	ctx context.Context,
	application *model.Application,
	workflow *model.ReleaseWorkflow,
	node model.WorkflowNode,
	trigger, ref, commitSHA, actorID, message string,
	now time.Time,
) (*model.PipelineRun, error) {
	return s.newStageResolvedWorkflowRunWithDatabase(ctx, s.db, application, workflow, node, trigger, ref, commitSHA, actorID, message, now)
}

func (s *Service) newStageResolvedWorkflowRunWithDatabase(
	ctx context.Context,
	database *gorm.DB,
	application *model.Application,
	workflow *model.ReleaseWorkflow,
	node model.WorkflowNode,
	trigger, ref, commitSHA, actorID, message string,
	now time.Time,
) (*model.PipelineRun, error) {
	if workflow == nil {
		return nil, ErrInvalidWorkflow
	}
	if workflow.SchemaVersion != model.WorkflowSchemaVersion || node.ID != workflow.Source.ID || node.Type != model.WorkflowNodeTrigger {
		return nil, ErrInvalidWorkflow
	}
	resolved := *workflow
	resolved.Stages = cloneWorkflowStages(workflow.Stages)
	buildPlans := make(map[string]workflowBuildPlanSnapshot)
	deploymentPlans := make(map[string]workflowDeploymentPlanSnapshot)
	deploymentTargets := make(map[string]workflowDeploymentTargetSnapshot)
	resolvedBuildPlans := make(map[string]model.BuildPlan)
	resolvedDeploymentPlans := make(map[string]model.DeploymentPlan)
	resolvedDeploymentTargets := make(map[string]model.DeploymentTarget)
	for i := range resolved.Stages {
		for j := range resolved.Stages[i].Tasks {
			current := &resolved.Stages[i].Tasks[j]
			switch current.Type {
			case model.WorkflowNodeBuild:
				var plan model.BuildPlan
				if current.Config.BuildPlanID == "" || database.WithContext(ctx).
					First(&plan, "id = ? AND is_active = ?", current.Config.BuildPlanID, true).Error != nil {
					return nil, ErrInvalidWorkflow
				}
				buildPlans[current.ID] = buildPlanSnapshot(plan)
				resolvedBuildPlans[plan.ID] = plan
			case model.WorkflowNodeDeploy:
				var plan model.DeploymentPlan
				if current.Config.DeploymentPlanID == "" || database.WithContext(ctx).
					First(&plan, "id = ? AND is_active = ?", current.Config.DeploymentPlanID, true).Error != nil {
					return nil, ErrInvalidWorkflow
				}
				var target model.DeploymentTarget
				if plan.DeploymentTargetID == "" || database.WithContext(ctx).
					First(&target, "id = ? AND is_active = ?", plan.DeploymentTargetID, true).Error != nil ||
					!deploymentPlanSupportsTarget(plan.Kind, target.Platform) {
					return nil, ErrInvalidWorkflow
				}
				deploymentPlans[current.ID] = workflowDeploymentPlanSnapshot{
					ID: plan.ID, Kind: plan.Kind, Script: plan.Script,
					ComposeYAML: plan.ComposeYAML, ServiceName: plan.ServiceName,
					DockerConfig:   plan.DockerConfig,
					TimeoutSeconds: plan.TimeoutSeconds,
				}
				deploymentTargets[current.ID] = workflowDeploymentTargetSnapshot{
					ID: target.ID, Name: target.Name, Platform: target.Platform,
					EnvironmentID: target.EnvironmentID, HostID: target.HostID, RuntimeID: target.RuntimeID,
					WorkingDirectory: target.WorkingDirectory, Namespace: target.Namespace,
					WorkloadName: target.WorkloadName, ContainerName: target.ContainerName,
					RolloutTimeout: target.RolloutTimeout,
				}
				resolvedDeploymentPlans[plan.ID] = plan
				resolvedDeploymentTargets[target.ID] = target
			}
		}
	}
	// 方案是独立可编辑资源，流水线启用后其类型或镜像仓库仍可能改变。
	// 每次创建运行快照前都重新校验完整制品链，避免先执行昂贵构建，
	// 到部署节点才发现文件/镜像类型已不匹配。
	if issues := validateWorkflowArtifactFlow(
		workflowTasks(resolved.Stages), resolvedBuildPlans, resolvedDeploymentPlans, resolvedDeploymentTargets,
	); len(issues) > 0 {
		if s.logger != nil {
			s.logger.Error("创建流水线运行前制品链校验失败", "operation", "pipeline_run_artifact_flow_validate",
				"application_id", application.ID, "workflow_id", workflow.ID, "issue_code", issues[0].Code,
				"err", ErrInvalidWorkflow)
		}
		return nil, ErrInvalidWorkflow
	}
	run, err := newWorkflowRun(application, &resolved, node, trigger, ref, commitSHA, actorID, message, now)
	if err != nil {
		return nil, err
	}
	snapshot, err := json.Marshal(workflowSnapshot{
		SchemaVersion: resolved.SchemaVersion, Source: resolved.Source, Stages: resolved.Stages,
		BuildPlans:      buildPlans,
		DeploymentPlans: deploymentPlans, DeploymentTargets: deploymentTargets,
		ApprovalEnabled: workflowHasApprovalNode(resolved.Stages),
	})
	if err != nil {
		return nil, fmt.Errorf("保存流水线快照失败: %w", err)
	}
	run.WorkflowSnapshot = string(snapshot)
	return run, nil
}

func buildPlanSnapshot(plan model.BuildPlan) workflowBuildPlanSnapshot {
	return workflowBuildPlanSnapshot{
		ID: plan.ID, Kind: plan.Kind, ConfigVersion: plan.ConfigVersion,
		Script: plan.Script, DockerfilePath: plan.DockerfilePath, ContextPath: plan.ContextPath,
		WorkingDirectory: plan.WorkingDirectory, ArtifactPath: plan.ArtifactPath, RuntimeImage: plan.RuntimeImage,
		ImageRegistryID: plan.ImageRegistryID, TargetStage: plan.TargetStage, Platform: plan.Platform,
		Pull: plan.Pull, CacheEnabled: plan.CacheEnabled, BuildArgs: plan.BuildArgs,
		EnvironmentVariables: plan.EnvironmentVariables, TimeoutSeconds: plan.TimeoutSeconds,
	}
}

func newWorkflowRun(application *model.Application, workflow *model.ReleaseWorkflow, node model.WorkflowNode, trigger, ref, commitSHA, actorID, message string, now time.Time) (*model.PipelineRun, error) {
	approvalRequired := workflowHasApprovalNode(workflow.Stages)
	snapshot, err := workflowSnapshotJSON(workflow)
	if err != nil {
		return nil, err
	}
	return &model.PipelineRun{
		ID: uuid.NewString(), ApplicationID: application.ID, Trigger: trigger,
		Ref: ref, CommitSHA: commitSHA, Status: model.PipelineRunDetected,
		Stage: string(node.Type), Environment: "",
		WorkflowID: workflow.ID, WorkflowRevision: workflow.Revision,
		CurrentNodeID: node.ID, WorkflowSnapshot: snapshot,
		ApprovalRequired: approvalRequired,
		Message:          message, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func matchingWorkflowTriggers(workflow *model.ReleaseWorkflow, event, ref, sourceBranch, targetBranch, action string) []model.WorkflowNode {
	if workflow == nil || !workflow.IsActive {
		return nil
	}
	node := workflow.Source
	if node.Type != model.WorkflowNodeTrigger || !containsEvent(node.Config.Events, event) {
		return nil
	}
	if event == "pr" {
		action = normalizePullRequestAction(action)
		if action == "" || !containsEvent(node.Config.PRActions, action) ||
			!matchWorkflowPattern(node.Config.PRSourcePattern, sourceBranch) ||
			!matchWorkflowPattern(node.Config.PRTargetPattern, targetBranch) {
			return nil
		}
		return []model.WorkflowNode{node}
	}
	if event == "tag" {
		tag := strings.TrimPrefix(ref, "refs/tags/")
		if matchTag(node.Config.TagPattern, tag) {
			return []model.WorkflowNode{node}
		}
		return nil
	}
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if matchWorkflowPattern(node.Config.Branch, branch) {
		return []model.WorkflowNode{node}
	}
	return nil
}

func applicationPollSources(application *model.Application) []model.WorkflowNode {
	if application.Workflow == nil || !application.Workflow.IsActive {
		return nil
	}
	node := application.Workflow.Source
	// Push、PR 和 Tag 均由远端状态发现；Webhook 只提供可选的低延迟通知。
	if node.Type == model.WorkflowNodeTrigger &&
		(containsEvent(node.Config.Events, "push") || containsEvent(node.Config.Events, "pr") || containsEvent(node.Config.Events, "tag")) {
		return []model.WorkflowNode{node}
	}
	return nil
}

func applicationEventSources(application *model.Application, event, ref, sourceBranch, targetBranch, action string) []model.WorkflowNode {
	if application.Workflow == nil || !application.Workflow.IsActive {
		return nil
	}
	return matchingWorkflowTriggers(application.Workflow, event, ref, sourceBranch, targetBranch, action)
}

func matchWorkflowPattern(pattern, value string) bool {
	// Git 分支和 Tag 名中的斜杠是名称的一部分。将它替换为普通字符后再匹配，
	// 让用户填写的 `*` 可以直观地匹配 `feature/payment` 等完整引用名。
	const slashPlaceholder = "\x00"
	pattern = strings.ReplaceAll(strings.TrimSpace(pattern), "/", slashPlaceholder)
	value = strings.ReplaceAll(strings.TrimSpace(value), "/", slashPlaceholder)
	matched, err := path.Match(pattern, value)
	return err == nil && matched
}

func normalizePullRequestAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "open", "opened", "reopen", "reopened":
		return "opened"
	case "update", "updated", "synchronize", "synchronized":
		return "updated"
	case "merge", "merged":
		return "merged"
	default:
		return ""
	}
}

func workflowEventName(repositoryEvent string) string {
	return map[string]string{"branch_push": "push", "pull_request": "pr", "tag_push": "tag"}[repositoryEvent]
}

type workflowPollCandidate struct {
	Event        string
	Ref          string
	Commit       string
	SourceBranch string
	TargetBranch string
	Action       string
}

func workflowPollCandidates(source model.WorkflowNode, refs repository.RefResult) []workflowPollCandidate {
	result := make([]workflowPollCandidate, 0)
	if containsEvent(source.Config.Events, "push") {
		for i := range refs.Branches {
			if matchWorkflowPattern(source.Config.Branch, refs.Branches[i].Name) {
				result = append(result, workflowPollCandidate{
					Event: "push", Ref: "refs/heads/" + refs.Branches[i].Name, Commit: refs.Branches[i].SHA,
				})
			}
		}
	}
	if containsEvent(source.Config.Events, "pr") {
		for i := range refs.PullRequests {
			pullRequest := refs.PullRequests[i]
			sourceBranch := strings.TrimSpace(pullRequest.SourceBranch)
			targetBranch := strings.TrimSpace(pullRequest.TargetBranch)
			if sourceBranch == "" || targetBranch == "" {
				continue
			}
			if matchWorkflowPattern(source.Config.PRTargetPattern, targetBranch) &&
				matchWorkflowPattern(source.Config.PRSourcePattern, sourceBranch) {
				result = append(result, workflowPollCandidate{
					Event: "pr", Ref: pullRequest.Ref, Commit: pullRequest.SHA,
					SourceBranch: sourceBranch, TargetBranch: targetBranch, Action: pullRequest.Action,
				})
			}
		}
	}
	if containsEvent(source.Config.Events, "tag") {
		for i := range refs.Tags {
			if matchTag(source.Config.TagPattern, refs.Tags[i].Name) {
				result = append(result, workflowPollCandidate{
					Event: "tag", Ref: "refs/tags/" + refs.Tags[i].Name, Commit: refs.Tags[i].SHA,
				})
			}
		}
	}
	return result
}

func polledRunTrigger(event string) string {
	return map[string]string{"push": "poll_push", "pr": "poll_pr", "tag": "poll_tag"}[event]
}

func (s *Service) runFromSource(ctx context.Context, application *model.Application, source model.WorkflowNode, trigger, ref, commitSHA, actorID, message string, now time.Time) (*model.PipelineRun, error) {
	if application.Workflow == nil || !application.Workflow.IsActive {
		return nil, ErrWorkflowNotActive
	}
	return s.newResolvedWorkflowRun(ctx, application, application.Workflow, source, trigger, ref, commitSHA, actorID, message, now)
}

func (s *Service) runFromSourceWithDatabase(ctx context.Context, database *gorm.DB, application *model.Application, source model.WorkflowNode, trigger, ref, commitSHA, actorID, message string, now time.Time) (*model.PipelineRun, error) {
	if application.Workflow == nil || !application.Workflow.IsActive {
		return nil, ErrWorkflowNotActive
	}
	return s.newStageResolvedWorkflowRunWithDatabase(ctx, database, application, application.Workflow, source, trigger, ref, commitSHA, actorID, message, now)
}

func parseWorkflowSnapshot(run *model.PipelineRun) (*workflowSnapshot, error) {
	var snapshot workflowSnapshot
	if run.WorkflowSnapshot == "" || json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot) != nil ||
		snapshot.SchemaVersion != model.WorkflowSchemaVersion || snapshot.Source.Type != model.WorkflowNodeTrigger ||
		snapshot.Source.ID == "" || len(workflowTasks(snapshot.Stages)) == 0 {
		return nil, ErrInvalidWorkflowTransition
	}
	snapshot.ApprovalEnabled = workflowHasApprovalNode(snapshot.Stages)
	return &snapshot, nil
}

// RetryRun 保留失败记录，并使用相同代码版本和应用当前有效配置创建一条新的可审计运行。
func (s *Service) RetryRun(ctx context.Context, runID, actorID string) (*model.PipelineRun, error) {
	var failed model.PipelineRun
	if err := s.db.WithContext(ctx).First(&failed, "id = ?", runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPipelineRunNotFound
		}
		return nil, fmt.Errorf("读取待重新执行的流水线运行失败: %w", err)
	}
	if failed.Status != model.PipelineRunFailed {
		return nil, ErrPipelineRunNotRetryable
	}
	if failed.Ref == "" || failed.CommitSHA == "" {
		return nil, ErrManualCommitRequired
	}
	application, err := s.FindApplication(ctx, failed.ApplicationID)
	if err != nil {
		return nil, err
	}
	if !application.IsActive || !pipelineExecutionConfigured(application) {
		return nil, fmt.Errorf("%w：%s", ErrPipelineIncomplete, pipelineExecutionIncompleteMessage(application))
	}
	if application.Workflow == nil || !application.Workflow.IsActive {
		return nil, ErrWorkflowNotActive
	}
	source := retryWorkflowSource(application.Workflow, &failed)
	if source == nil {
		return nil, ErrInvalidWorkflow
	}
	now := time.Now().UTC()
	run, err := s.newResolvedWorkflowRun(
		ctx,
		application, application.Workflow, *source, "retry", failed.Ref, failed.CommitSHA,
		actorID, "重新执行失败运行 "+failed.ID, now,
	)
	if err != nil {
		return nil, err
	}
	run.RetryOfID = failed.ID
	run.CommitMessage = failed.CommitMessage
	run.TriggerAction = failed.TriggerAction
	run.SourceBranch = failed.SourceBranch
	run.TargetBranch = failed.TargetBranch
	components, err := pipelineRunRepositories(application, run.ID, run.Ref, run.CommitSHA, now)
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		return tx.Create(&components).Error
	}); err != nil {
		return nil, fmt.Errorf("创建重新执行的流水线运行失败: %w", err)
	}
	run.Repositories = components
	return s.AdvanceRun(ctx, run.ID, actorID, "")
}

func (s *Service) AdvanceRun(ctx context.Context, runID, actorID, targetNodeID string) (*model.PipelineRun, error) {
	s.pipelineAdvanceMu.Lock()
	defer s.pipelineAdvanceMu.Unlock()
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "id = ?", runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidWorkflowTransition
		}
		return nil, err
	}
	if err := s.ensureReleasePlanRunAdvanceAllowed(ctx, &run); err != nil {
		return nil, err
	}
	return s.advanceLoadedRun(ctx, &run, actorID, targetNodeID)
}

// advanceRunIfCurrent 只在运行仍处于调用方观察到的节点和状态时推进。
// 调和器借此避免在用户已经进入人工放行节点后，再依据过期状态多推进一步。
func (s *Service) advanceRunIfCurrent(
	ctx context.Context,
	expected model.PipelineRun,
	actorID, targetNodeID string,
) (*model.PipelineRun, bool, error) {
	s.pipelineAdvanceMu.Lock()
	defer s.pipelineAdvanceMu.Unlock()
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "id = ?", expected.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrInvalidWorkflowTransition
		}
		return nil, false, err
	}
	if run.Status != expected.Status || run.Stage != expected.Stage || run.CurrentNodeID != expected.CurrentNodeID ||
		!run.UpdatedAt.Equal(expected.UpdatedAt) {
		return &run, false, nil
	}
	if err := s.ensureReleasePlanRunAdvanceAllowed(ctx, &run); err != nil {
		return nil, false, err
	}
	advanced, err := s.advanceLoadedRun(ctx, &run, actorID, targetNodeID)
	return advanced, true, err
}

func (s *Service) ensureReleasePlanRunAdvanceAllowed(ctx context.Context, run *model.PipelineRun) error {
	if run.ReleasePlanExecutionID == "" && run.ReleasePlanExecutionItemID == "" {
		return nil
	}
	if run.ReleasePlanExecutionID == "" || run.ReleasePlanExecutionItemID == "" {
		return ErrPipelineRunAwaitingReleasePlan
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.ReleasePlanExecutionItem{}).
		Where(
			"id = ? AND release_plan_execution_id = ? AND pipeline_run_id = ? AND status = ?",
			run.ReleasePlanExecutionItemID, run.ReleasePlanExecutionID, run.ID, model.ReleasePlanExecutionItemRunning,
		).Count(&count).Error; err != nil {
		if s.logger != nil {
			s.logger.Error("检查发布计划流水线调度状态失败", "operation", "pipeline_advance_release_plan_guard", "pipeline_run_id", run.ID, "err", err)
		}
		return ErrPipelineRunAwaitingReleasePlan
	}
	if count != 1 {
		return ErrPipelineRunAwaitingReleasePlan
	}
	return nil
}

func (s *Service) advanceLoadedRun(ctx context.Context, run *model.PipelineRun, actorID, targetNodeID string) (*model.PipelineRun, error) {
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		return nil, err
	}
	current, ok := workflowFindNode(snapshot.Source, snapshot.Stages, run.CurrentNodeID)
	if !ok {
		return nil, ErrInvalidWorkflowTransition
	}
	if current.Type == model.WorkflowNodeApproval && snapshot.ApprovalEnabled {
		var approvalCount int64
		if err := s.db.WithContext(ctx).Model(&model.PipelineRunApproval{}).
			Where("pipeline_run_id = ? AND node_id = ?", run.ID, current.ID).Count(&approvalCount).Error; err != nil {
			return nil, err
		}
		if approvalCount == 0 {
			return nil, ErrWorkflowApprovalRequired
		}
	}
	if (current.Type == model.WorkflowNodeBuild || current.Type == model.WorkflowNodeShell) && run.Stage != "task_succeeded" {
		if run.Status == model.PipelineRunRunning && run.ExecutionJobID != "" {
			return nil, ErrPipelineExecutionRunning
		}
		return s.enqueueBuildExecution(ctx, run, current)
	}
	if current.Type == model.WorkflowNodeDeploy && run.Stage != "deploy_succeeded" {
		if run.Status == model.PipelineRunRunning {
			return nil, ErrPipelineExecutionRunning
		}
		return s.enqueueDeployExecution(ctx, run, current)
	}
	target, hasNext, terminal := workflowNextNode(snapshot.Source, snapshot.Stages, current.ID)
	if !hasNext {
		if !terminal {
			return nil, ErrInvalidWorkflowTransition
		}
		if (current.Type == model.WorkflowNodeBuild || current.Type == model.WorkflowNodeShell) && run.Stage != "task_succeeded" {
			return nil, ErrInvalidWorkflowTransition
		}
		if current.Type == model.WorkflowNodeDeploy && run.Stage != "deploy_succeeded" {
			return nil, ErrInvalidWorkflowTransition
		}
		now := time.Now().UTC()
		message := "当前任务：" + current.Name + "；状态：已完成"
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(run).Updates(map[string]any{
				"status": model.PipelineRunSucceeded, "stage": "completed",
				"message": message, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			return tx.Model(&model.PipelineRunRepository{}).Where("pipeline_run_id = ?", run.ID).
				Updates(map[string]any{"status": model.PipelineRunRepositorySucceeded, "updated_at": now}).Error
		}); err != nil {
			return nil, err
		}
		run.Status, run.Stage, run.Message, run.UpdatedAt = model.PipelineRunSucceeded, "completed", message, now
		return run, nil
	}
	if targetNodeID != "" && targetNodeID != target.ID {
		return nil, ErrInvalidWorkflowTransition
	}
	status, stage, message := model.PipelineRunRunning, string(target.Type), "已进入“"+target.Name+"”"
	if target.Type == model.WorkflowNodeApproval && snapshot.ApprovalEnabled {
		status, message = model.PipelineRunAwaitingApproval, "等待其他成员审核"
	}
	if target.Type == model.WorkflowNodeDeploy {
		run.CurrentNodeID, run.Environment = target.ID, ""
		return s.enqueueDeployExecution(ctx, run, target)
	}
	if target.Type == model.WorkflowNodeBuild || target.Type == model.WorkflowNodeShell {
		run.CurrentNodeID, run.Environment = target.ID, ""
		return s.enqueueBuildExecution(ctx, run, target)
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"current_node_id": target.ID, "environment": "",
		"status": status, "stage": stage, "message": message, "updated_at": now,
	}
	if err := s.db.WithContext(ctx).Model(run).Updates(updates).Error; err != nil {
		return nil, err
	}
	run.CurrentNodeID, run.Environment, run.Status = target.ID, "", status
	run.Stage, run.Message, run.UpdatedAt = stage, message, now
	_ = actorID
	return run, nil
}

func (s *Service) enqueueBuildExecution(ctx context.Context, run *model.PipelineRun, node model.WorkflowNode) (*model.PipelineRun, error) {
	if node.Type != model.WorkflowNodeBuild && node.Type != model.WorkflowNodeShell {
		return nil, ErrInvalidWorkflowTransition
	}
	// Shell 和脚本构建可能产生外部副作用，同一消息最多执行一次。Dockerfile 构建以固定
	// Commit 和不可变制品登记为幂等边界，才允许有限重试。
	maxAttempts, idempotent := 1, false
	if node.Type == model.WorkflowNodeBuild {
		snapshot, err := parseWorkflowSnapshot(run)
		if err != nil {
			return nil, err
		}
		plan, ok := snapshot.BuildPlans[node.ID]
		if !ok || plan.ID != node.Config.BuildPlanID {
			return nil, ErrPipelineExecutionConfig
		}
		// Dockerfile 构建以固定 Commit 和不可变制品登记为幂等边界；任意脚本默认不自动重试。
		if plan.Kind == model.BuildPlanDockerfile {
			maxAttempts, idempotent = 4, true
		}
	}
	now := time.Now().UTC()
	var jobID string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, err := task.NewService(tx, 4).Create(ctx, task.CreateInput{
			Kind: "pipeline.build", Subject: "zrt.task.pipeline.build",
			Payload:        BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID},
			IdempotencyKey: "pipeline:" + run.ID + ":task:" + node.ID,
			MaxAttempts:    maxAttempts, Idempotent: idempotent,
		})
		if err != nil {
			return err
		}
		jobID = job.ID
		result := tx.Model(&model.PipelineRun{}).Where("id = ? AND status <> ?", run.ID, model.PipelineRunSucceeded).
			Updates(map[string]any{
				"current_node_id": node.ID, "status": model.PipelineRunRunning, "stage": "queued",
				"execution_job_id": job.ID, "message": "当前任务：" + node.Name + "；状态：等待执行", "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvalidWorkflowTransition
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	run.CurrentNodeID, run.Status, run.Stage = node.ID, model.PipelineRunRunning, "queued"
	run.ExecutionJobID, run.Message, run.UpdatedAt = jobID, "当前任务："+node.Name+"；状态：等待执行", now
	return run, nil
}

func (s *Service) enqueueDeployExecution(ctx context.Context, run *model.PipelineRun, node model.WorkflowNode) (*model.PipelineRun, error) {
	snapshot, snapshotErr := parseWorkflowSnapshot(run)
	hasPlan, hasTarget := false, false
	if snapshotErr == nil {
		_, hasPlan = snapshot.DeploymentPlans[node.ID]
		_, hasTarget = snapshot.DeploymentTargets[node.ID]
	}
	if snapshotErr != nil || node.Config.DeploymentPlanID == "" || !hasPlan || !hasTarget {
		message := "部署任务“" + node.Name + "”的部署方案快照不完整，流水线没有执行"
		if err := s.failCurrentExecution(ctx, run.ID, message, ErrPipelineExecutionConfig); err != nil && !errors.Is(err, ErrPipelineExecutionConfig) {
			return nil, err
		}
		run.Status, run.Stage, run.Message = model.PipelineRunFailed, "failed", message
		run.UpdatedAt = time.Now().UTC()
		return run, nil
	}
	now := time.Now().UTC()
	var jobID string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, err := task.NewService(tx, 1).Create(ctx, task.CreateInput{
			Kind: "pipeline.deploy", Subject: "zrt.task.pipeline.deploy",
			Payload:        DeployTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID},
			IdempotencyKey: "pipeline:" + run.ID + ":deploy:" + node.ID,
			MaxAttempts:    1, Idempotent: false,
		})
		if err != nil {
			return err
		}
		jobID = job.ID
		result := tx.Model(&model.PipelineRun{}).Where("id = ? AND status <> ?", run.ID, model.PipelineRunSucceeded).
			Updates(map[string]any{
				"current_node_id": node.ID, "environment": "",
				"status": model.PipelineRunRunning, "stage": "queued",
				"execution_job_id": job.ID, "deployment_id": "", "image": "",
				"message": "当前任务：" + node.Name + "；状态：等待执行", "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvalidWorkflowTransition
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	run.CurrentNodeID, run.Environment = node.ID, ""
	run.Status, run.Stage = model.PipelineRunRunning, "queued"
	run.ExecutionJobID, run.DeploymentID, run.Image = jobID, "", ""
	run.Message, run.UpdatedAt = "当前任务："+node.Name+"；状态：等待执行", now
	return run, nil
}

func (s *Service) ApproveRun(ctx context.Context, runID, actorID string) (*model.PipelineRun, error) {
	var run model.PipelineRun
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&run, "id = ?", runID).Error; err != nil {
			return ErrInvalidWorkflowTransition
		}
		snapshot, err := parseWorkflowSnapshot(&run)
		if err != nil {
			return err
		}
		current, found := workflowFindNode(snapshot.Source, snapshot.Stages, run.CurrentNodeID)
		if !found || current.Type != model.WorkflowNodeApproval || !snapshot.ApprovalEnabled || run.Status != model.PipelineRunAwaitingApproval {
			return ErrInvalidWorkflowTransition
		}
		if run.CreatedBy != "" && run.CreatedBy == actorID {
			return ErrWorkflowSelfApproval
		}
		now := time.Now().UTC()
		approval := model.PipelineRunApproval{
			ID: uuid.NewString(), PipelineRunID: run.ID, NodeID: current.ID,
			RequestedBy: run.CreatedBy, ApprovedBy: actorID, ApprovedAt: now,
		}
		if err := tx.Create(&approval).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return ErrInvalidWorkflowTransition
			}
			return err
		}
		result := tx.Model(&model.PipelineRun{}).
			Where("id = ? AND status = ?", run.ID, model.PipelineRunAwaitingApproval).
			Updates(map[string]any{
				"status": model.PipelineRunRunning, "approved_by": actorID, "approved_at": now,
				"message": "审核通过", "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvalidWorkflowTransition
		}
		run.Status, run.ApprovedBy, run.ApprovedAt = model.PipelineRunRunning, &actorID, &now
		run.Message, run.UpdatedAt = "审核通过", now
		return nil
	})
	if err != nil {
		return nil, err
	}
	advanceContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	advanced, err := s.AdvanceRun(advanceContext, run.ID, actorID, "")
	if err == nil {
		return advanced, nil
	}
	cause := fmt.Errorf("审核任务 %s 通过后自动推进失败: %w", run.CurrentNodeID, err)
	return nil, s.failExecution(advanceContext, failureStateForRun(run), "审核通过后未能进入下一任务", cause)
}
