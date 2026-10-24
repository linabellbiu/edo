package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"edo/internal/artifact"
	"edo/internal/cicd"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/repository"
)

type buildExecutionContext struct {
	run         model.PipelineRun
	node        model.WorkflowNode
	snapshot    workflowSnapshot
	application model.Application
	component   model.PipelineRunRepository
	plan        workflowBuildPlanSnapshot
}

type scriptContainerRunner interface {
	RunScriptContainer(context.Context, dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error)
}

type buildTaskExecutionError struct {
	cause     error
	retryable bool
}

func (e *buildTaskExecutionError) Error() string { return e.cause.Error() }
func (e *buildTaskExecutionError) Unwrap() error { return e.cause }

// completedBuildAdvanceError 表示构建或脚本本身已经成功并持久化，只剩下推进流水线失败。
// 这个阶段可以安全重试：下一次只会恢复 task_succeeded 状态，不会再次执行用户脚本。
type completedBuildAdvanceError struct{ cause error }

func (e *completedBuildAdvanceError) Error() string { return e.cause.Error() }
func (e *completedBuildAdvanceError) Unwrap() error { return e.cause }

func IsRetryableBuildTaskError(err error) bool {
	var executionError *buildTaskExecutionError
	return errors.As(err, &executionError) && executionError.retryable
}

func (s *Service) executeBuildTask(ctx context.Context, payload BuildTaskPayload, jobID string) error {
	defer s.realtime.Changed(ctx, "pipeline_runs", payload.PipelineRunID)
	prepared, err := s.loadBuildExecution(ctx, payload, jobID)
	if err != nil {
		if errors.Is(err, errPipelineExecutionComplete) {
			if s.hasNodeJob(ctx, payload.PipelineRunID, jobID) {
				return s.resumeNodeAdvance(ctx, payload.PipelineRunID)
			}
			return s.resumeCompletedBuildTask(ctx, payload, jobID)
		}
		if obsolete, obsoleteErr := s.executionTaskObsolete(ctx, payload.PipelineRunID, jobID, payload.WorkflowNodeID); obsoleteErr == nil && obsolete {
			return nil
		}
		return s.finishBuildTaskFailure(ctx,
			taskFailureState(payload.PipelineRunID, jobID, payload.WorkflowNodeID, model.PipelineRunRunning), false,
			"构建任务配置不完整，请检查流水线和构建方案", err)
	}
	ctx = withPipelineNodeLog(ctx, prepared.node.ID, prepared.node.Name)
	runningState := failureStateForRun(prepared.run)
	if prepared.node.Type == model.WorkflowNodeBuild {
		recovered, recoverErr := s.recoverCompletedBuildArtifact(ctx, prepared)
		if recoverErr != nil {
			return s.finishBuildTaskFailure(ctx, runningState, false,
				"恢复已完成的构建制品失败，请重新执行流水线", recoverErr)
		}
		if recovered != nil {
			s.appendRunLog(ctx, prepared.run.ID, "build", "info", "检测到当前任务已登记的不可变制品，跳过重复构建并继续流水线")
			if err := s.completeBuildExecution(ctx, prepared, recovered); err != nil {
				var advanceError *completedBuildAdvanceError
				if errors.As(err, &advanceError) {
					return s.finishBuildTaskFailure(ctx,
						taskFailureState(prepared.run.ID, jobID, prepared.node.ID, model.PipelineRunReady), true,
						"任务已完成，但推进后续任务失败", err)
				}
				return s.finishBuildTaskFailure(ctx, runningState, false, "记录任务结果失败", err)
			}
			return nil
		}
	}
	if err := s.updateExecutionPhase(ctx, &prepared.run, "checkout", "正在准备已固定 Commit 的隔离工作区"); err != nil {
		return s.finishBuildTaskFailure(ctx, runningState, buildTaskCanRetry(prepared), "更新构建任务状态失败", err)
	}
	var checkout *repository.CheckoutLease
	if prepared.run.NodeExecution {
		checkout, err = s.repositories.AcquireTaskCheckout(ctx, prepared.component.RepositoryID, pipelineRunCheckoutRef(prepared.run), prepared.run.CommitSHA, prepared.run.ID+":"+prepared.node.ID)
	} else {
		checkout, err = s.repositories.AcquireCheckout(ctx, prepared.component.RepositoryID, pipelineRunCheckoutRef(prepared.run), prepared.run.CommitSHA)
	}
	if err != nil {
		return s.finishBuildTaskFailure(ctx, runningState, buildTaskCanRetry(prepared),
			"获取代码失败，请检查仓库连接和所选 Commit", err)
	}
	defer checkout.Release()
	workspace := checkout.Directory
	if !checkout.RepositoryCached {
		s.appendRunLog(ctx, prepared.run.ID, "checkout", "info", "首次 Clone 仓库并准备独立版本工作区 "+checkout.Version)
	} else if checkout.Cached {
		s.appendRunLog(ctx, prepared.run.ID, "checkout", "info", "已命中并复用本地版本工作区 "+checkout.Version)
	} else {
		s.appendRunLog(ctx, prepared.run.ID, "checkout", "info", "已 Pull 更新并准备独立版本工作区 "+checkout.Version)
	}
	var produced *model.Artifact
	switch prepared.node.Type {
	case model.WorkflowNodeShell:
		if prepared.node.Config.Migration != nil {
			err = s.executeDatabaseTask(ctx, prepared, workspace)
			break
		}
		_, err = s.executePipelineShell(ctx, prepared, workspace, prepared.node.Config.RuntimeImage, prepared.node.Config.Script,
			prepared.node.Config.WorkingDirectory, prepared.node.Config.EnvironmentVariables,
			prepared.node.Config.TimeoutSeconds, "shell", "脚本", "", "")
	case model.WorkflowNodeBuild:
		produced, err = s.executePipelineBuild(ctx, prepared, workspace)
	default:
		err = ErrPipelineExecutionConfig
	}
	if err != nil {
		return s.finishBuildTaskFailure(ctx, runningState, buildTaskCanRetry(prepared),
			"任务执行失败，请查看流水线日志", err)
	}
	if err := s.completeBuildExecution(ctx, prepared, produced); err != nil {
		var advanceError *completedBuildAdvanceError
		if errors.As(err, &advanceError) {
			return s.finishBuildTaskFailure(ctx,
				taskFailureState(prepared.run.ID, jobID, prepared.node.ID, model.PipelineRunReady), true,
				"任务已完成，但推进后续任务失败", err)
		}
		return s.finishBuildTaskFailure(ctx, runningState, buildTaskCanRetry(prepared), "记录任务结果失败", err)
	}
	return nil
}

