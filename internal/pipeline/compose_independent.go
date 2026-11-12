package pipeline

import (
	"context"
	"errors"
	"strings"
	"time"

	"edo/internal/model"
	composetemplate "github.com/compose-spec/compose-go/v2/template"
	"github.com/distribution/reference"
	regclient "github.com/regclient/regclient"
	registryreference "github.com/regclient/regclient/types/ref"
	"go.yaml.in/yaml/v3"
)

func independentComposeConfig(config model.WorkflowNodeConfig) model.WorkflowNodeConfig {
	config.ArtifactNodeID, config.ArtifactBuildPlanID = "", ""
	config.ArtifactNodeIDs, config.ComposeImages = nil, nil
	return config
}

// 变量只提供流水线上下文，不用来判定部署镜像是否对应上游构建。
func (s *Service) addDeploymentPipelineVariables(ctx context.Context, prepared *executionContext) error {
	if prepared.variables == nil {
		prepared.variables = map[string]string{}
	}
	for key, value := range pipelineCommandEnvironment(&buildExecutionContext{run: prepared.run, application: prepared.application}) {
		if strings.HasPrefix(key, "EDO_") {
			prepared.variables[key] = value
		}
	}
	prepared.variables["EDO_APPLICATION_NAME"] = prepared.application.Name
	prepared.variables["EDO_DEPLOYMENT_TARGET_ID"] = prepared.target.ID
	delete(prepared.variables, "EDO_IMAGE_TAG")
	delete(prepared.variables, "IMAGE_TAG")
	variables := composetemplate.ExtractVariables(map[string]any{"compose": prepared.deploymentPlan.ComposeYAML, "script": prepared.node.Config.DeploymentScript, "plan_script": prepared.deploymentPlan.Script}, nil)
	_, tagReferenced := variables["EDO_IMAGE_TAG"]
	_, legacyTagReferenced := variables["IMAGE_TAG"]
	if !tagReferenced && !legacyTagReferenced && prepared.node.Config.DeploymentParameters == "" {
		return nil
	}
	var prior []string
	for _, stage := range prepared.snapshot.Stages {
		current := false
		for _, node := range stage.Tasks {
			current = current || node.ID == prepared.node.ID
		}
		if current {
			break
		}
		for _, node := range stage.Tasks {
			if node.Type == model.WorkflowNodeBuild && prepared.snapshot.BuildPlans[node.ID].Kind == model.BuildPlanDockerfile {
				prior = append(prior, node.ID)
			}
		}
	}
	if len(prior) == 0 {
		return nil
	}
	var images []model.Artifact
	err := s.db.WithContext(ctx).Model(&model.Artifact{}).Select("artifacts.name").
		Joins("JOIN pipeline_node_runs ON pipeline_node_runs.artifact_id = artifacts.uuid").
		Where("pipeline_node_runs.pipeline_run_id = ? AND pipeline_node_runs.project_id = ? AND pipeline_node_runs.node_id IN ? AND pipeline_node_runs.status IN ?", prepared.run.ID, prepared.run.ProjectID, prior, []model.PipelineRunStatus{model.PipelineRunSucceeded, model.PipelineRunSkipped}).
		Where("artifacts.project_id = ? AND artifacts.application_id = ? AND artifacts.kind = ? AND artifacts.status = ?", prepared.run.ProjectID, prepared.run.ApplicationID, model.ArtifactKindOCIImage, model.ArtifactStatusAvailable).Find(&images).Error
	if err != nil {
		s.logger.ErrorContext(ctx, "读取部署流水线变量失败", "operation", "deployment_pipeline_variables", "node_id", prepared.node.ID, "err", err)
		return ErrPipelineExecutionConfig
	}
	tags := map[string]bool{}
	for _, image := range images {
		name, err := reference.ParseNormalizedNamed(image.Name)
		if err != nil {
			continue
		}
		if tagged, ok := name.(reference.NamedTagged); ok {
			tags[tagged.Tag()] = true
		}
	}
	// 无镜像或不同构建使用不同 Tag 时不猜测；用户仍可使用自己的变量和固定 Tag。
	if len(tags) == 1 {
		for tag := range tags {
			prepared.variables["EDO_IMAGE_TAG"] = tag
			prepared.variables["IMAGE_TAG"] = tag
		}
	}
	return nil
}

