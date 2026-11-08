package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	distributionreference "github.com/distribution/reference"
	"go.yaml.in/yaml/v3"

	"edo/internal/artifact"
	"edo/internal/deployment"
	"edo/internal/envtext"
	"edo/internal/model"
)

const maximumComposeStackBytes = 128 * 1024

// 草稿允许字段暂未填完；完整语义校验在启用及执行时完成。
func validComposeNodeShape(config model.WorkflowNodeConfig) bool {
	if len(config.ComposeImages) > 32 {
		return false
	}
	if c := config.Compose; c != nil {
		if len(c.Services) > 64 || len(c.ProjectName) > 128 || len(c.TagVariable) > 131 || len(c.EnvFile) > 1024 || len(c.HealthURL) > 2048 {
			return false
		}
	}
	for _, binding := range config.ComposeImages {
		if len(binding.Service) > 128 || len(binding.ArtifactNodeID) > 64 {
			return false
		}
	}
	return true
}

func parseComposeStack(value string) (map[string]any, error) {
	if len(value) == 0 || len(value) > maximumComposeStackBytes {
		return nil, ErrInvalidComposeDelivery
	}
	decoder := yaml.NewDecoder(strings.NewReader(value))
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		return nil, ErrInvalidComposeDelivery
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidComposeDelivery
	}
	services, ok := doc["services"].(map[string]any)
	if !ok || len(services) == 0 || len(services) > 64 || doc["include"] != nil {
		return nil, ErrInvalidComposeDelivery
	}
	for name, raw := range services {
		service, ok := raw.(map[string]any)
		if !ok || !composeServicePattern.MatchString(name) || service["build"] != nil || service["extends"] != nil {
			return nil, ErrInvalidComposeDelivery
		}
	}
	encoded, err := yaml.Marshal(doc)
	if err != nil || len(encoded) > maximumComposeStackBytes {
		return nil, ErrInvalidComposeDelivery
	}
	return doc, nil
}

func validateComposeNode(config model.WorkflowNodeConfig) error {
	if len(config.ComposeImages) > 32 || config.Compose == nil {
		return ErrInvalidComposeDelivery
	}
	settings := config.Compose
	if !composeProjectPattern.MatchString(settings.ProjectName) || composeTagName(settings.TagVariable) == "" {
		return ErrInvalidComposeDelivery
	}
	if (settings.EnvFile != "" && !validComposeFilePath(settings.EnvFile)) || !validComposeHealthURL(settings.HealthURL) {
		return ErrInvalidComposeDelivery
	}
	selected := map[string]bool{}
	for _, name := range settings.Services {
		if !composeServicePattern.MatchString(name) || selected[name] {
			return ErrInvalidComposeDelivery
		}
		selected[name] = true
	}
	seen := map[string]bool{}
	sources := map[string]bool{}
	used := map[string]bool{}
	for _, source := range config.ArtifactNodeIDs {
		if source == "" || sources[source] {
			return ErrInvalidComposeDelivery
		}
		sources[source] = true
	}
	if len(config.ComposeImages) == 0 {
		if config.ArtifactNodeID != "" && !sources[config.ArtifactNodeID] {
			return ErrInvalidComposeDelivery
		}
		return nil
	}
	for _, binding := range config.ComposeImages {
		if !composeServicePattern.MatchString(binding.Service) || seen[binding.Service] || binding.ArtifactNodeID == "" || len(binding.ArtifactNodeID) > 64 || !sources[binding.ArtifactNodeID] {
			return ErrInvalidComposeDelivery
		}
		seen[binding.Service] = true
		used[binding.ArtifactNodeID] = true
	}
	primary := false
	for _, binding := range config.ComposeImages {
		primary = primary || binding.ArtifactNodeID == config.ArtifactNodeID
	}
	if !primary || len(sources) != len(used) {
		return ErrInvalidComposeDelivery
	}
	return nil
}

