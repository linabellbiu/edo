package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/pipeline"
	"go.yaml.in/yaml/v3"
	"gorm.io/gorm"
)

type operationInput struct {
	Kind          string                           `json:"kind"`
	Workflow      *pipeline.AssistantWorkflowInput `json:"workflow,omitempty"`
	Run           *pipeline.AssistantRunInput      `json:"run,omitempty"`
	WorkflowID    string                           `json:"workflow_id,omitempty"`
	ApplicationID string                           `json:"application_id,omitempty"`
}

type operationSnapshot struct {
	Input           operationInput   `json:"input"`
	Fingerprint     string           `json:"fingerprint"`
	Preview         OperationPreview `json:"preview"`
	ReadPermissions []string         `json:"read_permissions"`
}

func operationKind(kind string) bool {
	return kind == "workflow_create" || kind == "workflow_activate" || kind == "pipeline_deploy"
}

func operationPermissions(input operationInput) []string {
	permissions := []string{"assistant.execute", "application.read"}
	switch input.Kind {
	case "workflow_create":
		permissions = append(permissions, "workflow.create", "repository.read", "build_plan.read", "deployment_plan.read")
		if input.Workflow != nil {
			if input.Workflow.ApplicationID == "" {
				permissions = append(permissions, "application.create")
			}
			if input.Workflow.NewBuild != nil {
				permissions = append(permissions, "build_plan.create", "image_registry.read")
			}
			if input.Workflow.NewDeployment != nil {
				permissions = append(permissions, "deployment_plan.create", "cluster.read")
			}
		}
	case "workflow_activate":
		permissions = append(permissions, "workflow.read", "workflow.update", "build_plan.read", "deployment_plan.read")
	case "pipeline_deploy":
		permissions = append(permissions, "workflow.read", "pipeline_run.read", "pipeline_run.execute", "deployment_plan.read")
	}
	return permissions
}

func planYAML(input operationInput) string {
	// 先按JSON字段投影，避免YAML编码器使用Go字段名或带出内部对象。
	data, _ := json.Marshal(input)
	var value any
	_ = json.Unmarshal(data, &value)
	encoded, _ := yaml.Marshal(value)
	return string(encoded)
}