func pipelineRunCheckoutRef(run model.PipelineRun) string {
	if normalizePullRequestAction(run.TriggerAction) == "merged" {
		targetBranch := strings.TrimSpace(strings.TrimPrefix(run.TargetBranch, "refs/heads/"))
		if targetBranch != "" {
			return "refs/heads/" + targetBranch
		}
	}
	return run.Ref
}

func (s *Service) recoverCompletedBuildArtifact(
	ctx context.Context,
	prepared *buildExecutionContext,
) (*model.Artifact, error) {
	if prepared == nil || prepared.node.Type != model.WorkflowNodeBuild {
		return nil, nil
	}
	planJSON, err := json.Marshal(prepared.plan)
	if err != nil {
		return nil, ErrPipelineExecutionConfig
	}
	producer := model.BuildRunProducerScript
	expectedKind := model.ArtifactKindFileBundle
	expectedStorage := model.ArtifactStorageKindLocalFile
	if prepared.plan.Kind == model.BuildPlanDockerfile {
		producer = model.BuildRunProducerDockerfile
		expectedKind = model.ArtifactKindOCIImage
		if prepared.plan.ImageRegistryID == "" {
			expectedStorage = model.ArtifactStorageKindDockerDaemon
		} else {
			expectedStorage = model.ArtifactStorageKindRegistry
		}
	} else if prepared.plan.Kind != model.BuildPlanScript {
		return nil, ErrPipelineExecutionConfig
	}
	var build model.BuildRun
	err = s.db.WithContext(ctx).
		Where("pipeline_run_id = ? AND workflow_node_id = ?", prepared.run.ID, prepared.node.ID).
		First(&build).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if build.Status != model.BuildRunStatusSucceeded || build.ApplicationID != prepared.application.ID ||
		build.RepositoryID != prepared.component.RepositoryID || build.BuildPlanID != prepared.plan.ID ||
		build.ProducerKind != producer || build.Ref != prepared.run.Ref || build.CommitSHA != prepared.run.CommitSHA ||
		build.PlanSnapshot != string(planJSON) || build.PlanDigest != sha256Digest(planJSON) {
		return nil, artifact.ErrArtifactConflict
	}
	var existing model.Artifact
	if err := s.db.WithContext(ctx).Where("build_run_id = ?", build.ID).First(&existing).Error; err != nil {
		return nil, err
	}
	if existing.Status != model.ArtifactStatusAvailable || existing.ApplicationID != prepared.application.ID ||
		existing.PipelineRunID != prepared.run.ID || existing.Kind != expectedKind || existing.StorageKind != expectedStorage ||
		(expectedStorage == model.ArtifactStorageKindRegistry && existing.ImageRegistryID != prepared.plan.ImageRegistryID) {
		return nil, artifact.ErrArtifactConflict
	}
	return &existing, nil
}

