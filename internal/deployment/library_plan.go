package deployment

import (
	"context"
	"encoding/json"
	"errors"

	"edo/internal/model"
)

// ResolveWorkflowLibraryPlan 将库方案和阶段差异解析为独立的有效方案，不写方案或目标表。
func (s *Service) ResolveWorkflowLibraryPlan(ctx context.Context, base model.DeploymentPlan, baseTarget *model.DeploymentTarget, override *model.WorkflowDeploymentOverride, workflowID, nodeID string) (*model.DeploymentPlan, *model.DeploymentTarget, error) {
	fail := func(cause error) (*model.DeploymentPlan, *model.DeploymentTarget, error) {
		s.logger.WarnContext(ctx, "解析部署阶段方案失败", "operation", "workflow_library_plan_resolve", "deployment_plan_id", base.ID, "node_id", nodeID, "err", cause)
		return nil, nil, model.ErrDeploymentOverride
	}
	base = model.WorkflowDeploymentSnapshotBase(base, override)
	if override != nil && override.PlanSnapshot != nil {
		baseTarget = base.DeploymentTarget
	}
	if base.Kind != model.DeploymentPlanKubernetes || base.ID == "" {
		return fail(ErrInvalidTarget)
	}
	if err := model.ValidateWorkflowDeploymentOverride(override); err != nil {
		return fail(err)
	}
	var copied model.DeploymentPlan
	data, err := json.Marshal(base)
	if err != nil {
		return fail(err)
	}
	if err = json.Unmarshal(data, &copied); err != nil {
		return fail(err)
	}
	timeout := copied.TimeoutSeconds
	if override != nil && override.TimeoutSeconds != nil {
		timeout = *override.TimeoutSeconds
	}
	target := baseTarget
	if override != nil && override.DeploymentTarget != nil {
		target = override.DeploymentTarget
	}
	if target == nil {
		return fail(ErrInvalidTarget)
	}
	targetCopy := *target
	targetCopy.Platform = model.DeploymentKubernetes
	if baseTarget != nil {
		targetCopy.EnvironmentID = baseTarget.EnvironmentID
	}
	execution := copied.ExecutionConfig
	if execution == nil {
		execution = &model.DeploymentExecutionConfig{}
	}
	var patch json.RawMessage
	if override != nil {
		patch = override.Kubernetes
	}
	if model.HasWorkflowDeploymentOverride(&model.WorkflowDeploymentOverride{Kubernetes: patch}) && execution.Kubernetes == nil {
		return fail(model.ErrDeploymentOverride)
	}
	originalTemplate := ""
	if execution.Kubernetes != nil {
		originalTemplate = execution.Kubernetes.Template
	}
	execution.Kubernetes, err = model.ApplyWorkflowKubernetesPatch(execution.Kubernetes, patch)
	if err != nil {
		return fail(err)
	}
	if execution.Kubernetes != nil && execution.Kubernetes.Template != originalTemplate {
		return fail(model.ErrDeploymentOverride)
	}
	if override != nil && override.ArtifactNodeID != nil {
		execution.ArtifactBuildPlanID = ""
		execution.ArtifactNodeID = *override.ArtifactNodeID
	}
	if override != nil {
		if override.DeploymentRegistryID != nil {
			execution.DeploymentRegistryID = *override.DeploymentRegistryID
		}
		if override.DeploymentInputVersion != nil {
			execution.DeploymentInputVersion = *override.DeploymentInputVersion
		}
		if override.DeploymentInputs != nil {
			execution.DeploymentInputs = append([]model.DeploymentInput(nil), (*override.DeploymentInputs)...)
		}
	}
	if execution.DeploymentInputVersion == 1 {
		// 新显式镜像来源只使用用户选择的仓库，不继承库方案的旧构建绑定。
		execution.ArtifactNodeID, execution.ArtifactBuildPlanID = "", ""
		execution.ArtifactNodeIDs, execution.ComposeImages = nil, nil
	}
	plan, resolved, err := s.ResolveInlinePlan(ctx, &model.WorkflowDeploymentPlan{Name: copied.Name, Description: copied.Description, Kind: copied.Kind, TimeoutSeconds: timeout, ExecutionConfig: execution, DeploymentTarget: &targetCopy}, workflowID, nodeID)
	if err != nil {
		return fail(err)
	}
	if baseTarget != nil && sameKubernetesLocation(resolved, baseTarget) {
		resolved.ID = baseTarget.ID
		plan.DeploymentTargetID = baseTarget.ID
	}
	// 使用原库方案的展示信息；有效身份只与流水线任务绑定，不随方案或参数切换而漂移。
	plan.CreatedBy, plan.CreatedAt, plan.UpdatedAt = copied.CreatedBy, copied.CreatedAt, copied.UpdatedAt
	return plan, resolved, nil
}

// resolveRollbackLibraryPlan 继续按当前定义核验来源方案及目标；历史运行只用于绑定来源身份。
func (s *Service) resolveRollbackLibraryPlan(ctx context.Context, source *model.DeploymentRecord, run model.PipelineRun, node model.WorkflowNode) (*model.DeploymentPlan, *model.DeploymentTarget, error) {
	var snapshot struct {
		Plans map[string]struct {
			SourcePlanID string `json:"source_plan_id"`
		} `json:"deployment_plans"`
	}
	if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		s.logger.WarnContext(ctx, "解析回退方案来源快照失败", "operation", "workflow_library_rollback_source", "deployment_id", source.ID, "err", err)
		return nil, nil, ErrInvalidTarget
	}
	if snapshot.Plans[source.WorkflowNodeID].SourcePlanID == "" || snapshot.Plans[source.WorkflowNodeID].SourcePlanID != node.Config.DeploymentPlanID {
		return nil, nil, ErrInvalidTarget
	}
	var base model.DeploymentPlan
	if err := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND department_id = ? AND is_active = ?", node.Config.DeploymentPlanID, source.ProjectID, source.DepartmentID, true).First(&base).Error; err != nil {
		s.logger.WarnContext(ctx, "读取回退来源部署方案失败", "operation", "workflow_library_rollback_source", "deployment_id", source.ID, "deployment_plan_id", node.Config.DeploymentPlanID, "err", err)
		return nil, nil, err
	}
	var target *model.DeploymentTarget
	base = model.WorkflowDeploymentSnapshotBase(base, node.Config.DeploymentOverride)
	if base.DeploymentTargetID != "" {
		target = &model.DeploymentTarget{}
		if err := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND department_id = ? AND is_active = ?", base.DeploymentTargetID, source.ProjectID, source.DepartmentID, true).First(target).Error; err != nil {
			s.logger.WarnContext(ctx, "读取回退来源目标失败", "operation", "workflow_library_rollback_target", "deployment_id", source.ID, "err", err)
			return nil, nil, err
		}
		if base.DeploymentTarget != nil {
			target = base.DeploymentTarget
		}
	}
	plan, resolved, err := s.ResolveWorkflowLibraryPlan(ctx, base, target, node.Config.DeploymentOverride, run.WorkflowID, source.WorkflowNodeID)
	if err != nil {
		return nil, nil, errors.Join(ErrInvalidTarget, err)
	}
	return plan, resolved, nil
}
