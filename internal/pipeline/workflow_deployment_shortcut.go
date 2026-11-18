package pipeline

import (
	"context"
	"errors"
	"slices"

	"gorm.io/gorm"

	"edo/internal/model"
)

var (
	ErrDeploymentShortcutPermission = errors.New("没有创建快捷部署方案的权限")
	ErrDeploymentShortcutUpstream   = errors.New("快捷部署方案需要可复用的上游产物引用，请使用命名产物或构建方案")
)

func (s *Service) saveDeploymentShortcut(ctx context.Context, tx *gorm.DB, actorID string, input WorkflowInput) (string, error) {
	if input.ReusableDeploymentPlanName == "" {
		return "", nil
	}
	if err := s.requireDeliveryPermissionWithDatabase(ctx, tx, actorID, "deployment_plan.create"); err != nil {
		return "", ErrDeploymentShortcutPermission
	}
	nodes := workflowTasks(workflowSaveStages(input))
	if input.TaskID == "" || len(nodes) != 1 || nodes[0].Type != model.WorkflowNodeDeploy || nodes[0].Config.InlineDeploymentPlan == nil {
		return "", ErrInvalidDeploymentPlan
	}
	node := nodes[0]
	inline := node.Config.InlineDeploymentPlan
	execution, err := shortcutDeploymentExecution(node, input.Stages)
	if err != nil {
		return "", err
	}
	yaml := inline.ComposeYAML
	if execution.ComposeSource != nil {
		yaml = ""
	}
	serviceName := inline.ServiceName
	if inline.Kind == model.DeploymentPlanDocker {
		serviceName = ""
	}
	plan, err := s.createDeploymentPlan(ctx, tx, actorID, DeploymentPlanInput{
		Name: input.ReusableDeploymentPlanName, Kind: inline.Kind, Description: inline.Description,
		Script: inline.Script, ComposeYAML: yaml, ServiceName: serviceName, DockerConfig: inline.DockerConfig,
		TimeoutSeconds: inline.TimeoutSeconds, ExecutionConfig: execution, Reusable: true,
	})
	if err != nil {
		return "", err
	}
	return plan.ID, nil
}

func shortcutDeploymentExecution(node model.WorkflowNode, stages []model.WorkflowStage) (*model.DeploymentExecutionConfig, error) {
	config := node.Config
	if execution := config.InlineDeploymentPlan.ExecutionConfig; execution != nil {
		model.ApplyDeploymentExecution(&config, execution)
	}
	execution := model.DeploymentExecutionFromNode(config)
	prior := make(map[string]string)
	for _, stage := range stages {
		if slices.ContainsFunc(stage.Tasks, func(task model.WorkflowNode) bool { return task.ID == node.ID }) {
			break
		}
		for _, task := range stage.Tasks {
			if task.Type == model.WorkflowNodeBuild {
				prior[task.ID] = task.Config.BuildPlanID
			}
		}
	}
	if execution.ArtifactNodeID != "" {
		execution.ArtifactBuildPlanID = prior[execution.ArtifactNodeID]
		if execution.ArtifactBuildPlanID == "" {
			return nil, ErrDeploymentShortcutUpstream
		}
		execution.ArtifactNodeID = ""
	}
	for i := range execution.DeploymentInputs {
		upstream := &execution.DeploymentInputs[i]
		if upstream.Kind != "upstream" || upstream.ArtifactNodeID == "" {
			continue
		}
		if upstream.ArtifactReference == "" {
			if name := "${" + upstream.ArtifactName + "}"; upstreamOutputName(name) != "" {
				upstream.ArtifactReference = name
			} else if upstream.ArtifactBuildPlanID = prior[upstream.ArtifactNodeID]; upstream.ArtifactBuildPlanID == "" {
				return nil, ErrDeploymentShortcutUpstream
			}
		}
		upstream.ArtifactNodeID, upstream.ArtifactName = "", ""
	}
	if source := execution.ComposeSource; source != nil && source.Kind == "upstream" {
		if source.ArtifactReference == "" {
			source.ArtifactReference = "${" + source.ArtifactName + "}"
		}
		if upstreamOutputName(source.ArtifactReference) == "" {
			return nil, ErrDeploymentShortcutUpstream
		}
		source.ArtifactNodeID, source.ArtifactName = "", ""
	}
	return execution, nil
}