func (s *Service) operationPlan(ctx context.Context, user *model.User, input operationInput) (operationSnapshot, string, error) {
	plan := operationSnapshot{Input: input, Preview: OperationPreview{YAML: planYAML(input), Changes: []string{}, Warnings: []string{}, Checks: []string{"当前用户及项目权限", "资源归属与配置", "确认时重新检查，5分钟内有效"}}}
	name := ""
	for _, permission := range operationPermissions(input) {
		if _, err := s.authorize(ctx, user, permission); err != nil {
			return plan, name, err
		}
	}
	for _, permission := range operationPermissions(input) {
		if strings.HasSuffix(permission, ".read") {
			plan.ReadPermissions = append(plan.ReadPermissions, permission)
		}
	}
	if s.deps.Pipeline == nil {
		return plan, name, s.failure(ctx, "assistant_operation_unavailable", ErrUnavailable, ErrUnavailable)
	}
	q := (&assistantTool{service: s}).diagnosticQuery(ctx)
	switch input.Kind {
	case "workflow_create":
		if input.Workflow == nil {
			return plan, name, s.failure(ctx, "assistant_workflow_input", ErrInvalidInput, ErrInvalidInput)
		}
		preview, err := s.deps.Pipeline.PreviewAssistantWorkflow(ctx, user.ID, *input.Workflow)
		if err != nil {
			return plan, name, s.operationFailure(ctx, "assistant_workflow_preview", err)
		}
		plan.Fingerprint = preview.Fingerprint
		name = preview.ApplicationName
		plan.Preview.Changes = append(plan.Preview.Changes, "应用："+name, "流水线："+preview.Name, "步骤："+strings.Join(preview.Steps, " → "))
		for _, resource := range preview.Resources {
			plan.Preview.Changes = append(plan.Preview.Changes, safeText(redact(resource), 1000))
		}
		plan.Preview.Warnings = append(plan.Preview.Warnings, "只创建草稿，不启用、不构建、不部署；启用和执行需要另行确认")
		for _, issue := range preview.Issues {
			plan.Preview.Warnings = append(plan.Preview.Warnings, safeText(redact(issue.Message), 300))
		}
	case "pipeline_deploy":
		if input.Run == nil {
			return plan, name, s.failure(ctx, "assistant_deploy_input", ErrInvalidInput, ErrInvalidInput)
		}
		var err error
		plan.Fingerprint, err = s.deps.Pipeline.PreviewAssistantRun(ctx, user.ID, *input.Run)
		if err != nil {
			return plan, name, s.operationFailure(ctx, "assistant_deploy_preview", err)
		}
		var workflow model.ReleaseWorkflow
		if err := q.First(&workflow, &model.ReleaseWorkflow{ID: input.Run.WorkflowID, ApplicationID: input.Run.ApplicationID}).Error; err != nil {
			return plan, name, s.failure(ctx, "assistant_deploy_workflow", err, ErrNotFound)
		}
		name = workflow.Name
		var app model.Application
		if err := q.Session(&gorm.Session{}).Select("ID", "name").First(&app, &model.Application{ID: input.Run.ApplicationID}).Error; err != nil {
			return plan, name, s.failure(ctx, "assistant_deploy_application", err, ErrNotFound)
		}
		plan.Preview.Changes = append(plan.Preview.Changes, "应用："+app.Name, "执行流水线："+workflow.Name)
		if input.Run.ArtifactID != "" {
			var artifact model.Artifact
			if err := q.Session(&gorm.Session{}).Select("ID", "name", "digest").First(&artifact, &model.Artifact{ID: input.Run.ArtifactID}).Error; err != nil {
				return plan, name, s.failure(ctx, "assistant_deploy_artifact", err, ErrNotFound)
			}
			plan.Preview.Changes = append(plan.Preview.Changes, "已有制品："+artifact.Name, "制品Digest："+artifact.Digest)
		} else {
			plan.Preview.Changes = append(plan.Preview.Changes, "引用："+input.Run.Ref, "提交："+input.Run.Commit)
		}
		plan.Preview.Warnings = append(plan.Preview.Warnings, "将按已保存的流水线执行测试、构建、审核和部署，不跳过审核；受理不代表部署成功")
		if err := s.describeTargets(ctx, q, workflow.Stages, &plan.Preview); err != nil {
			return plan, name, err
		}
	case "workflow_activate":
		if input.WorkflowID == "" || input.ApplicationID == "" {
			return plan, name, s.failure(ctx, "assistant_activate_input", ErrInvalidInput, ErrInvalidInput)
		}
		result, err := s.deps.Pipeline.GetApplicationWorkflow(ctx, input.ApplicationID, input.WorkflowID)
		if err != nil {
			return plan, name, s.operationFailure(ctx, "assistant_activate_read", err)
		}
		workflow := result.Workflow
		if workflow.IsActive {
			return plan, name, s.failure(ctx, "assistant_activate_state", ErrActionState, ErrActionState)
		}
		plan.Fingerprint, err = s.deps.Pipeline.PreviewAssistantActivation(ctx, input.ApplicationID, input.WorkflowID)
		if err != nil {
			return plan, name, s.operationFailure(ctx, "assistant_activate_validation", err)
		}
		name = workflow.Name
		if err := s.describeTargets(ctx, q, workflow.Stages, &plan.Preview); err != nil {
			return plan, name, err
		}
		plan.Preview.Changes = append(plan.Preview.Changes, "启用流水线："+name, "触发方式："+strings.Join(workflow.Source.Config.Events, "、"))
		plan.Preview.Warnings = append(plan.Preview.Warnings, "启用后，匹配的代码事件可能自动运行已保存的任务；本次确认本身不发起发布")
	default:
		return plan, name, s.failure(ctx, "assistant_operation_whitelist", ErrForbidden, ErrForbidden)
	}
	return plan, name, nil
}