func (s *Service) resumeCompletedBuildTask(ctx context.Context, payload BuildTaskPayload, jobID string) error {
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "uuid = ? AND execution_job_id = ?", payload.PipelineRunID, jobID).Error; err != nil {
		if obsolete, obsoleteErr := s.executionTaskObsolete(ctx, payload.PipelineRunID, jobID, payload.WorkflowNodeID); obsoleteErr == nil && obsolete {
			return nil
		}
		return s.finishBuildTaskFailure(ctx,
			taskFailureState(payload.PipelineRunID, jobID, payload.WorkflowNodeID, model.PipelineRunReady), true,
			"读取已完成任务状态失败", err)
	}
	if run.Status == model.PipelineRunSucceeded {
		return nil
	}
	if run.Status != model.PipelineRunReady || run.Stage != "task_succeeded" || run.CurrentNodeID != payload.WorkflowNodeID {
		if obsolete, obsoleteErr := s.executionTaskObsolete(ctx, payload.PipelineRunID, jobID, payload.WorkflowNodeID); obsoleteErr == nil && obsolete {
			return nil
		}
		return s.finishBuildTaskFailure(ctx, failureStateForRun(run), false,
			"构建任务状态不一致，请重新执行流水线", ErrInvalidWorkflowTransition)
	}
	advanceContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := s.AdvanceRun(advanceContext, run.ID, run.CreatedBy, ""); err != nil {
		return s.finishBuildTaskFailure(ctx, failureStateForRun(run), true, "任务已完成，但推进后续任务失败",
			&completedBuildAdvanceError{cause: err})
	}
	return nil
}

func buildTaskCanRetry(prepared *buildExecutionContext) bool {
	return prepared != nil && prepared.node.Type == model.WorkflowNodeBuild && prepared.plan.Kind == model.BuildPlanDockerfile
}

func (s *Service) finishBuildTaskFailure(
	ctx context.Context,
	expected executionFailureState,
	allowRetry bool,
	message string,
	cause error,
) error {
	retryable := allowRetry && transientBuildError(cause)
	if s.hasNodeJob(ctx, expected.runID, expected.jobID) {
		return s.finishNodeFailure(ctx, expected, allowRetry, message, cause)
	}
	if retryable {
		var job model.Job
		if err := s.db.WithContext(context.WithoutCancel(ctx)).First(&job, "uuid = ?", expected.jobID).Error; err == nil && job.Attempt < job.MaxAttempts {
			now := time.Now().UTC()
			retryMessage := message + "；系统将自动重试"
			updateContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			updates := map[string]any{"message": retryMessage, "updated_at": now}
			if expected.status == model.PipelineRunRunning {
				updates["stage"] = "retrying"
			}
			result := s.db.WithContext(updateContext).Model(&model.PipelineRun{}).
				Where("stage <> ?", stoppingStage).
				Where("uuid = ? AND execution_job_id = ? AND current_node_id = ? AND status = ?",
					expected.runID, expected.jobID, expected.nodeID, expected.status).Updates(updates)
			if result.Error == nil && result.RowsAffected == 1 {
				s.appendRunLog(updateContext, expected.runID, "retrying", "warning", retryMessage)
				return &buildTaskExecutionError{cause: cause, retryable: true}
			}
			if result.Error != nil {
				cause = errors.Join(cause, result.Error)
			}
		}
	}
	failed := s.failExecution(ctx, expected, message, cause)
	return &buildTaskExecutionError{cause: failed, retryable: false}
}

