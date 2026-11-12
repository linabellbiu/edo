package pipeline

import (
	"context"
	"fmt"
	"io"
	"strings"

	distributionreference "github.com/distribution/reference"

	"edo/internal/configfile"
	"edo/internal/deployment"
	"edo/internal/model"
	"edo/internal/sshdeploy"
)

func referencesPipelineImageTag(script string) bool {
	return strings.Contains(script, "${EDO_IMAGE_TAG}") || strings.Contains(script, "${IMAGE_TAG}")
}

func (s *Service) prepareDeploymentInputs(ctx context.Context, prepared *executionContext) ([]io.Closer, error) {
	closers := []io.Closer{}
	if prepared.node.Config.DeploymentInputVersion != 1 {
		return closers, nil
	}
	if !prepared.snapshot.IndependentDeploymentImages && referencesPipelineImageTag(prepared.node.Config.DeploymentScript) && prepared.deploymentPlan.Kind != model.DeploymentPlanComposeStack {
		prior := []string{}
		found := false
		for _, stage := range prepared.snapshot.Stages {
			for _, node := range stage.Tasks {
				if node.ID == prepared.node.ID {
					found = true
				}
			}
			if found {
				break
			}
			for _, node := range stage.Tasks {
				if node.Type == model.WorkflowNodeBuild && prepared.snapshot.BuildPlans[node.ID].Kind == model.BuildPlanDockerfile {
					if prepared.snapshot.ExplicitDeploymentRegistries && model.RequiresDeploymentRegistry(prepared.deploymentPlan.Kind) && prepared.snapshot.BuildPlans[node.ID].ImageRegistryID != prepared.node.Config.DeploymentRegistryID {
						continue
					}
					prior = append(prior, node.ID)
				}
			}
		}
		if prepared.composeCandidates == nil {
			prepared.composeCandidates = map[string]model.Artifact{}
		}
		for _, nodeID := range prior {
			var node model.PipelineNodeRun
			if err := s.db.WithContext(ctx).Where("pipeline_run_id = ? AND project_id = ? AND node_id = ? AND status IN ?", prepared.run.ID, prepared.run.ProjectID, nodeID, []model.PipelineRunStatus{model.PipelineRunSucceeded, model.PipelineRunSkipped}).First(&node).Error; err != nil {
				return closers, s.deploymentInputError(prepared.node.ID, "deployment_script_image_source", err)
			}
			image, err := s.artifacts.Find(ctx, node.ArtifactID)
			if err != nil {
				return closers, s.deploymentInputError(prepared.node.ID, "deployment_script_image_artifact", err)
			}
			check := prepared.run
			check.ArtifactID = image.ID
			if image.ApplicationID != prepared.run.ApplicationID || image.ProjectID != prepared.run.ProjectID || image.Kind != model.ArtifactKindOCIImage || !artifactAvailableToPipelineRun(check, image) {
				return closers, ErrDeploymentInputs
			}
			if prepared.snapshot.ExplicitDeploymentRegistries && model.RequiresDeploymentRegistry(prepared.deploymentPlan.Kind) && (image.StorageKind != model.ArtifactStorageKindRegistry || image.ImageRegistryID != prepared.node.Config.DeploymentRegistryID) {
				return closers, s.deploymentInputError(prepared.node.ID, "deployment_script_saved_registry", ErrDeploymentInputs)
			}
			prepared.composeCandidates[nodeID] = *image
		}
	}
	directory := prepared.snapshot.ApplicationConfigDirectory
	if !configfile.ValidDirectory(directory) {
		return closers, ErrDeploymentInputs
	}
	for _, input := range prepared.node.Config.DeploymentInputs {
		if input.Kind != "upstream" {
			continue
		}
		var node model.PipelineNodeRun
		if err := s.db.WithContext(ctx).Where("pipeline_run_id = ? AND project_id = ? AND node_id = ? AND status IN ?", prepared.run.ID, prepared.run.ProjectID, input.ArtifactNodeID, []model.PipelineRunStatus{model.PipelineRunSucceeded, model.PipelineRunSkipped}).First(&node).Error; err != nil {
			return closers, s.deploymentInputError(prepared.node.ID, "deployment_input_upstream", err)
		}
		if s.artifacts == nil {
			return closers, ErrPipelineExecutionUnavailable
		}
		stored, err := s.artifacts.Find(ctx, node.ArtifactID)
		if err != nil {
			return closers, s.deploymentInputError(prepared.node.ID, "deployment_input_artifact", err)
		}
		check := prepared.run
		check.ArtifactID = stored.ID
		if stored.ProjectID != prepared.run.ProjectID || stored.ApplicationID != prepared.run.ApplicationID || !artifactAvailableToPipelineRun(check, stored) || stored.Kind != model.ArtifactKindFileBundle || stored.Status != model.ArtifactStatusAvailable {
			return closers, s.deploymentInputError(prepared.node.ID, "deployment_input_identity", ErrDeploymentInputs)
		}
		var build model.BuildRun
		if err := s.db.WithContext(ctx).Where("uuid = ? AND pipeline_run_id = ? AND workflow_node_id = ? AND commit_sha = ? AND status = ?", stored.BuildRunID, stored.PipelineRunID, input.ArtifactNodeID, prepared.run.CommitSHA, model.BuildRunStatusSucceeded).First(&build).Error; err != nil {
			return closers, s.deploymentInputError(prepared.node.ID, "deployment_input_producer", err)
		}
		_, rawContent, err := s.artifacts.OpenDownload(ctx, stored.ID)
		var content io.ReadCloser = rawContent
		if err != nil {
			return closers, s.deploymentInputError(prepared.node.ID, "deployment_input_open", err)
		}
		digest := stored.Digest
		if input.ArtifactName != "" {
			_ = content.Close()
			content, digest, err = s.artifacts.OpenBundleMember(ctx, stored.ID, input.ArtifactName+".tar.gz")
			if err != nil {
				return closers, s.deploymentInputError(prepared.node.ID, "deployment_input_output", err)
			}
			if prepared.variables == nil {
				prepared.variables = map[string]string{}
			}
			// 命名构建物的路径在配置目录解析后由脚本前缀导出。
		}
		closers = append(closers, content)
		prepared.inputFiles = append(prepared.inputFiles, sshdeploy.FileInput{Directory: directory, Path: input.Path, Digest: digest, Content: content})
	}
	for _, image := range prepared.snapshot.RegistryInputs[prepared.node.ID] {
		if image.Index < 0 || image.Index >= len(prepared.node.Config.DeploymentInputs) {
			return closers, ErrDeploymentInputs
		}
		input := prepared.node.Config.DeploymentInputs[image.Index]
		if input.Kind != "registry" || input.RegistryID != image.RegistryID || input.Tag != image.Tag {
			return closers, ErrDeploymentInputs
		}
		if err := s.requireDeliveryPermission(ctx, prepared.snapshot.AuthorityID, "image_registry.execute"); err != nil {
			return closers, err
		}
		var registry model.ImageRegistry
		if err := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND is_active = ?", image.RegistryID, prepared.run.ProjectID, true).First(&registry).Error; err != nil {
			return closers, s.deploymentInputError(prepared.node.ID, "deployment_input_registry", err)
		}
		auth, err := s.registryAuth(registry)
		if err != nil {
			return closers, s.deploymentInputError(prepared.node.ID, "deployment_input_registry_auth", err)
		}
		var capability model.HostCapability
		if s.docker == nil || s.db.WithContext(ctx).Where("host_id = ? AND kind = ? AND status = ?", prepared.target.HostID, model.HostCapabilityDocker, model.HostCapabilityReady).First(&capability).Error != nil {
			return closers, s.deploymentInputError(prepared.node.ID, "deployment_input_docker", ErrDeploymentInputs)
		}
		if err := s.requirePipelineExecutionPermission(ctx, prepared.run, prepared.snapshot); err != nil {
			return closers, err
		}
		if err := s.docker.PrepareRegistryImage(ctx, capability.RuntimeID, prepared.target.HostID, image.ImageRef, auth); err != nil {
			return closers, s.deploymentInputError(prepared.node.ID, "deployment_input_pull", err)
		}
		if prepared.variables == nil {
			prepared.variables = map[string]string{}
		}
		prepared.variables[fmt.Sprintf("EDO_INPUT_IMAGE_%d", image.Index+1)] = image.ImageRef
		s.appendRunLog(ctx, prepared.run.ID, "prepare", "info", fmt.Sprintf("镜像来源 %d 已固定并拉取 %s", image.Index+1, image.ImageRef))
	}
	return closers, nil
}