func (s *Service) describeTargets(ctx context.Context, q *gorm.DB, stages []model.WorkflowStage, preview *OperationPreview) error {
	for _, stage := range stages {
		for _, node := range stage.Tasks {
			if node.Type != model.WorkflowNodeDeploy {
				continue
			}
			var plan model.DeploymentPlan
			if err := q.Session(&gorm.Session{}).First(&plan, &model.DeploymentPlan{ID: node.Config.DeploymentPlanID}).Error; err != nil {
				return s.failure(ctx, "assistant_target_plan", err, ErrNotFound)
			}
			var target model.DeploymentTarget
			if err := q.Session(&gorm.Session{}).First(&target, &model.DeploymentTarget{ID: plan.DeploymentTargetID}).Error; err != nil {
				return s.failure(ctx, "assistant_target_binding", err, ErrNotFound)
			}
			preview.Changes = append(preview.Changes, fmt.Sprintf("部署方案：%s；运行位置：%s；命名空间：%s；工作负载：%s；容器：%s", plan.Name, target.Name, target.Namespace, target.WorkloadName, target.ContainerName))
		}
	}
	return nil
}

func (s *Service) operationFailure(ctx context.Context, operation string, cause error) error {
	public := errors.New("无法生成操作计划，请检查所选模板、代码版本和配置；缺少的资源需要先补齐")
	for _, known := range []error{pipeline.ErrPipelineIncomplete, pipeline.ErrWorkflowRuntimeUnavailable, pipeline.ErrWorkflowRuntimeNotPrepared, pipeline.ErrManualCommitRequired, pipeline.ErrManualCommitNotFound, pipeline.ErrWorkflowNotActive, pipeline.ErrManualArtifactInvalid, pipeline.ErrInvalidApplicationName, pipeline.ErrWorkflowExists, pipeline.ErrApplicationExists} {
		if errors.Is(cause, known) {
			public = known
			break
		}
	}
	return s.failure(ctx, operation, cause, public)
}