func transientBuildError(err error) bool {
	var advanceError *completedBuildAdvanceError
	if errors.As(err, &advanceError) {
		return true
	}
	if dockerengine.IsRetryableBuildError(err) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

func (s *Service) loadBuildExecution(ctx context.Context, payload BuildTaskPayload, jobID string) (*buildExecutionContext, error) {
	if payload.PipelineRunID == "" || payload.WorkflowNodeID == "" || jobID == "" || s.repositories == nil {
		return nil, ErrPipelineExecutionConfig
	}
	var run model.PipelineRun
	if nodeRun, found, err := s.loadNodeExecution(ctx, payload.PipelineRunID, jobID, payload.WorkflowNodeID); err != nil {
		return nil, err
	} else if found {
		run = *nodeRun
	} else if err := s.db.WithContext(ctx).First(&run, "uuid = ? AND execution_job_id = ?", payload.PipelineRunID, jobID).Error; err != nil {
		return nil, err
	}
	if run.CurrentNodeID == payload.WorkflowNodeID && (run.Stage == "task_succeeded" || run.Status == model.PipelineRunSucceeded) {
		return nil, errPipelineExecutionComplete
	}
	if run.Status != model.PipelineRunRunning || run.CurrentNodeID != payload.WorkflowNodeID {
		return nil, ErrInvalidWorkflowTransition
	}
	snapshot, err := parseWorkflowSnapshot(&run)
	if err != nil {
		return nil, err
	}
	node, found := workflowFindNode(snapshot.Source, snapshot.Stages, payload.WorkflowNodeID)
	if !found || (node.Type != model.WorkflowNodeBuild && node.Type != model.WorkflowNodeShell) {
		return nil, ErrPipelineExecutionConfig
	}
	result := &buildExecutionContext{run: run, node: node, snapshot: *snapshot}
	if err := s.db.WithContext(ctx).First(&result.application, "uuid = ? AND is_active = ?", run.ApplicationID, true).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).First(&result.component, "pipeline_run_id = ?", run.ID).Error; err != nil {
		return nil, err
	}
	if node.Type == model.WorkflowNodeBuild {
		plan, ok := snapshot.BuildPlans[node.ID]
		if !ok || plan.ID == "" || plan.ID != node.Config.BuildPlanID {
			return nil, ErrPipelineExecutionConfig
		}
		platform, platformErr := workflowBuildPlatform(*snapshot, node.ID, plan)
		if platformErr != nil {
			return nil, platformErr
		}
		plan.Platform = platform
		result.plan = plan
	}
	return result, nil
}