func deploymentScriptEnvironment(prepared *executionContext) (map[string]string, error) {
	values := mergeDeploymentVariables(prepared.node.Config.EnvironmentVariables, prepared.variables)
	values = mergeDeploymentVariables(values, map[string]string{
		"EDO_PIPELINE_RUN_ID": prepared.run.ID, "EDO_APPLICATION_ID": prepared.application.ID,
		"EDO_APPLICATION_NAME": prepared.application.Name, "EDO_GIT_REF": prepared.run.Ref,
		"EDO_GIT_TAG":    pipelineGitTag(prepared.run),
		"EDO_COMMIT_SHA": prepared.run.ShortCommitSHA(), "EDO_DEPLOYMENT_TARGET_ID": prepared.target.ID,
	})
	delete(values, "EDO_IMAGE_TAG")
	delete(values, "IMAGE_TAG")
	images := []model.Artifact{}
	if prepared.artifact.Kind == model.ArtifactKindOCIImage && prepared.artifact.PipelineRunID != "" {
		images = append(images, prepared.artifact)
	}
	for _, image := range prepared.composeCandidates {
		images = append(images, image)
	}
	tag := ""
	ambiguous := false
	for _, image := range images {
		ref, err := distributionreference.ParseNormalizedNamed(image.Name)
		if err != nil {
			return nil, ErrDeploymentInputs
		}
		tagged, ok := ref.(distributionreference.NamedTagged)
		if !ok {
			return nil, ErrDeploymentInputs
		}
		if tag != "" && tag != tagged.Tag() {
			ambiguous = true
		}
		tag = tagged.Tag()
	}
	if ambiguous {
		tag = ""
	}
	if prepared.snapshot.IndependentDeploymentImages {
		tag = prepared.variables["EDO_IMAGE_TAG"]
	}
	if tag != "" {
		values["EDO_IMAGE_TAG"] = tag
		// 已保存的脚本及运行快照继续按原变量读取本次制品。
		if !prepared.snapshot.ImageTagIncludesName {
			values["IMAGE_TAG"] = tag
		}
		if alias := composeTagName(prepared.node.Config.DeploymentTagReference); alias != "" && !prepared.snapshot.IndependentDeploymentImages {
			values[alias] = tag
		}
	}
	if referencesPipelineImageTag(prepared.node.Config.DeploymentScript) && tag == "" {
		return nil, ErrDeploymentInputs
	}
	if prepared.artifact.ImageRef != "" {
		values["EDO_IMAGE"] = prepared.artifact.ImageRef
		if prepared.artifact.StorageKind == model.ArtifactStorageKindDockerDaemon {
			values["EDO_IMAGE"] = prepared.artifact.LocalImageID
		}
	}
	return values, nil
}

