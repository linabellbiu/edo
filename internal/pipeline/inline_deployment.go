package pipeline

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"edo/internal/configfile"
	"edo/internal/deployment"
	"edo/internal/model"
)

func normalizeInlineDeploymentPlan(input *model.WorkflowDeploymentPlan, sources ...*model.WorkflowComposeSource) error {
	if len(sources) > 0 && sources[0] != nil {
		if input == nil || input.Kind != model.DeploymentPlanComposeStack || !validResourceName(input.Name) || input.DeploymentTarget == nil || input.TimeoutSeconds < 60 || input.TimeoutSeconds > 3600 {
			return ErrInvalidWorkflow
		}
		if err := normalizeDeploymentExecution(input.Kind, "", input.ExecutionConfig); err != nil {
			return err
		}
		input.Script = ""
		input.ComposeYAML = ""
		input.ServiceName = ""
		return nil
	}
	if input == nil {
		return nil
	}
	var target *deployment.TargetInput
	if t := input.DeploymentTarget; t != nil {
		target = &deployment.TargetInput{Name: input.Name, Description: input.Description, Platform: t.Platform, EnvironmentID: t.EnvironmentID,
			HostIDs: t.HostIDs, HostID: t.HostID, RuntimeID: t.RuntimeID, WorkingDirectory: t.WorkingDirectory, Namespace: t.Namespace, WorkloadName: t.WorkloadName, ContainerName: t.ContainerName, RolloutTimeout: input.TimeoutSeconds}
	}
	normalized, err := normalizeDeploymentPlanInput(DeploymentPlanInput{Name: input.Name, Kind: input.Kind, Description: input.Description,
		ExecutionConfig: input.ExecutionConfig, Script: input.Script, ComposeYAML: input.ComposeYAML, ServiceName: input.ServiceName, DockerConfig: input.DockerConfig, TimeoutSeconds: input.TimeoutSeconds, DeploymentTarget: target})
	if err != nil {
		if errors.Is(err, ErrDeploymentInputs) || errors.Is(err, configfile.ErrParameters) {
			return err
		}
		return fmt.Errorf("%w：自定义部署方案无效，请检查执行方式和参数", ErrInvalidWorkflow)
	}
	input.Name, input.Kind, input.Description = normalized.Name, normalized.Kind, normalized.Description
	input.Script, input.ComposeYAML, input.ServiceName = normalized.Script, normalized.ComposeYAML, normalized.ServiceName
	input.ExecutionConfig = normalized.ExecutionConfig
	input.DockerConfig, input.TimeoutSeconds = normalized.DockerConfig, normalized.TimeoutSeconds
	// 只接受目标的可配置字段，不保存客户端传入的资源身份和归属。
	if t := input.DeploymentTarget; t != nil {
		input.DeploymentTarget = &model.DeploymentTarget{Platform: t.Platform, EnvironmentID: t.EnvironmentID, HostIDs: t.HostIDs, HostID: t.HostID, RuntimeID: t.RuntimeID,
			WorkingDirectory: t.WorkingDirectory, Namespace: t.Namespace, WorkloadName: t.WorkloadName, ContainerName: t.ContainerName, RolloutTimeout: normalized.TimeoutSeconds}
	}
	return nil
}

func workflowDeploymentPlanKey(node model.WorkflowNode) string {
	if node.Config.InlineDeploymentPlan != nil {
		id, _ := model.InlineDeploymentIdentity("", node.ID)
		return id
	}
	return node.Config.DeploymentPlanID
}

func (s *Service) resolveInlineDeploymentPlan(ctx context.Context, application *model.Application, workflowID string, node model.WorkflowNode, databases ...*gorm.DB) (*model.DeploymentPlan, *model.DeploymentTarget, error) {
	if s.deployments == nil {
		s.logger.ErrorContext(ctx, "自定义部署解析服务不可用", "operation", "workflow_inline_resolve", "node_id", node.ID, "err", ErrInvalidWorkflow)
		return nil, nil, ErrInvalidWorkflow
	}
	if err := s.validateDeploymentExecutionReferences(ctx, node.Config.InlineDeploymentPlan.ExecutionConfig, databases...); err != nil {
		return nil, nil, err
	}
	service := s.deployments
	if len(databases) > 0 {
		service = service.WithTransaction(databases[0])
	}
	plan, target, err := service.ResolveInlinePlan(ctx, node.Config.InlineDeploymentPlan, workflowID, node.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w：自定义部署方案的环境或执行目标不可用", ErrInvalidWorkflow)
	}
	if !deploymentPlanSupportsTarget(plan.Kind, target.Platform) {
		s.logger.ErrorContext(ctx, "自定义部署方式与目标不匹配", "operation", "workflow_inline_resolve", "node_id", node.ID, "err", ErrDeploymentPlanTargetMismatch)
		return nil, nil, ErrDeploymentPlanTargetMismatch
	}
	if application != nil {
		plan.ProjectID, plan.DepartmentID = application.ProjectID, application.DepartmentID
		target.ProjectID, target.DepartmentID = application.ProjectID, application.DepartmentID
	}
	return plan, target, nil
}