// workflowBuildPlatform 从本次运行已经冻结的下游部署目标中推导构建平台。
// 遇到下一次构建即停止，避免把后续独立制品的部署架构错误合并到当前制品。
func workflowBuildPlatform(snapshot workflowSnapshot, buildNodeID string, plan workflowBuildPlanSnapshot) (string, error) {
	tasks := workflowTasks(snapshot.Stages)
	buildIndex := -1
	for index := range tasks {
		if tasks[index].ID == buildNodeID {
			buildIndex = index
			break
		}
	}
	if buildIndex < 0 {
		return "", ErrPipelineExecutionConfig
	}
	platformSet := make(map[string]struct{}, 2)
	for index := buildIndex + 1; index < len(tasks); index++ {
		if tasks[index].Type == model.WorkflowNodeBuild && snapshot.ExecutionVersion < 2 {
			break
		}
		if tasks[index].Type != model.WorkflowNodeDeploy {
			continue
		}
		if snapshot.ExecutionVersion >= 2 {
			sources, err := artifactSources(snapshot.Stages)
			if err != nil {
				return "", err
			}
			if sources[tasks[index].ID] != buildNodeID {
				continue
			}
		}
		target, exists := snapshot.DeploymentTargets[tasks[index].ID]
		if !exists {
			return "", ErrPipelineExecutionConfig
		}
		architectures := target.Architectures
		if len(architectures) == 0 {
			architectures = []model.HostArchitecture{target.Architecture}
		}
		for _, architecture := range architectures {
			platform := architecture.OCIPlatform()
			if platform == "" {
				return "", ErrPipelineExecutionConfig
			}
			platformSet[platform] = struct{}{}
		}
	}
	platforms := make([]string, 0, len(platformSet))
	for platform := range platformSet {
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	if len(platforms) > 1 && (plan.Kind != model.BuildPlanDockerfile || plan.ImageRegistryID == "") {
		return "", ErrPipelineExecutionConfig
	}
	return strings.Join(platforms, ","), nil
}

func (s *Service) executePipelineBuild(ctx context.Context, prepared *buildExecutionContext, workspace string) (*model.Artifact, error) {
	if s.artifacts == nil {
		return nil, ErrPipelineExecutionUnavailable
	}
	planJSON, err := json.Marshal(prepared.plan)
	if err != nil {
		return nil, ErrPipelineExecutionConfig
	}
	metadata := artifact.BuildMetadata{
		ProjectID: prepared.run.ProjectID, DepartmentID: prepared.run.DepartmentID,
		ApplicationID: prepared.application.ID, PipelineRunID: prepared.run.ID,
		RepositoryID: prepared.component.RepositoryID, WorkflowNodeID: prepared.node.ID,
		BuildPlanID: prepared.plan.ID, Ref: prepared.run.Ref, CommitSHA: prepared.run.CommitSHA,
		PlanSnapshot: string(planJSON), PlanDigest: sha256Digest(planJSON), CreatedBy: executionActor(prepared.run.CreatedBy),
	}
	switch prepared.plan.Kind {
	case model.BuildPlanScript:
		artifactPath := strings.TrimSpace(prepared.plan.ArtifactPath)
		outputDirectory := ""
		var buildDirectory *artifact.BuildDirectoryLease
		if artifactPath != "" {
			buildDirectory, err = s.artifacts.AcquireBuildDirectory("edo-script-output-")
			if err != nil {
				return nil, err
			}
			defer buildDirectory.Release()
			outputDirectory = buildDirectory.Directory
		}
		runtimeImage := effectiveBuildRuntimeImage(prepared.node.Config, prepared.plan)
		result, err := s.executePipelineShell(ctx, prepared, workspace, runtimeImage, prepared.plan.Script,
			prepared.plan.WorkingDirectory, prepared.plan.EnvironmentVariables,
			prepared.plan.TimeoutSeconds, "build", "构建", artifactPath, outputDirectory)
		if err != nil {
			return nil, err
		}
		if artifactPath == "" {
			s.appendRunLog(ctx, prepared.run.ID, "build", "success", "Shell 构建已完成，本次构建未配置保存文件制品")
			return nil, nil
		}
		outputPath := result.ArtifactPath
		outputInfo, err := os.Lstat(outputPath)
		if err != nil {
			return nil, err
		}
		metadata.ProducerKind = model.BuildRunProducerScript
		return s.artifacts.CreateFileFromPath(ctx, artifact.BuildOutputInput{
			BuildMetadata: metadata, SourcePath: outputPath,
			Name: artifactName(prepared.application.Name, prepared.run.CommitSHA, artifactPath, outputInfo.IsDir()),
		})
	case model.BuildPlanDockerfile:
		if s.docker == nil {
			return nil, ErrPipelineExecutionUnavailable
		}
		contextDirectory, dockerfile, err := secureBuildPaths(workspace, prepared.plan.ContextPath, prepared.plan.DockerfilePath)
		if err != nil {
			return nil, err
		}
		metadata.ProducerKind = model.BuildRunProducerDockerfile
		buildArgs := effectiveDockerBuildArgs(prepared.node.Config, prepared.plan.BuildArgs)
		if len(prepared.snapshot.VariableBindings[prepared.node.ID]) > 0 {
			if err := s.requireDeliveryPermission(ctx, prepared.snapshot.AuthorityID, "delivery_variable.execute"); err != nil {
				return nil, err
			}
		}
		values, files, variableRedactions, variableErr := s.Delivery().ResolveVariables(ctx, prepared.run.ProjectID, prepared.run.Ref, prepared.snapshot.VariableBindings[prepared.node.ID])
		if variableErr != nil {
			return nil, variableErr
		}
		buildSecrets := maps.Clone(files)
		if buildSecrets == nil {
			buildSecrets = map[string]string{}
		}
		if buildArgs == nil {
			buildArgs = map[string]string{}
		}
		for _, binding := range prepared.snapshot.VariableBindings[prepared.node.ID] {
			if binding.Kind == "file" {
				continue
			}
			if binding.IsSecret {
				buildSecrets[binding.Name] = values[binding.Name]
			} else {
				buildArgs[binding.Name] = values[binding.Name]
			}
		}

		options := dockerengine.BuildOptions{
			Pull: prepared.plan.Pull, CacheEnabled: prepared.plan.CacheEnabled,
			TargetStage: prepared.plan.TargetStage, Platform: prepared.plan.Platform,
			BuildArgs: buildArgs, Secrets: buildSecrets,
			Labels: map[string]string{
				"io.edo.application.id":  prepared.application.ID,
				"io.edo.pipeline.run.id": prepared.run.ID,
				"io.edo.commit":          prepared.run.CommitSHA,
			},
		}
		if options.Platform != "" {
			s.appendRunLog(ctx, prepared.run.ID, "build", "info", "已根据部署目标选择构建平台 "+options.Platform)
		}
		timeout := time.Duration(prepared.plan.TimeoutSeconds) * time.Second
		if prepared.plan.ImageRegistryID == "" {
			output := s.newBuildLogWriter(ctx, prepared.run.ID, "build", append(sensitiveVariableValues(buildArgs), variableRedactions...)...)
			defer output.Close()
			image, err := localExecutionImage(&executionContext{run: prepared.run, application: prepared.application})
			if err != nil {
				return nil, err
			}
			if err := s.updateExecutionPhase(ctx, &prepared.run, "build", "正在构建本地 OCI 镜像 "+image); err != nil {
				return nil, err
			}
			builtImage, err := s.docker.BuildLocalArtifactWithOptions(ctx, contextDirectory, dockerfile, image, options, timeout, output)
			if err != nil {
				return nil, err
			}
			return s.artifacts.CreateImage(ctx, artifact.ImageInput{
				BuildMetadata: metadata, Name: image, StorageKind: model.ArtifactStorageKindDockerDaemon,
				Digest: builtImage.ImageID, ImageRef: image, RuntimeID: dockerengine.LocalEndpointID,
				LocalImageID: builtImage.ImageID, SizeBytes: builtImage.SizeBytes,
			})
		}
		var registry model.ImageRegistry
		if err := s.db.WithContext(ctx).First(&registry, "uuid = ? AND is_active = ?", prepared.plan.ImageRegistryID, true).Error; err != nil {
			return nil, err
		}
		execution := &executionContext{run: prepared.run, application: prepared.application, registry: registry}
		execution.component.ImageRegistryID = registry.ID
		image, auth, err := s.executionImage(ctx, execution)
		if err != nil {
			return nil, err
		}
		redactions := append(sensitiveVariableValues(buildArgs), variableRedactions...)
		if auth.Credential != "" {
			redactions = append(redactions, auth.Credential)
		}
		output := s.newBuildLogWriter(ctx, prepared.run.ID, "build", redactions...)
		defer output.Close()
		if err := s.updateExecutionPhase(ctx, &prepared.run, "build", "正在构建并推送 OCI 镜像 "+image); err != nil {
			return nil, err
		}
		builtImage, err := s.docker.BuildAndPushArtifactWithOptions(
			ctx, contextDirectory, dockerfile, image, auth, options, timeout, output,
		)
		if err != nil {
			return nil, err
		}
		return s.artifacts.CreateImage(ctx, artifact.ImageInput{
			BuildMetadata: metadata, Name: image, StorageKind: model.ArtifactStorageKindRegistry,
			ImageRef: builtImage.ImageRef, ImageRegistryID: registry.ID, SizeBytes: builtImage.SizeBytes,
		})
	default:
		return nil, ErrPipelineExecutionConfig
	}
}

func effectiveBuildRuntimeImage(config model.WorkflowNodeConfig, plan workflowBuildPlanSnapshot) string {
	if strings.TrimSpace(config.RuntimeImage) != "" {
		return config.RuntimeImage
	}
	return plan.RuntimeImage
}

func effectiveDockerBuildArgs(config model.WorkflowNodeConfig, configured map[string]string) map[string]string {
	result := maps.Clone(configured)
	argument := workflowRuntimeBuildArg(config.ToolchainLanguage)
	if argument == "" || config.ToolchainVersion == "" {
		return result
	}
	if result == nil {
		result = make(map[string]string, 1)
	}
	result[argument] = config.ToolchainVersion
	return result
}

func secureBuildPaths(workspace, contextPath, dockerfilePath string) (string, string, error) {
	contextDirectory, err := secureWorkspacePath(workspace, contextPath)
	if err != nil {
		return "", "", ErrInvalidBuildPlan
	}
	dockerfileAbsolute, err := secureWorkspacePath(workspace, dockerfilePath)
	if err != nil {
		return "", "", ErrInvalidBuildPlan
	}
	info, err := os.Stat(contextDirectory)
	if err != nil || !info.IsDir() {
		return "", "", ErrInvalidBuildPlan
	}
	info, err = os.Stat(dockerfileAbsolute)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", ErrInvalidBuildPlan
	}
	dockerfileRelative, err := filepath.Rel(contextDirectory, dockerfileAbsolute)
	if err != nil || dockerfileRelative == ".." || strings.HasPrefix(dockerfileRelative, ".."+string(filepath.Separator)) {
		return "", "", ErrInvalidBuildPlan
	}
	return contextDirectory, dockerfileRelative, nil
}

