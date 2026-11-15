package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"edo/internal/configfile"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/shellcheck"
	"gorm.io/gorm"
)

func (s *Service) validateDeploymentExecutionReferences(ctx context.Context, config *model.DeploymentExecutionConfig, databases ...*gorm.DB) error {
	if config == nil {
		return nil
	}
	queryDB := s.db
	if len(databases) > 0 {
		queryDB = databases[0]
	}
	if err := s.validateKubernetesTemplateConfiguration(ctx, queryDB, config.Kubernetes); err != nil {
		return err
	}
	check := func(value any, id string) error {
		if id == "" {
			return nil
		}
		query := queryDB.WithContext(ctx).Model(value).Where("uuid = ?", id)
		if scope, ok := database.DepartmentScopeFromContext(ctx); ok && scope.ProjectID != "" {
			query = query.Where("project_id = ?", scope.ProjectID)
		}
		var count int64
		if err := query.Count(&count).Error; err != nil {
			s.logger.ErrorContext(ctx, "读取部署方案引用失败", "operation", "deployment_plan_execution_reference", "err", err)
			return ErrInvalidDeploymentPlan
		}
		if count != 1 {
			s.logger.WarnContext(ctx, "部署方案引用不存在或不属于当前项目", "operation", "deployment_plan_execution_reference", "err", ErrInvalidDeploymentPlan)
			return ErrInvalidDeploymentPlan
		}
		return nil
	}
	if err := check(&model.ImageRegistry{}, config.DeploymentRegistryID); err != nil {
		return err
	}
	if err := check(&model.BuildPlan{}, config.ArtifactBuildPlanID); err != nil {
		return err
	}
	for _, input := range config.DeploymentInputs {
		var value any
		var id string
		switch input.Kind {
		case "configuration":
			value, id = &model.DeliveryVariable{}, input.ConfigurationID
		case "registry":
			value, id = &model.ImageRegistry{}, input.RegistryID
		case "upstream":
			value, id = &model.BuildPlan{}, input.ArtifactBuildPlanID
		}
		if err := check(value, id); err != nil {
			return err
		}
	}
	for _, file := range config.ConfigurationFiles {
		if err := check(&model.DeliveryVariable{}, file.ID); err != nil {
			return err
		}
	}
	return nil
}

func normalizeDeploymentExecution(kind model.DeploymentPlanKind, composeYAML string, config *model.DeploymentExecutionConfig) error {
	if config == nil {
		return nil
	}
	if config.Kubernetes != nil {
		if kind != model.DeploymentPlanKubernetes {
			slog.Warn("非 Kubernetes 部署不能使用集群模板", "operation", "kubernetes_template_normalize", "kind", kind, "err", ErrInvalidDeploymentPlan)
			return ErrInvalidDeploymentPlan
		}
		if err := model.ValidateKubernetesDeploymentSettings(config.Kubernetes); err != nil {
			return err
		}
	}
	node := model.WorkflowNode{ID: "deployment-plan", Name: "部署方案", Type: model.WorkflowNodeDeploy, Config: config.NodeConfig()}
	if len(config.DeploymentRegistryID) > 36 {
		slog.Warn("部署镜像仓库标识无效", "operation", "deployment_registry_normalize", "err", ErrInvalidDeploymentPlan)
		return ErrInvalidDeploymentPlan
	}
	if len(node.Config.ArtifactNodeIDs) > 64 || len(node.Config.ComposeImages) > 32 {
		slog.Warn("部署方案上游来源数量无效", "operation", "deployment_plan_execution_normalize", "kind", kind, "err", ErrInvalidDeploymentPlan)
		return ErrInvalidDeploymentPlan
	}
	// Compose 使用用户填写的镜像；旧自动收集结果不能成为方案的新约束。
	if kind == model.DeploymentPlanComposeStack {
		node.Config.ArtifactBuildPlanID = ""
		node.Config.ArtifactNodeID = ""
		node.Config.ArtifactNodeIDs = nil
		node.Config.ComposeImages = nil
	}
	err := sanitizeWorkflowNode(&node, kind)
	if err == nil && (!validDeploymentScriptVariables(node.Config.EnvironmentVariables) || len(shellcheck.Check(node.Config.DeploymentScript)) > 0 ||
		node.Config.DeploymentTagReference != "" && node.Config.DeploymentTagReference != "${"+composeTagName(node.Config.DeploymentTagReference)+"}" || len(node.Config.ArtifactBuildPlanID) > 36 || node.Config.ArtifactBuildPlanID != "" && node.Config.ArtifactNodeID != "") {
		err = ErrInvalidDeploymentPlan
	}
	if err == nil && node.Config.Compose != nil && composeTagName(node.Config.Compose.TagVariable) == "" {
		err = ErrInvalidDeploymentPlan
	}
	if err == nil && kind != model.DeploymentPlanComposeStack && node.Config.Compose != nil {
		err = ErrInvalidDeploymentPlan
	}
	if err == nil && kind == model.DeploymentPlanKubernetes {
		err = validateKubernetesRegistryExecution(node.Config)
		if err == nil && node.Config.DeploymentInputVersion == 1 {
			node.Config.ArtifactNodeID, node.Config.ArtifactBuildPlanID = "", ""
			node.Config.ArtifactNodeIDs, node.Config.ComposeImages = nil, nil
		}
	}
	if err == nil && kind == model.DeploymentPlanComposeStack && composeYAML != "" && strings.TrimSpace(node.Config.DeploymentScript) == "" {
		err = validateComposeBindingsYAML(composeYAML, node.Config)
	}
	if err != nil {
		slog.Warn("部署方案执行配置无效", "operation", "deployment_plan_execution_normalize", "kind", kind, "err", err)
		if errors.Is(err, ErrDeploymentInputs) || errors.Is(err, configfile.ErrParameters) {
			return err
		}
		return ErrInvalidDeploymentPlan
	}
	if node.Config.Compose != nil {
		node.Config.Compose.TagVariable = "${" + composeTagName(node.Config.Compose.TagVariable) + "}"
	}
	*config = *model.DeploymentExecutionFromNode(node.Config)
	return nil
}

