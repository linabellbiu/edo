package pipeline

import (
	"edo/internal/composeconfig"
	"edo/internal/model"
)

func composeServiceScope(doc map[string]any, selected []string) ([]string, error) {
	services, err := composeconfig.ServiceScope(doc, selected)
	if err != nil {
		return nil, ErrInvalidComposeDelivery
	}
	return services, nil
}

// 自动收集前序阶段镜像任务；运行快照保留来源，实际镜像再按完整引用精确匹配。
func autoComposeArtifactSources(stages []model.WorkflowStage, plans map[string]workflowBuildPlanSnapshot, deployments map[string]workflowDeploymentPlanSnapshot) {
	prior := []string{}
	for i := range stages {
		for j := range stages[i].Tasks {
			node := &stages[i].Tasks[j]
			if node.Type != model.WorkflowNodeDeploy || deployments[node.ID].Kind != model.DeploymentPlanComposeStack {
				continue
			}
			node.Config.ComposeImages = nil
			node.Config.ArtifactNodeIDs = append([]string{}, prior...)
			node.Config.ArtifactNodeID = ""
			if len(prior) > 0 {
				node.Config.ArtifactNodeID = prior[0]
			}
		}
		for _, node := range stages[i].Tasks {
			if node.Type == model.WorkflowNodeBuild {
				plan := plans[node.ID]
				if plan.Kind == model.BuildPlanDockerfile && plan.ImageRegistryID != "" {
					prior = append(prior, node.ID)
				}
			}
		}
	}
}