func (s *Service) executePipelineShell(
	ctx context.Context,
	prepared *buildExecutionContext,
	workspace, runtimeImage, script, workingDirectory string,
	environment map[string]string,
	timeoutSeconds int,
	stage, outputName, artifactPath, outputDirectory string,
) (dockerengine.ScriptContainerResult, error) {
	if s.scriptRunner == nil {
		return dockerengine.ScriptContainerResult{}, ErrPipelineExecutionUnavailable
	}
	if strings.TrimSpace(script) == "" || timeoutSeconds < 30 || timeoutSeconds > 7200 ||
		!validScriptEnvironmentVariables(environment) {
		return dockerengine.ScriptContainerResult{}, ErrPipelineExecutionConfig
	}
	directory, err := secureWorkspacePath(workspace, workingDirectory)
	if err != nil {
		return dockerengine.ScriptContainerResult{}, err
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return dockerengine.ScriptContainerResult{}, ErrPipelineExecutionConfig
	}
	if err := s.updateExecutionPhase(ctx, &prepared.run, stage, "正在执行“"+prepared.node.Name+"”"); err != nil {
		return dockerengine.ScriptContainerResult{}, err
	}
	if len(prepared.snapshot.VariableBindings[prepared.node.ID]) > 0 {
		if err := s.requireDeliveryPermission(ctx, prepared.snapshot.AuthorityID, "delivery_variable.execute"); err != nil {
			return dockerengine.ScriptContainerResult{}, err
		}
	}
	injected, files, redactions, variableErr := s.Delivery().ResolveVariables(ctx, prepared.run.ProjectID, prepared.run.Ref, prepared.snapshot.VariableBindings[prepared.node.ID])
	if variableErr != nil {
		return dockerengine.ScriptContainerResult{}, variableErr
	}
	environment = maps.Clone(environment)
	if environment == nil {
		environment = map[string]string{}
	}
	for key, value := range injected {
		environment[key] = value
	}
	redactions = append(redactions, sensitiveVariableValues(environment)...)
	var reportPaths []string
	if err := cicd.ValidateReportConfig(prepared.node.Config.Reports); err != nil {
		return dockerengine.ScriptContainerResult{}, err
	}
	for _, config := range prepared.node.Config.Reports {
		reportPaths = append(reportPaths, config.Paths...)
	}
	cacheKey, cacheRoot, cacheOutput := "", "", ""
	if config := prepared.node.Config.Cache; config != nil {
		if s.artifacts == nil {
			return dockerengine.ScriptContainerResult{}, ErrPipelineExecutionUnavailable
		}
		key, keyErr := cicd.CacheKey(ctx, prepared.run.ProjectID, prepared.component.RepositoryID, prepared.plan.Platform, runtimeImage, workspace, *config)
		if keyErr != nil {
			return dockerengine.ScriptContainerResult{}, keyErr
		}
		cacheKey = key
		cacheLease, leaseErr := s.artifacts.AcquireDependencyCache()
		if leaseErr != nil {
			return dockerengine.ScriptContainerResult{}, leaseErr
		}
		defer cacheLease.Release()
		cacheRoot = cacheLease.Directory
		export, exportErr := s.artifacts.AcquireBuildDirectory("edo-cache-output-")
		if exportErr != nil {
			return dockerengine.ScriptContainerResult{}, exportErr
		}
		defer export.Release()
		cacheOutput = export.Directory
		hit, restoreErr := cicd.RestoreCache(cacheRoot, cacheKey, workspace, *config)
		if restoreErr != nil {
			s.logger.Warn("恢复依赖缓存失败", "operation", "pipeline_cache_restore", "run_id", prepared.run.ID, "err", restoreErr)
			return dockerengine.ScriptContainerResult{}, cicd.ErrInvalidCache
		}
		if hit {
			s.appendRunLog(ctx, prepared.run.ID, stage, "info", "已恢复当前项目的依赖缓存")
		} else {
			s.appendRunLog(ctx, prepared.run.ID, stage, "info", "未命中依赖缓存，将在任务成功后保存")
		}
	}
	output := s.newExecutionLogWriter(ctx, prepared.run.ID, stage, outputName, redactions...)
	defer output.Close()
	result, err := s.scriptRunner.RunScriptContainer(ctx, dockerengine.ScriptContainerInput{
		Image:            runtimeImage,
		Platform:         prepared.plan.Platform,
		Script:           script,
		SourceDirectory:  workspace,
		WorkingDirectory: workingDirectory,
		ArtifactPath:     artifactPath,
		OutputDirectory:  outputDirectory,
		Environment:      environment,
		FileVariables:    files, ReportPaths: reportPaths,
		CacheOutputDirectory: cacheOutput,
		CachePaths: func() []string {
			if prepared.node.Config.Cache != nil {
				return prepared.node.Config.Cache.Paths
			}
			return nil
		}(),
		SystemEnvironment: pipelineCommandEnvironment(prepared),
		Labels: map[string]string{
			"io.edo.application.id":   prepared.application.ID,
			"io.edo.pipeline.run.id":  prepared.run.ID,
			"io.edo.workflow.node.id": prepared.node.ID,
			"io.edo.commit":           prepared.run.CommitSHA,
		},
		Timeout: time.Duration(timeoutSeconds) * time.Second,
		Stdout:  output,
		Stderr:  output,
	})
	if err == nil && cacheKey != "" {
		if cacheErr := cicd.SaveCache(cacheRoot, cacheKey, result.CachePaths); cacheErr != nil {
			s.logger.Warn("保存依赖缓存失败", "operation", "pipeline_cache_save", "run_id", prepared.run.ID, "err", cacheErr)
			s.appendRunLog(ctx, prepared.run.ID, stage, "warning", "依赖缓存未保存，本次任务结果不受影响")
		}
	}
	if ctx.Err() != nil {
		return result, errors.Join(err, ctx.Err())
	}
	reportErr := s.Delivery().SaveReports(context.WithoutCancel(ctx), prepared.run, prepared.node.ID, prepared.node.Config.Reports, result.Reports, redactions)
	if reportErr != nil {
		return result, errors.Join(err, reportErr)
	}
	if err != nil {
		return result, err
	}
	if result.ImageID != "" {
		s.appendRunLog(ctx, prepared.run.ID, stage, "info", "脚本运行镜像已固定为 "+result.ImageID)
	}
	return result, nil
}