func (s *Service) executeCustomDeployment(ctx context.Context, prepared *executionContext, approvedBy string) error {
	failure := failureStateForRun(prepared.run)
	script := prepared.node.Config.DeploymentScript
	if strings.TrimSpace(script) == "" {
		script = prepared.deploymentPlan.Script
	}
	paths := []string{}
	if prepared.configuration != nil {
		for _, binding := range prepared.configuration.Bindings {
			paths = append(paths, binding.Path)
		}
	}
	prefix, err := configfile.WriteScript(prepared.snapshot.ApplicationConfigDirectory, paths)
	if err != nil {
		return s.failDeploymentExecution(ctx, failure, "应用配置路径不可用", err)
	}
	if prepared.artifact.Kind == model.ArtifactKindOCIImage && prepared.artifact.PipelineRunID != "" {
		image := prepared.artifact.ImageRef
		var imageErr error
		if prepared.target.Platform == model.DeploymentDocker {
			image, _, imageErr = s.resolveDeploymentImage(ctx, prepared)
		}
		if imageErr != nil {
			return s.failDeploymentExecution(ctx, failure, "准备本次构建镜像失败", imageErr)
		}
		if prepared.artifact.StorageKind == model.ArtifactStorageKindRegistry {
			var capability model.HostCapability
			if err := s.db.WithContext(ctx).Where("host_id = ? AND kind = ? AND status = ?", prepared.target.HostID, model.HostCapabilityDocker, model.HostCapabilityReady).First(&capability).Error; err != nil {
				return s.failDeploymentExecution(ctx, failure, "目标主机 Docker 能力不可用", err)
			}
			var registry model.ImageRegistry
			if err := s.db.WithContext(ctx).Where("uuid = ? AND is_active = ?", prepared.artifact.ImageRegistryID, true).First(&registry).Error; err != nil {
				return s.failDeploymentExecution(ctx, failure, "本次构建镜像仓库不可用", err)
			}
			auth, err := s.registryAuth(registry)
			if err != nil {
				return s.failDeploymentExecution(ctx, failure, "读取镜像仓库认证失败", err)
			}
			if err := s.requirePipelineExecutionPermission(ctx, prepared.run, prepared.snapshot); err != nil {
				return s.failDeploymentExecution(ctx, failure, "部署执行授权已失效", err)
			}
			if err := s.docker.PrepareRegistryImage(ctx, capability.RuntimeID, prepared.target.HostID, image, auth); err != nil {
				return s.failDeploymentExecution(ctx, failure, "目标主机无法拉取本次构建镜像", err)
			}
		}
	}
	environment, err := deploymentScriptEnvironment(prepared)
	if err != nil {
		return s.failDeploymentExecution(ctx, failure, "部署脚本引用的镜像 Tag 不可用，请检查本次流水线构建产物", err)
	}
	if prepared.target.WorkingDirectory == "" {
		prefix += "cd \"${EDO_APP_CONFIG_PATH:?缺少应用配置目录}\"\n"
	}
	for _, input := range prepared.node.Config.DeploymentInputs {
		if input.Kind == "upstream" && input.ArtifactName != "" {
			prefix += "export " + input.ArtifactName + "=\"${EDO_APP_CONFIG_PATH}/" + input.Path + "\"\n"
		}
	}
	script = "# EDO 用户部署脚本 v1\n" + prefix + script
	prepared.deploymentPlan.Script = script
	output := s.newExecutionLogWriter(ctx, prepared.run.ID, "deploy", "部署脚本", prepared.variableRedactions...)
	defer output.Close()
	if err := s.updateExecutionPhase(ctx, &prepared.run, "deploy", "正在执行部署脚本"); err != nil {
		return s.failDeploymentExecution(ctx, failure, "更新部署状态失败", err)
	}
	image := prepared.artifact.ImageRef
	if prepared.deploymentPlan.Kind == model.DeploymentPlanScript {
		image = ""
	}
	record, err := s.deployments.RequestCommandSnapshotAndRun(ctx, prepared.run.CreatedBy, prepared.target, deployment.CommandRequestInput{
		UsesDeliveryVariables: len(prepared.snapshot.VariableBindings[prepared.node.ID]) > 0 || prepared.configuration != nil,
		TargetID:              prepared.target.ID, ApplicationID: prepared.application.ID, ArtifactID: prepared.artifact.ID, Image: image,
		PipelineRunID: prepared.run.ID, WorkflowNodeID: prepared.node.ID, ApprovedBy: approvedBy,
		DeploymentPlanID: prepared.deploymentPlan.ID, PlanKind: prepared.deploymentPlan.Kind, Script: script,
		ScriptDigest:   model.DeploymentPlanExecutionDigest(prepared.deploymentPlan.Kind, script, prepared.deploymentPlan.TimeoutSeconds),
		TimeoutSeconds: prepared.deploymentPlan.TimeoutSeconds, Environment: environment, Stdout: output, Stderr: output,
	})
	if err != nil {
		return s.handleDeploymentExecutionError(ctx, failure, deploymentFailureLogMessage(record, "部署脚本执行失败，请查看任务日志"), err)
	}
	return s.completeExecution(ctx, prepared, record)
}