// 只读取用户的 image，并使用用户保存的仓库认证拉取；不读取上游制品来匹配镜像。
func (s *Service) resolveConfiguredComposeImages(ctx context.Context, prepared *executionContext, source string) (string, error) {
	fail := func(operation string, err error) (string, error) {
		// 插值结果可能包含密钥，诊断信息复用流水线日志的脱敏规则。
		diagnostic := string(redactLogBytes([]byte(err.Error()), normalizeLogRedactions(prepared.variableRedactions)))
		return "", s.deploymentInputError(prepared.node.ID, operation, errors.New(diagnostic))
	}
	doc, err := parseComposeStack(source)
	if err != nil {
		return "", err
	}
	services, err := composeServiceScope(doc, prepared.node.Config.Compose.Services)
	if err != nil {
		return "", err
	}
	interpolation := composeInterpolationValues(prepared)
	var registry model.ImageRegistry
	if err = s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND is_active = ?", prepared.node.Config.DeploymentRegistryID, prepared.run.ProjectID, true).First(&registry).Error; err != nil {
		s.logger.WarnContext(ctx, "读取部署配置的镜像仓库失败", "operation", "compose_configured_registry", "node_id", prepared.node.ID, "err", err)
		return "", ErrDeploymentRegistryUnavailable
	}
	client, prefix, err := s.deploymentRegistryClient(registry)
	if err != nil {
		return fail("compose_configured_registry_client", err)
	}
	prepared.composeArtifacts = map[string]model.Artifact{}
	frozen, err := registryInputImageBindings(prepared.snapshot.RegistryInputs[prepared.node.ID])
	if err != nil {
		return fail("compose_configured_frozen_images", err)
	}
	for _, service := range services {
		entry := doc["services"].(map[string]any)[service].(map[string]any)
		template, _ := entry["image"].(string)
		expanded, interpolationErr := composetemplate.SubstituteWithOptions(template, func(key string) (string, bool) {
			value, ok := interpolation[key]
			return value, ok
		}, composetemplate.WithoutLogging)
		if interpolationErr != nil {
			return fail("compose_configured_interpolation", ErrDeploymentInputs)
		}
		name, parseErr := reference.ParseNormalizedNamed(expanded)
		if parseErr != nil {
			return fail("compose_configured_image", parseErr)
		}
		// 其他仓库的镜像由用户的 Compose 配置处理，不向其传送当前仓库凭据。
		if !strings.HasPrefix(reference.TrimNamed(name).Name(), strings.TrimSuffix(prefix, "/")+"/") {
			continue
		}
		// 显式镜像来源已在运行创建时冻结；Tag 后续变化不能覆盖该次选择。
		if pinned := frozen[reference.TagNameOnly(name).String()]; pinned != "" {
			entry["image"] = pinned
			prepared.composeArtifacts[service] = model.Artifact{ImageRef: pinned, ImageRegistryID: registry.ID}
			continue
		}
		ref, parseErr := registryreference.New(reference.TagNameOnly(name).String())
		if parseErr != nil {
			return fail("compose_configured_reference", parseErr)
		}
		if ref.Digest == "" {
			request, cancel := context.WithTimeout(ctx, 20*time.Second)
			manifest, headErr := client.ManifestHead(request, ref, regclient.WithManifestRequireDigest())
			cancel()
			if headErr != nil {
				return fail("compose_configured_image_digest", headErr)
			}
			ref.Tag, ref.Digest = "", manifest.GetDescriptor().Digest.String()
		}
		if !strings.HasPrefix(ref.Digest, "sha256:") {
			return fail("compose_configured_image_digest", ErrDeploymentInputs)
		}
		entry["image"] = ref.CommonName()
		prepared.composeArtifacts[service] = model.Artifact{ImageRef: ref.CommonName(), ImageRegistryID: registry.ID}
	}
	encoded, err := yaml.Marshal(doc)
	if err != nil || len(encoded) > maximumComposeStackBytes {
		return "", ErrInvalidComposeDelivery
	}
	return string(encoded), nil
}