func sensitiveVariableValues(values map[string]string) []string {
	result := make([]string, 0)
	for name, value := range values {
		upper := strings.ToUpper(name)
		if value != "" && (strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") ||
			strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "CREDENTIAL") ||
			strings.HasSuffix(upper, "_PASS") || strings.HasSuffix(upper, "_KEY") ||
			strings.HasSuffix(upper, "_AUTH")) {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func pipelineCommandEnvironment(prepared *buildExecutionContext) map[string]string {
	environment := map[string]string{
		"EDO_PIPELINE_RUN_ID": prepared.run.ID,
		"EDO_APPLICATION_ID":  prepared.application.ID,
		"EDO_GIT_REF":         prepared.run.Ref,
		"EDO_COMMIT_SHA":      prepared.run.CommitSHA,
	}
	if prepared.plan.Platform != "" {
		environment["EDO_TARGET_PLATFORM"] = prepared.plan.Platform
		if strings.HasPrefix(prepared.plan.Platform, "linux/") && !strings.Contains(prepared.plan.Platform, ",") {
			architecture := strings.TrimPrefix(prepared.plan.Platform, "linux/")
			environment["EDO_TARGET_ARCH"] = architecture
			if prepared.node.Config.ToolchainLanguage == workflowPresetGo {
				environment["GOOS"], environment["GOARCH"] = "linux", architecture
			}
		}
	}
	return environment
}

func secureWorkspacePath(workspace, relative string) (string, error) {
	relative = strings.TrimSpace(relative)
	if relative == "" {
		relative = "."
	}
	cleaned := filepath.Clean(relative)
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", ErrPipelineExecutionConfig
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, cleaned)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	relativeToRoot, err := filepath.Rel(root, resolved)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return "", ErrPipelineExecutionConfig
	}
	return resolved, nil
}