func validateComposeBindingsYAML(value string, config model.WorkflowNodeConfig) error {
	if err := validateComposeNode(config); err != nil {
		return err
	}
	doc, err := parseComposeStack(value)
	if err != nil {
		return err
	}
	services := doc["services"].(map[string]any)
	if _, err := composeServiceScope(doc, config.Compose.Services); err != nil {
		return err
	}
	for _, raw := range services {
		service := raw.(map[string]any)
		if image, ok := service["image"].(string); !ok || strings.TrimSpace(image) == "" {
			return ErrInvalidComposeDelivery
		}
	}
	for _, binding := range config.ComposeImages {
		service, ok := services[binding.Service].(map[string]any)
		if !ok {
			return ErrInvalidComposeDelivery
		}
		image, ok := service["image"].(string)
		if !ok || !strings.Contains(image, "${"+composeTagName(config.Compose.TagVariable)+"}") {
			return ErrInvalidComposeDelivery
		}
	}
	return nil
}

func (s *Service) loadComposeStackArtifacts(ctx context.Context, prepared *executionContext) error {
	if validateComposeNode(prepared.node.Config) != nil || s.docker == nil || (len(prepared.node.Config.ArtifactNodeIDs) > 0 && s.artifacts == nil) {
		return ErrPipelineExecutionConfig
	}
	prepared.composeArtifacts = map[string]model.Artifact{}
	prepared.composeCandidates = map[string]model.Artifact{}
	for _, sourceID := range prepared.node.Config.ArtifactNodeIDs {
		var node model.PipelineNodeRun
		if err := s.db.WithContext(ctx).Where("pipeline_run_id = ? AND project_id = ? AND node_id = ? AND status IN ?", prepared.run.ID, prepared.run.ProjectID, sourceID, []model.PipelineRunStatus{model.PipelineRunSucceeded, model.PipelineRunSkipped}).First(&node).Error; err != nil {
			return err
		}
		stored, err := s.artifacts.Find(ctx, node.ArtifactID)
		if err != nil {
			return err
		}
		checkRun := prepared.run
		checkRun.ArtifactID = stored.ID
		if stored.ApplicationID != prepared.run.ApplicationID || stored.ProjectID != prepared.run.ProjectID || !artifactAvailableToPipelineRun(checkRun, stored) || stored.Status != model.ArtifactStatusAvailable || stored.Kind != model.ArtifactKindOCIImage || stored.StorageKind != model.ArtifactStorageKindRegistry {
			return ErrPipelineExecutionConfig
		}
		if stored.PipelineRunID != prepared.run.ID && !(prepared.run.Trigger == "retry" && prepared.run.RetryOfID != "" && stored.PipelineRunID == prepared.run.RetryOfID) {
			return ErrPipelineExecutionConfig
		}
		var build model.BuildRun
		if err := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND application_id = ? AND pipeline_run_id = ? AND workflow_node_id = ? AND commit_sha = ? AND status = ?", stored.BuildRunID, prepared.run.ProjectID, prepared.run.ApplicationID, stored.PipelineRunID, sourceID, prepared.run.CommitSHA, model.BuildRunStatusSucceeded).First(&build).Error; err != nil {
			return err
		}
		plan, ok := prepared.snapshot.BuildPlans[sourceID]
		if !ok || plan.ID != build.BuildPlanID || plan.ImageRegistryID != stored.ImageRegistryID {
			return ErrPipelineExecutionConfig
		}
		var registry model.ImageRegistry
		if err := s.db.WithContext(ctx).First(&registry, "uuid = ? AND is_active = ?", stored.ImageRegistryID, true).Error; err != nil {
			return err
		}
		if !artifact.RegistryImageMatches(stored.ImageRef, stored.Digest, registry.Endpoint, registry.Namespace) {
			return ErrPipelineExecutionConfig
		}
		prepared.composeCandidates[sourceID] = *stored
		if sourceID == prepared.node.Config.ArtifactNodeID {
			prepared.artifact = *stored
		}
	}
	for _, binding := range prepared.node.Config.ComposeImages {
		image, ok := prepared.composeCandidates[binding.ArtifactNodeID]
		if !ok {
			return ErrPipelineExecutionConfig
		}
		prepared.composeArtifacts[binding.Service] = image
	}
	return nil
}