func (s *Service) proposeOperation(ctx context.Context, user *model.User, conversation string, input operationInput) (ActionView, error) {
	if _, err := s.conversation(ctx, user, conversation); err != nil {
		return ActionView{}, err
	}
	plan, name, err := s.operationPlan(ctx, user, input)
	if err != nil {
		return ActionView{}, err
	}
	if redact(plan.Preview.YAML) != plan.Preview.YAML {
		return ActionView{}, s.failure(ctx, "assistant_operation_sensitive", ErrInvalidInput, errors.New("配置可能包含密钥，请移除后再生成计划"))
	}
	for _, permission := range operationPermissions(input) {
		if _, err := s.authorize(ctx, user, permission); err != nil {
			return ActionView{}, err
		}
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return ActionView{}, s.failure(ctx, "assistant_operation_encode", err, ErrStorage)
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	now := time.Now().UTC()
	titles := map[string]string{"workflow_create": "创建应用流水线草稿", "workflow_activate": "启用流水线", "pipeline_deploy": "执行应用流水线"}
	row := model.AssistantAction{ID: newID(), ConversationID: conversation, UserID: user.ID, ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, Kind: input.Kind, Title: titles[input.Kind], Summary: "请查看操作计划，核对目标、配置和影响后确认。", TargetName: safeText(name, 128), Snapshot: string(encoded), Status: "pending", ExpiresAt: now.Add(5 * time.Minute), CreatedAt: now, UpdatedAt: now}
	if err := s.deps.Database.WithContext(ctx).Create(&row).Error; err != nil {
		return ActionView{}, s.failure(ctx, "assistant_operation_save", err, ErrStorage)
	}
	return actionView(row), nil
}

func (s *Service) confirmOperation(ctx context.Context, user *model.User, row model.AssistantAction) (ActionView, error) {
	if row.Status != "pending" {
		return ActionView{}, s.failure(ctx, "assistant_operation_replay", ErrActionState, ErrActionState)
	}
	if !time.Now().Before(row.ExpiresAt) {
		return ActionView{}, s.failure(ctx, "assistant_operation_expired", ErrActionExpired, ErrActionExpired)
	}
	var expected operationSnapshot
	if json.Unmarshal([]byte(row.Snapshot), &expected) != nil || expected.Input.Kind != row.Kind || expected.Fingerprint == "" {
		return ActionView{}, s.failure(ctx, "assistant_operation_snapshot", ErrActionState, ErrActionState)
	}
	current, _, err := s.operationPlan(ctx, user, expected.Input)
	if err != nil {
		return ActionView{}, err
	}
	if current.Fingerprint != expected.Fingerprint {
		return ActionView{}, s.failure(ctx, "assistant_operation_changed", ErrActionState, ErrActionState)
	}
	claim := s.owned(ctx, user).Model(&model.AssistantAction{}).Where(&model.AssistantAction{ID: row.ID, Status: "pending"}).Where("expires_at > ? AND snapshot = ?", time.Now().UTC(), row.Snapshot).Updates(map[string]any{"status": "executing", "updated_at": time.Now().UTC()})
	if claim.Error != nil || claim.RowsAffected != 1 {
		return ActionView{}, s.failure(ctx, "assistant_operation_claim", claim.Error, ErrActionState)
	}
	if err := s.audit(ctx, user.ID, "assistant.action.execute", row.ID, model.AuditSucceeded, map[string]any{"kind": row.Kind}); err != nil {
		return ActionView{}, err
	}
	// 领取后不自动重试；创建操作由业务事务保证整体成功或整体回滚。
	for _, permission := range operationPermissions(expected.Input) {
		if _, err = s.authorize(ctx, user, permission); err != nil {
			break
		}
	}
	result := ActionResult{Message: "操作已受理，请查看流水线运行记录的实际状态"}
	if err == nil {
		switch row.Kind {
		case "workflow_create":
			var created *pipeline.AssistantWorkflowPlan
			created, err = s.deps.Pipeline.CreateAssistantWorkflowExpected(ctx, user.ID, *expected.Input.Workflow, expected.Fingerprint)
			if err == nil {
				result.ApplicationID, result.WorkflowID = created.ApplicationID, created.WorkflowID
				result.Message = "流水线草稿已创建，尚未启用或部署"
			}
		case "pipeline_deploy":
			var run *model.PipelineRun
			run, err = s.deps.Pipeline.ExecuteAssistantRunExpected(ctx, user.ID, *expected.Input.Run, expected.Fingerprint)
			if run != nil {
				result.PipelineRunID = run.ID
			}
		case "workflow_activate":
			err = s.deps.Pipeline.ActivateAssistantWorkflowExpected(ctx, expected.Input.ApplicationID, expected.Input.WorkflowID, user.ID, expected.Fingerprint)
			if err == nil {
				result.WorkflowID = expected.Input.WorkflowID
				result.ApplicationID = expected.Input.ApplicationID
				result.Message = "流水线已启用，本次没有发起部署"
			}
		default:
			err = ErrForbidden
		}
	}
	row.Status = "succeeded"
	auditResult := model.AuditSucceeded
	if err != nil {
		row.Status = "failed"
		auditResult = model.AuditFailed
		result.Message = "操作未完成，请重新检查目标和配置，不会自动重试"
		if result.PipelineRunID != "" {
			result.Message = "运行记录已创建，但任务推进未完成，请查看运行记录；不会自动重新发起部署"
		}
		s.failure(ctx, "assistant_operation_execute", err, ErrActionState, "action_id", row.ID, "kind", row.Kind)
	}
	encoded, _ := json.Marshal(result)
	row.Result = string(encoded)
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	saved := s.owned(finish, user).Model(&model.AssistantAction{}).Where(&model.AssistantAction{ID: row.ID, Status: "executing"}).Updates(map[string]any{"status": row.Status, "result": row.Result, "updated_at": time.Now().UTC()})
	if saved.Error != nil || saved.RowsAffected != 1 {
		return ActionView{}, s.failure(finish, "assistant_operation_result", saved.Error, ErrStorage)
	}
	if err := s.audit(finish, user.ID, "assistant.action.result", row.ID, auditResult, map[string]any{"kind": row.Kind, "status": row.Status}); err != nil {
		return ActionView{}, err
	}
	return actionView(row), nil
}
