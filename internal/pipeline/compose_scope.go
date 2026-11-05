package pipeline

import (
	"sort"
	"strings"

	"edo/internal/model"
)

// composeServiceScope 仅计算服务作用范围，依赖的启动顺序仍由官方 Compose 处理。
func composeServiceScope(doc map[string]any, selected []string) ([]string, error) {
	services := doc["services"].(map[string]any)
	seen, visiting := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if seen[name] {
			return nil
		}
		if visiting[name] {
			return ErrInvalidComposeDelivery
		}
		raw, ok := services[name].(map[string]any)
		if !ok {
			return ErrInvalidComposeDelivery
		}
		visiting[name] = true
		dependencies := []string{}
		switch value := raw["depends_on"].(type) {
		case []any:
			for _, dependency := range value {
				name, ok := dependency.(string)
				if !ok {
					return ErrInvalidComposeDelivery
				}
				dependencies = append(dependencies, name)
			}
		case map[string]any:
			for name := range value {
				dependencies = append(dependencies, name)
			}
		case nil:
		default:
			return ErrInvalidComposeDelivery
		}
		for _, key := range []string{"network_mode", "pid", "ipc"} {
			if value, ok := raw[key].(string); ok && strings.HasPrefix(value, "service:") {
				dependencies = append(dependencies, strings.TrimPrefix(value, "service:"))
			}
		}
		for _, dependency := range dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		delete(visiting, name)
		seen[name] = true
		return nil
	}
	roots := selected
	if len(roots) == 0 {
		for name, raw := range services {
			if service := raw.(map[string]any); service["profiles"] == nil {
				roots = append(roots, name)
			}
		}
	}
	for _, name := range roots {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
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