func (s *Service) completeBuildExecution(ctx context.Context, prepared *buildExecutionContext, produced *model.Artifact) error {
	if prepared.run.NodeExecution {
		return s.completeNodeExecution(ctx, prepared.run, prepared.node, produced, nil)
	}
	_, hasNext, terminal := workflowNextNode(prepared.snapshot.Source, prepared.snapshot.Stages, prepared.node.ID)
	if !hasNext && !terminal {
		return ErrInvalidWorkflowTransition
	}
	status, stage := model.PipelineRunReady, "task_succeeded"
	componentStatus := model.PipelineRunRepositoryReady
	if !hasNext {
		status, stage, componentStatus = model.PipelineRunSucceeded, "completed", model.PipelineRunRepositorySucceeded
	}
	message := "当前任务：" + prepared.node.Name + "；状态：已完成"
	updates := map[string]any{"status": status, "stage": stage, "message": message, "updated_at": time.Now().UTC()}
	if produced != nil {
		updates["artifact_id"] = produced.ID
		if produced.Kind == model.ArtifactKindOCIImage {
			updates["image"] = produced.ImageRef
		}
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.PipelineRun{}).
			Where("stage <> ?", stoppingStage).
			Where("uuid = ? AND execution_job_id = ? AND status = ?", prepared.run.ID, prepared.run.ExecutionJobID, model.PipelineRunRunning).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvalidWorkflowTransition
		}
		return tx.Model(&model.PipelineRunRepository{}).Where("pipeline_run_id = ?", prepared.run.ID).
			Updates(map[string]any{"status": componentStatus, "updated_at": time.Now().UTC()}).Error
	})
	if err != nil {
		return err
	}
	s.appendRunLog(ctx, prepared.run.ID, stage, "success", message)
	if !hasNext {
		s.notifyWorkflowNode(ctx, &prepared.run, prepared.node, workflowNotificationSucceeded, message)
		return nil
	}
	advanceContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, err = s.AdvanceRun(advanceContext, prepared.run.ID, prepared.run.CreatedBy, "")
	if err != nil {
		return &completedBuildAdvanceError{cause: err}
	}
	s.notifyWorkflowNode(ctx, &prepared.run, prepared.node, workflowNotificationSucceeded, message)
	return nil
}

func executionActor(actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "system"
	}
	return actor
}

func sha256Digest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func artifactName(applicationName, commit, artifactPath string, directory bool) string {
	commit = strings.TrimSpace(commit)
	if len(commit) > 12 {
		commit = commit[:12]
	}
	extension := filepath.Ext(strings.TrimSpace(artifactPath))
	if directory {
		extension = ".tar.gz"
	}
	return strings.TrimSpace(applicationName) + "-" + commit + extension
}