// renderComposeStack 从已构建制品读取 Tag；Compose 中的镜像表达式必须准确匹配构建结果，执行时再固定为 Digest。
func renderComposeStack(value string, config model.WorkflowNodeConfig, images map[string]model.Artifact, variables map[string]string) (string, string, error) {
	if err := validateComposeNode(config); err != nil {
		return "", "", err
	}
	doc, err := parseComposeStack(value)
	if err != nil {
		return "", "", err
	}
	services := doc["services"].(map[string]any)
	tag := ""
	taggedImages := map[string]string{}
	for _, binding := range config.ComposeImages {
		image, ok := images[binding.Service]
		if !ok {
			return "", "", ErrPipelineExecutionConfig
		}
		name, err := distributionreference.ParseNormalizedNamed(image.Name)
		if err != nil {
			return "", "", ErrPipelineExecutionConfig
		}
		tagged, ok := name.(distributionreference.NamedTagged)
		if !ok || (tag != "" && tag != tagged.Tag()) {
			return "", "", ErrPipelineExecutionConfig
		}
		tag = tagged.Tag()
		// 制品名称用于展示，登记时会去掉仓库路径；完整身份必须来自已校验的摘要引用。
		pinned, err := distributionreference.ParseNormalizedNamed(image.ImageRef)
		if err != nil {
			return "", "", ErrPipelineExecutionConfig
		}
		if _, ok := pinned.(distributionreference.Digested); !ok {
			return "", "", ErrPipelineExecutionConfig
		}
		fullTag, err := distributionreference.WithTag(distributionreference.TrimNamed(pinned), tagged.Tag())
		if err != nil {
			return "", "", ErrPipelineExecutionConfig
		}
		taggedImages[binding.Service] = distributionreference.FamiliarString(fullTag)
	}
	for _, binding := range config.ComposeImages {
		service, ok := services[binding.Service].(map[string]any)
		if !ok {
			return "", "", ErrInvalidComposeDelivery
		}
		template, ok := service["image"].(string)
		if !ok {
			return "", "", ErrInvalidComposeDelivery
		}
		expanded := os.Expand(template, func(name string) string {
			if name == composeTagName(config.Compose.TagVariable) {
				return tag
			}
			return variables[name]
		})
		image := images[binding.Service]
		if expanded != taggedImages[binding.Service] && expanded != image.ImageRef {
			return "", "", ErrPipelineExecutionConfig
		}
		service["image"] = image.ImageRef
	}
	doc["name"] = config.Compose.ProjectName
	encoded, err := yaml.Marshal(doc)
	if err != nil || len(encoded) > maximumComposeStackBytes {
		return "", "", ErrInvalidComposeDelivery
	}
	return string(encoded), tag, nil
}