func validateKubernetesRegistryExecution(config model.WorkflowNodeConfig) error {
	if config.DeploymentScript != "" || len(config.ConfigurationFiles) > 0 || config.DeploymentParameters != "" {
		return ErrInvalidDeploymentPlan
	}
	if config.DeploymentInputVersion == 0 {
		return nil // 已保存的旧上游镜像配置继续按原规则执行。
	}
	if config.DeploymentInputVersion == 1 && len(config.DeploymentInputs) == 0 {
		return nil // 通用方案可暂不绑定镜像；启用和执行仍要求补齐。
	}
	if !model.UsesRegistryDeploymentImage(config) || config.DeploymentRegistryID == "" || config.DeploymentInputs[0].RegistryID != config.DeploymentRegistryID {
		slog.Warn("Kubernetes 部署需要准确的单个仓库镜像", "operation", "kubernetes_registry_execution_normalize", "err", ErrDeploymentInputs)
		return ErrDeploymentInputs
	}
	return nil
}

// 独立方案引用构建方案，不绑定另一个流水线的任务 UUID。执行时只匹配前序阶段，
// 同一方案存在多个候选任务时明确拒绝，不能静默选择其中一个产物。
func resolveDeploymentBuildBindings(node *model.WorkflowNode, stages []model.WorkflowStage) error {
	resolve := func(planID string) (string, error) {
		var matches []string
		for _, stage := range stages {
			for _, task := range stage.Tasks {
				if task.ID == node.ID {
					if len(matches) == 1 {
						return matches[0], nil
					}
					slog.Warn("部署方案上游构建无法唯一匹配", "operation", "deployment_plan_build_binding", "node_id", node.ID, "build_plan_id", planID, "candidates", len(matches), "err", ErrInvalidWorkflow)
					return "", ErrInvalidWorkflow
				}
			}
			for _, task := range stage.Tasks {
				if task.Type == model.WorkflowNodeBuild && task.Config.BuildPlanID == planID {
					matches = append(matches, task.ID)
				}
			}
		}
		return "", ErrInvalidWorkflow
	}
	if node.Config.ArtifactBuildPlanID != "" {
		id, err := resolve(node.Config.ArtifactBuildPlanID)
		if err != nil {
			return err
		}
		node.Config.ArtifactNodeID, node.Config.ArtifactBuildPlanID = id, ""
	}
	for i := range node.Config.DeploymentInputs {
		input := &node.Config.DeploymentInputs[i]
		if input.Kind == "upstream" && input.ArtifactReference != "" {
			id, name, err := resolveUpstreamOutput(node.ID, input.ArtifactReference, stages)
			if err != nil {
				return err
			}
			input.ArtifactNodeID, input.ArtifactName, input.ArtifactBuildPlanID = id, name, ""
			continue
		}
		if input.Kind != "upstream" || input.ArtifactBuildPlanID == "" {
			continue
		}
		id, err := resolve(input.ArtifactBuildPlanID)
		if err != nil {
			return err
		}
		input.ArtifactNodeID, input.ArtifactBuildPlanID = id, ""
	}
	if source := node.Config.ComposeSource; source != nil && source.Kind == "upstream" && source.ArtifactReference != "" {
		id, name, err := resolveUpstreamOutput(node.ID, source.ArtifactReference, stages)
		if err != nil {
			return err
		}
		source.ArtifactNodeID, source.ArtifactName = id, name
	}
	return nil
}

func executionReferencesBuild(config *model.DeploymentExecutionConfig, id string) bool {
	if config == nil {
		return false
	}
	if config.ArtifactBuildPlanID == id {
		return true
	}
	for _, input := range config.DeploymentInputs {
		if input.ArtifactBuildPlanID == id {
			return true
		}
	}
	return false
}
