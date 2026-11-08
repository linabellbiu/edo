package deployment

import (
	"context"
	"fmt"

	"edo/internal/database"
	"edo/internal/model"
)

// ResolveInlinePlan 复用正式目标校验，只读解析当前环境，不写方案或目标表。
func (s *Service) ResolveInlinePlan(ctx context.Context, input *model.WorkflowDeploymentPlan, workflowID, nodeID string) (*model.DeploymentPlan, *model.DeploymentTarget, error) {
	if input == nil || input.DeploymentTarget == nil {
		s.logger.ErrorContext(ctx, "流水线自定义部署目标缺失", "operation", "workflow_inline_target", "node_id", nodeID, "err", ErrInvalidTarget)
		return nil, nil, ErrInvalidTarget
	}
	t := input.DeploymentTarget
	resolved, err := s.normalizeTarget(ctx, TargetInput{Name: input.Name, Description: input.Description, Platform: t.Platform,
		EnvironmentID: t.EnvironmentID, HostIDs: t.HostIDs, RuntimeID: t.RuntimeID, HostID: t.HostID, WorkingDirectory: t.WorkingDirectory,
		Namespace: t.Namespace, WorkloadName: t.WorkloadName, ContainerName: t.ContainerName, RolloutTimeout: input.TimeoutSeconds})
	if err != nil {
		s.logger.ErrorContext(ctx, "流水线自定义部署目标无效", "operation", "workflow_inline_target", "node_id", nodeID, "err", err)
		return nil, nil, fmt.Errorf("%w：自定义部署方案的环境或执行目标不可用", ErrInvalidTarget)
	}
	planID, targetID := model.InlineDeploymentIdentity(workflowID, nodeID)
	scope, _ := database.DepartmentScopeFromContext(ctx)
	target := &model.DeploymentTarget{ID: targetID, ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, Name: resolved.Name, Description: resolved.Description,
		Platform: resolved.Platform, EnvironmentID: resolved.EnvironmentID, HostIDs: resolved.HostIDs, HostID: resolved.HostID, RuntimeID: resolved.RuntimeID,
		WorkingDirectory: resolved.WorkingDirectory, Namespace: resolved.Namespace, WorkloadName: resolved.WorkloadName, ContainerName: resolved.ContainerName,
		RolloutTimeout: resolved.RolloutTimeout, IsActive: true}
	plan := &model.DeploymentPlan{ID: planID, ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, Name: input.Name, Description: input.Description, Kind: input.Kind,
		ExecutionConfig: input.ExecutionConfig, Script: input.Script, ComposeYAML: input.ComposeYAML, ServiceName: input.ServiceName, DockerConfig: input.DockerConfig, TimeoutSeconds: input.TimeoutSeconds,
		DeploymentTargetID: targetID, DeploymentTarget: target, IsActive: true}
	return plan, target, nil
}

// rollbackPlanTarget 对自定义方案重新核对所属流水线，独立方案继续使用原资源边界。
func (s *Service) rollbackPlanTarget(ctx context.Context, source *model.DeploymentRecord, run model.PipelineRun) (*model.DeploymentPlan, *model.DeploymentTarget, error) {
	inlineID, _ := model.InlineDeploymentIdentity(run.WorkflowID, source.WorkflowNodeID)
	if run.WorkflowID != "" && inlineID == source.DeploymentPlanID {
		var workflow model.ReleaseWorkflow
		if err := s.db.WithContext(ctx).Where("uuid = ? AND application_id = ? AND project_id = ? AND department_id = ?", run.WorkflowID, source.ApplicationID, source.ProjectID, source.DepartmentID).First(&workflow).Error; err != nil {
			return nil, nil, err
		}
		for _, stage := range workflow.Stages {
			for _, node := range stage.Tasks {
				if (node.ID == source.WorkflowNodeID || model.DeploymentHostNodeID(node.ID, source.HostID) == source.WorkflowNodeID) && node.Type == model.WorkflowNodeDeploy && node.Config.DeploymentPlanID == "" && node.Config.InlineDeploymentPlan != nil {
					definition := *node.Config.InlineDeploymentPlan
					if node.ID != source.WorkflowNodeID {
						if definition.DeploymentTarget == nil {
							return nil, nil, ErrInvalidTarget
						}
						selected := false
						for _, hostID := range definition.DeploymentTarget.HostIDs {
							if hostID == source.HostID {
								selected = true
							}
						}
						if !selected {
							return nil, nil, ErrInvalidTarget
						}
						targetCopy := *definition.DeploymentTarget
						targetCopy.HostIDs = []string{source.HostID}
						definition.DeploymentTarget = &targetCopy
					}
					plan, target, err := s.ResolveInlinePlan(ctx, &definition, workflow.ID, source.WorkflowNodeID)
					if err == nil {
						restoreManagedComposeDirectory(source, run, target)
					}
					return plan, target, err
				}
			}
		}
		return nil, nil, ErrInvalidTarget
	}
	var plan model.DeploymentPlan
	if err := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND department_id = ? AND is_active = ?", source.DeploymentPlanID, source.ProjectID, source.DepartmentID, true).First(&plan).Error; err != nil {
		return nil, nil, err
	}
	var target model.DeploymentTarget
	if err := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND department_id = ? AND is_active = ?", plan.DeploymentTargetID, source.ProjectID, source.DepartmentID, true).First(&target).Error; err != nil {
		return nil, nil, err
	}
	if target.HostIDs != nil {
		resolved, err := s.ResolveHostTarget(ctx, target, source.HostID)
		if err != nil {
			return nil, nil, err
		}
		target = *resolved
	}
	restoreManagedComposeDirectory(source, run, &target)
	return &plan, &target, nil
}