func (s *Service) executeComposeStackDeployment(ctx context.Context, prepared *executionContext, approvedBy string) error {
	failure := failureStateForRun(prepared.run)
	if len(prepared.node.Config.ComposeImages) == 0 {
		config, images, err := autoComposeImages(prepared.deploymentPlan.ComposeYAML, prepared.node.Config, prepared.composeCandidates, prepared.variables)
		if err != nil {
			return s.failDeploymentExecution(ctx, failure, "Compose 镜像无法自动匹配，请检查镜像引用及上游构建", err)
		}
		prepared.node.Config, prepared.composeArtifacts = config, images
		prepared.artifact = model.Artifact{}
		if len(config.ComposeImages) > 0 {
			prepared.artifact = images[config.ComposeImages[0].Service]
		}
	}
	content, tag, err := renderComposeStack(prepared.deploymentPlan.ComposeYAML, prepared.node.Config, prepared.composeArtifacts, prepared.variables)
	if err != nil {
		return s.failDeploymentExecution(ctx, failure, "Compose 镜像与本次构建的名称或 Tag 不匹配，请检查部署方案和变量", err)
	}
	content, err = pinRegistryComposeImages(content, prepared.snapshot.RegistryInputs[prepared.node.ID], prepared.variables)
	if err != nil {
		return s.failDeploymentExecution(ctx, failure, "无法固定所选镜像仓库的镜像，请检查镜像和 Tag", err)
	}
	var capability model.HostCapability
	if err := s.db.WithContext(ctx).First(&capability, "host_id = ? AND kind = ? AND status = ?", prepared.target.HostID, model.HostCapabilityDocker, model.HostCapabilityReady).Error; err != nil {
		return s.failDeploymentExecution(ctx, failure, "目标主机的 Docker 能力不可用", err)
	}
	for _, image := range prepared.composeArtifacts {
		var registry model.ImageRegistry
		if err := s.db.WithContext(ctx).First(&registry, "uuid = ? AND is_active = ?", image.ImageRegistryID, true).Error; err != nil {
			return s.failDeploymentExecution(ctx, failure, "本次构建的镜像仓库已不可用", err)
		}
		auth, err := s.registryAuth(registry)
		if err != nil {
			return s.failDeploymentExecution(ctx, failure, "镜像仓库认证不可用", err)
		}
		if err := s.requirePipelineExecutionPermission(ctx, prepared.run, prepared.snapshot); err != nil {
			return s.failDeploymentExecution(ctx, failure, "流水线执行授权已失效", err)
		}
		if err := s.docker.PrepareRegistryImage(ctx, capability.RuntimeID, prepared.target.HostID, image.ImageRef, auth); err != nil {
			return s.failDeploymentExecution(ctx, failure, "目标主机无法拉取本次构建的固定镜像", err)
		}
	}
	message := "正在使用 EDO Compose 配置部署"
	if tag != "" {
		message += "镜像 Tag " + tag
	}
	if err := s.updateExecutionPhase(ctx, &prepared.run, "deploy", message); err != nil {
		return s.failDeploymentExecution(ctx, failure, "更新 Compose 发布状态失败", err)
	}
	output := s.newExecutionLogWriter(ctx, prepared.run.ID, "deploy", "Docker Compose", prepared.variableRedactions...)
	defer output.Close()
	content, fileWrites, err := composeStackVariables(content, prepared.node.Config.Compose.ProjectName, prepared.target.WorkingDirectory, prepared.run.ID+"/"+prepared.node.ID, prepared.variables, prepared.variableFiles)
	if err != nil {
		return s.failDeploymentExecution(ctx, failure, "Compose 部署变量配置无效", err)
	}
	content, configurationWrites, err := composeConfiguration(content, prepared.configuration)
	if err != nil {
		return s.failDeploymentExecution(ctx, failure, "Compose 配置文件无效", err)
	}
	fileWrites += configurationWrites
	script, err := composeStackScript(content, prepared.node.Config, prepared.target.WorkingDirectory, prepared.deploymentPlan.TimeoutSeconds, fileWrites)
	if err != nil {
		return s.failDeploymentExecution(ctx, failure, "Compose 部署配置无效", err)
	}
	prepared.deploymentPlan.Script = script
	environment := mergeDeploymentVariables(prepared.variables, map[string]string{"EDO_PIPELINE_RUN_ID": prepared.run.ID, "EDO_APPLICATION_ID": prepared.application.ID, "EDO_APPLICATION_NAME": prepared.application.Name, "EDO_GIT_REF": prepared.run.Ref, "EDO_COMMIT_SHA": prepared.run.CommitSHA, "EDO_DEPLOYMENT_TARGET_ID": prepared.target.ID})
	if tag != "" {
		environment["EDO_IMAGE_TAG"] = tag
		environment[composeTagName(prepared.node.Config.Compose.TagVariable)] = tag
	}
	for name, value := range prepared.variableFiles {
		environment[envtext.TransportKey(name)] = value
	}
	record, err := s.deployments.RequestCommandSnapshotAndRun(ctx, prepared.run.CreatedBy, prepared.target, deployment.CommandRequestInput{UsesDeliveryVariables: len(prepared.snapshot.VariableBindings[prepared.node.ID]) > 0 || prepared.configuration != nil, TargetID: prepared.target.ID, ApplicationID: prepared.application.ID, ArtifactID: prepared.artifact.ID, Image: prepared.artifact.ImageRef, PipelineRunID: prepared.run.ID, WorkflowNodeID: prepared.node.ID, ApprovedBy: approvedBy, DeploymentPlanID: prepared.deploymentPlan.ID, PlanKind: model.DeploymentPlanComposeStack, Script: script, ScriptDigest: model.DeploymentPlanExecutionDigest(model.DeploymentPlanComposeStack, script, prepared.deploymentPlan.TimeoutSeconds), TimeoutSeconds: prepared.deploymentPlan.TimeoutSeconds, Environment: environment, Stdout: output, Stderr: output})
	if err != nil {
		return s.handleDeploymentExecutionError(ctx, failure, deploymentFailureLogMessage(record, "Compose 部署失败，请查看运行日志"), err)
	}
	return s.completeExecution(ctx, prepared, record)
}

