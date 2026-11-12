package pipeline

import (
	"context"
	"log/slog"
	"strings"

	"edo/internal/model"
	"edo/internal/pipelineimage"
	composetemplate "github.com/compose-spec/compose-go/v2/template"
)

// 新运行只提供 Tag，不应用历史的服务与构建任务关联；已保存配置及运行快照不回写。
func tagOnlyWorkflowStages(stages []model.WorkflowStage) []model.WorkflowStage {
	result := cloneWorkflowStages(stages)
	for i := range result {
		for j := range result[i].Tasks {
			config := &result[i].Tasks[j].Config
			config.ImageBuildNodeID = ""
			config.ComposeBuildBindings = nil
		}
	}
	return result
}

func validateSelectedImageReferences(node model.WorkflowNode, kind model.DeploymentPlanKind, source, script string) error {
	if len(node.Config.ComposeBuildBindings) > 0 && kind != model.DeploymentPlanComposeStack {
		return pipelineimage.ErrBinding
	}
	if referencesPipelineImageTag(script) && node.Config.ImageBuildNodeID == "" {
		return pipelineimage.ErrBinding
	}
	if kind != model.DeploymentPlanComposeStack || strings.TrimSpace(source) == "" {
		return nil
	}
	doc, err := parseComposeStack(source)
	if err != nil {
		return err
	}
	services := doc["services"].(map[string]any)
	for _, binding := range node.Config.ComposeBuildBindings {
		if _, exists := services[binding.Service]; !exists {
			return pipelineimage.ErrBinding
		}
	}
	for service, raw := range services {
		template, _ := raw.(map[string]any)["image"].(string)
		_, referenced := composetemplate.ExtractVariables(map[string]any{"image": template}, nil)["EDO_IMAGE_TAG"]
		id := node.Config.ImageBuildNodeID
		bound := false
		for _, binding := range node.Config.ComposeBuildBindings {
			if binding.Service == service {
				id = binding.BuildNodeID
				bound = true
			}
		}
		if referenced && id == "" || bound && !referenced {
			return pipelineimage.ErrBinding
		}
	}
	return nil
}

func validImageBuildBindingShape(node model.WorkflowNode) bool {
	c := node.Config
	if len(c.ImageBuildNodeID) > 64 || len(c.ComposeBuildBindings) > 64 {
		return false
	}
	if (c.ImageBuildNodeID != "" || len(c.ComposeBuildBindings) > 0) && node.Type != model.WorkflowNodeDeploy {
		return false
	}
	seen := map[string]bool{}
	for _, binding := range c.ComposeBuildBindings {
		if !composeServicePattern.MatchString(binding.Service) || seen[binding.Service] || binding.BuildNodeID == "" || len(binding.BuildNodeID) > 64 {
			return false
		}
		seen[binding.Service] = true
	}
	return true
}

func validateImageBuildBindings(stages []model.WorkflowStage, kinds map[string]model.BuildPlanKind) []WorkflowIssue {
	var issues []WorkflowIssue
	for _, stage := range stages {
		for _, node := range stage.Tasks {
			if node.Type != model.WorkflowNodeDeploy {
				continue
			}
			prior := pipelineimage.PriorBuilds(stages, node.ID, kinds)
			valid := validImageBuildBindingShape(node)
			if node.Config.ImageBuildNodeID != "" {
				valid = valid && prior[node.Config.ImageBuildNodeID]
			}
			for _, binding := range node.Config.ComposeBuildBindings {
				valid = valid && prior[binding.BuildNodeID]
			}
			if !valid {
				issues = append(issues, WorkflowIssue{Code: "invalid_image_build_binding", Message: pipelineimage.ErrBinding.Error(), NodeID: node.ID, StageID: stage.ID})
			}
		}
	}
	return issues
}

func (s *Service) loadSelectedBuildImages(ctx context.Context, prepared *executionContext) error {
	kinds := map[string]model.BuildPlanKind{}
	for id, plan := range prepared.snapshot.BuildPlans {
		kinds[id] = plan.Kind
	}
	prior := pipelineimage.PriorBuilds(prepared.snapshot.Stages, prepared.node.ID, kinds)
	ids := []string{}
	if id := prepared.node.Config.ImageBuildNodeID; id != "" {
		ids = append(ids, id)
	}
	for _, binding := range prepared.node.Config.ComposeBuildBindings {
		ids = append(ids, binding.BuildNodeID)
	}
	fail := func(err error) error {
		slog.ErrorContext(ctx, "读取部署所选构建镜像失败", "operation", "deployment_selected_build", "pipeline_run_id", prepared.run.ID, "node_id", prepared.node.ID, "err", err)
		return pipelineimage.ErrBinding
	}
	if !validImageBuildBindingShape(prepared.node) {
		return fail(pipelineimage.ErrBinding)
	}
	for _, id := range ids {
		if !prior[id] {
			return fail(pipelineimage.ErrBinding)
		}
	}
	images, err := pipelineimage.Load(ctx, s.db, prepared.run, ids)
	if err != nil {
		return fail(err)
	}
	for _, image := range images {
		if model.RequiresDeploymentRegistry(prepared.deploymentPlan.Kind) && (image.StorageKind != model.ArtifactStorageKindRegistry || image.ImageRegistryID != prepared.node.Config.DeploymentRegistryID) {
			return fail(pipelineimage.ErrBinding)
		}
	}
	prepared.selectedBuildImages = images
	return nil
}

// 绑定只解释完整的内置镜像引用，不重写用户拼接的其他镜像名称。
func selectedComposeBuild(prepared *executionContext, service, template string) (model.Artifact, bool, error) {
	if !prepared.snapshot.ImageTagIncludesName {
		return model.Artifact{}, false, nil
	}
	_, referenced := composetemplate.ExtractVariables(map[string]any{"image": template}, nil)["EDO_IMAGE_TAG"]
	id := ""
	for _, binding := range prepared.node.Config.ComposeBuildBindings {
		if binding.Service == service {
			id = binding.BuildNodeID
			break
		}
	}
	if id == "" && referenced {
		id = prepared.node.Config.ImageBuildNodeID
	}
	if id == "" {
		if referenced {
			return model.Artifact{}, false, pipelineimage.ErrBinding
		}
		return model.Artifact{}, false, nil
	}
	image, ok := prepared.selectedBuildImages[id]
	if !ok || !referenced {
		return model.Artifact{}, false, pipelineimage.ErrBinding
	}
	return image, true, nil
}