func composeStackScript(content string, config model.WorkflowNodeConfig, directory string, timeout int, fileWrites string) (string, error) {
	if validateComposeNode(config) != nil || len(content) > maximumComposeStackBytes || timeout < 60 {
		return "", ErrInvalidComposeDelivery
	}
	settings := config.Compose
	docker := "docker"
	if settings.UseSudo {
		docker = "sudo -n docker"
	}
	args := "--ansi never --project-name " + composeShellLiteral(settings.ProjectName) + " --project-directory " + composeShellLiteral(directory)
	if settings.EnvFile != "" {
		args += " --env-file " + composeShellLiteral(settings.EnvFile)
	}
	extra, err := configurationComposeArguments(config)
	if err != nil {
		return "", err
	}
	args += extra
	doc, err := parseComposeStack(content)
	if err != nil {
		return "", err
	}
	scope, err := composeServiceScope(doc, settings.Services)
	if err != nil {
		return "", err
	}
	services, explicit, expected := []string{}, []string{}, []string{}
	for _, name := range settings.Services {
		explicit = append(explicit, composeShellLiteral(name))
	}
	for _, name := range scope {
		services = append(services, composeShellLiteral(name))
		service := doc["services"].(map[string]any)[name].(map[string]any)
		image, ok := service["image"].(string)
		if !ok {
			return "", ErrInvalidComposeDelivery
		}
		if !strings.Contains(image, "$") {
			expected = append(expected, composeShellLiteral(name)+") printf '%s\\n' "+composeShellLiteral(image)+";;")
		}
	}
	marker := "EDO_COMPOSE_" + strings.TrimPrefix(sha256Digest([]byte(content)), "sha256:")
	for strings.Contains(content, "\n"+marker+"\n") {
		marker += "_"
	}
	health := ":"
	if settings.HealthURL != "" {
		health = "curl --fail --silent --show-error --max-time 10 --output /dev/null " + composeShellLiteral(settings.HealthURL)
	}
	script := strings.NewReplacer("__EDO_UP_SERVICES__", strings.Join(explicit, " "), "__EDO_WRITE_ENV_FILES__", fileWrites, "__EDO_DOCKER__", docker, "__EDO_COMPOSE_ARGS__", args, "__EDO_PROJECT_NAME__", composeShellLiteral(settings.ProjectName), "__EDO_EXPECTED_IMAGES__", strings.Join(expected, " "), "__EDO_SERVICES__", strings.Join(services, " "), "__EDO_TIMEOUT__", fmt.Sprint(timeout-30), "__EDO_HEALTH_CHECK__", health, "__EDO_ADOPT_EXISTING__", fmt.Sprint(settings.AdoptExisting), "__EDO_WRITE_COMPOSE__", "cat >\"$release/compose.yml\" <<'"+marker+"'\n"+content+"\n"+marker+"\n").Replace(composeStackTemplate)
	return script, nil
}
