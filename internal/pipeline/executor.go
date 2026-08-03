package pipeline

import (
	"context"
	"errors"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/regclient/regclient"
	"gorm.io/gorm"

	"edo/internal/artifact"
	"edo/internal/deployment"
	"edo/internal/dockerengine"
	"edo/internal/model"
)

var (
	ErrPipelineExecutionUnavailable = errors.New("流水线执行服务尚未就绪")
	ErrPipelineExecutionRunning     = errors.New("流水线正在执行，请稍后刷新")
	ErrPipelineExecutionConfig      = errors.New("流水线执行配置不完整，请检查任务、构建方案和部署方案")
	ErrPipelineExecutionState       = errors.New("流水线执行状态更新失败，请稍后重试")
	errPipelineExecutionComplete    = errors.New("流水线执行已经完成")
)

type DeployTaskPayload struct {
	PipelineRunID  string `json:"pipeline_run_id"`
	WorkflowNodeID string `json:"workflow_node_id"`
}

type BuildTaskPayload struct {
	PipelineRunID  string `json:"pipeline_run_id"`
	WorkflowNodeID string `json:"workflow_node_id"`
}

type executionContext struct {
	run            model.PipelineRun
	node           model.WorkflowNode
	snapshot       workflowSnapshot
	application    model.Application
	component      model.PipelineRunRepository
	deploymentPlan model.DeploymentPlan
	registry       model.ImageRegistry
	target         model.DeploymentTarget
	artifact       model.Artifact
}

// executionFailureState 固定产生错误的任务身份。失败落库必须同时匹配任务、节点和状态，
// 避免旧消息重投时覆盖已经推进到后续任务的流水线状态。
type executionFailureState struct {
	runID  string
	jobID  string
	nodeID string
	status model.PipelineRunStatus
}

func taskFailureState(runID, jobID, nodeID string, status model.PipelineRunStatus) executionFailureState {
	return executionFailureState{runID: runID, jobID: jobID, nodeID: nodeID, status: status}
}

func failureStateForRun(run model.PipelineRun) executionFailureState {
	return taskFailureState(run.ID, run.ExecutionJobID, run.CurrentNodeID, run.Status)
}

func (s *Service) ExecuteDeployTask(ctx context.Context, payload DeployTaskPayload, jobID string) error {
	if s.deployments == nil || s.repositories == nil || s.secrets == nil {
		return ErrPipelineExecutionUnavailable
	}
	prepared, err := s.loadExecution(ctx, payload, jobID)
	if err != nil {
		if errors.Is(err, errPipelineExecutionComplete) {
			return nil
		}
		if obsolete, obsoleteErr := s.executionTaskObsolete(ctx, payload.PipelineRunID, jobID, payload.WorkflowNodeID); obsoleteErr == nil && obsolete {
			return nil
		}
		return s.failExecution(ctx, taskFailureState(payload.PipelineRunID, jobID, payload.WorkflowNodeID, model.PipelineRunRunning),
			"流水线执行配置不完整，请检查任务、构建方案和部署方案", err)
	}
	failureState := failureStateForRun(prepared.run)
	s.appendRunLog(ctx, prepared.run.ID, "deploy", "info", "开始部署已构建制品")
	approvedBy, err := s.latestExecutionApproval(ctx, prepared.run.ID)
	if err != nil {
		return s.failExecution(ctx, failureState, "读取流水线审核状态失败，请稍后重试", err)
	}
	if prepared.target.Platform == model.DeploymentSSH {
		return s.executeSSHDeployment(ctx, prepared, approvedBy)
	}
	image, expectedImageID, err := s.resolveDeploymentImage(ctx, prepared)
	if err != nil {
		return s.failExecution(ctx, failureState, "部署制品不可用或传输失败，请查看流水线日志", err)
	}

	if err := s.updateExecutionPhase(ctx, &prepared.run, "deploy", "正在将制品部署到“"+prepared.target.Name+"”"); err != nil {
		return s.failExecution(ctx, failureState, "更新流水线执行状态失败", err)
	}
	request := deployment.RequestInput{
		TargetID: prepared.target.ID, ApplicationID: prepared.application.ID, ApplicationName: prepared.application.Name,
		ArtifactID: prepared.artifact.ID, Image: image, ImageDisplay: prepared.artifact.Name,
		ExpectedImageID: expectedImageID,
		PipelineRunID:   prepared.run.ID, WorkflowNodeID: prepared.node.ID, ApprovedBy: approvedBy,
		DeploymentPlanID: prepared.deploymentPlan.ID, PlanKind: prepared.deploymentPlan.Kind,
	}
	if prepared.artifact.StorageKind == model.ArtifactStorageKindRegistry {
		request.RegistryAuth, err = s.registryAuth(prepared.registry)
		if err != nil {
			return s.failExecution(ctx, failureState, "读取镜像仓库认证失败，请检查构建方案使用的镜像仓库", err)
		}
	}
	var output io.WriteCloser
	if prepared.deploymentPlan.Kind == model.DeploymentPlanCompose {
		output = s.newExecutionLogWriter(ctx, prepared.run.ID, "deploy", "Docker Compose")
		defer output.Close()
		request.ComposeYAML = prepared.deploymentPlan.ComposeYAML
		request.ComposeService = prepared.deploymentPlan.ServiceName
		request.TimeoutSeconds = prepared.deploymentPlan.TimeoutSeconds
		request.ComposeDigest = model.DeploymentPlanComposeExecutionDigest(
			request.ComposeYAML, request.ComposeService, request.TimeoutSeconds,
		)
		request.Stdout, request.Stderr = output, output
	} else if prepared.deploymentPlan.Kind == model.DeploymentPlanDocker {
		output = s.newExecutionLogWriter(ctx, prepared.run.ID, "deploy", "Docker")
		defer output.Close()
		request.DockerConfig = prepared.deploymentPlan.DockerConfig
		request.DockerConfigDigest = model.DockerContainerConfigDigest(request.DockerConfig)
		request.Stdout, request.Stderr = output, output
	}
	record, err := s.deployments.RequestSnapshotAndRun(ctx, prepared.run.CreatedBy, prepared.target, request)
	if err != nil {
		return s.handleDeploymentExecutionError(ctx, failureState,
			deploymentFailureLogMessage(record, "发布执行失败，请检查目标环境和发布记录"), err)
	}
	return s.completeExecution(ctx, prepared, record)
}

func (s *Service) resolveDeploymentImage(ctx context.Context, prepared *executionContext) (string, string, error) {
	if prepared.artifact.Status != model.ArtifactStatusAvailable || prepared.artifact.Kind != model.ArtifactKindOCIImage {
		return "", "", ErrPipelineExecutionConfig
	}
	switch prepared.artifact.StorageKind {
	case model.ArtifactStorageKindRegistry:
		if prepared.target.Platform != model.DeploymentDocker && prepared.target.Platform != model.DeploymentKubernetes {
			return "", "", ErrPipelineExecutionConfig
		}
		if !strings.Contains(prepared.artifact.ImageRef, "@sha256:") {
			return "", "", ErrPipelineExecutionConfig
		}
		return prepared.artifact.ImageRef, "", nil
	case model.ArtifactStorageKindDockerDaemon:
		if prepared.target.Platform != model.DeploymentDocker || prepared.artifact.RuntimeID != dockerengine.LocalEndpointID ||
			!dockerengine.IsEDOLocalImage(prepared.artifact.ImageRef) || !dockerengine.IsValidImageID(prepared.artifact.LocalImageID) {
			return "", "", ErrPipelineExecutionConfig
		}
		if dockerengine.IsLocalEndpointID(prepared.target.RuntimeID) {
			return prepared.artifact.ImageRef, prepared.artifact.LocalImageID, nil
		}
		if err := s.updateExecutionPhase(ctx, &prepared.run, "transfer", "正在通过 SSH 传输镜像到“"+prepared.target.Name+"”"); err != nil {
			return "", "", err
		}
		targetImageID, err := s.docker.TransferImageToSSH(
			ctx, prepared.target.RuntimeID, prepared.artifact.ImageRef, prepared.artifact.LocalImageID, 30*time.Minute,
		)
		if err != nil {
			return "", "", err
		}
		return prepared.artifact.ImageRef, targetImageID, nil
	default:
		return "", "", ErrPipelineExecutionConfig
	}
}

func (s *Service) executeSSHDeployment(ctx context.Context, prepared *executionContext, approvedBy string) error {
	failureState := failureStateForRun(prepared.run)
	if err := s.updateExecutionPhase(ctx, &prepared.run, "deploy", "正在执行命令脚本并发布到“"+prepared.target.Name+"”"); err != nil {
		return s.failExecution(ctx, failureState, "更新流水线执行状态失败", err)
	}
	output := s.newExecutionLogWriter(ctx, prepared.run.ID, "deploy", "命令部署")
	defer output.Close()
	if prepared.artifact.ID == "" || prepared.artifact.Kind != model.ArtifactKindFileBundle || s.artifacts == nil {
		return s.failExecution(ctx, failureState, "主机脚本部署需要上游文件制品", ErrPipelineExecutionConfig)
	}
	_, artifactFile, err := s.artifacts.OpenDownload(ctx, prepared.artifact.ID)
	if err != nil {
		return s.failExecution(ctx, failureState, "打开待部署制品失败", err)
	}
	defer artifactFile.Close()
	record, err := s.deployments.RequestCommandSnapshotAndRun(ctx, prepared.run.CreatedBy, prepared.target, deployment.CommandRequestInput{
		TargetID: prepared.target.ID, ArtifactID: prepared.artifact.ID,
		PipelineRunID: prepared.run.ID, WorkflowNodeID: prepared.node.ID,
		ApprovedBy: approvedBy, DeploymentPlanID: prepared.deploymentPlan.ID,
		PlanKind: prepared.deploymentPlan.Kind, Script: prepared.deploymentPlan.Script,
		ScriptDigest: model.DeploymentPlanExecutionDigest(
			prepared.deploymentPlan.Kind, prepared.deploymentPlan.Script, prepared.deploymentPlan.TimeoutSeconds,
		),
		TimeoutSeconds: prepared.deploymentPlan.TimeoutSeconds,
		Environment: map[string]string{
			"EDO_PIPELINE_RUN_ID":      prepared.run.ID,
			"EDO_APPLICATION_ID":       prepared.application.ID,
			"EDO_APPLICATION_NAME":     prepared.application.Name,
			"EDO_GIT_REF":              prepared.run.Ref,
			"EDO_COMMIT_SHA":           prepared.run.CommitSHA,
			"EDO_DEPLOYMENT_TARGET_ID": prepared.target.ID,
		},
		Artifact: artifactFile, ArtifactName: prepared.artifact.Name, ArtifactDigest: prepared.artifact.Digest,
		Stdout: output, Stderr: output,
	})
	if err != nil {
		return s.handleDeploymentExecutionError(ctx, failureState,
			deploymentFailureLogMessage(record, "命令脚本部署失败，请查看流水线日志"), err)
	}
	return s.completeExecution(ctx, prepared, record)
}

func deploymentFailureLogMessage(record *model.DeploymentRecord, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if record == nil || record.Status != model.DeploymentFailed {
		return fallback
	}
	message := strings.TrimSpace(record.ErrorMessage)
	if message == "" || len(message) > 255 || strings.ContainsAny(message, "\x00\r\n") {
		return fallback
	}
	return message
}

func (s *Service) handleDeploymentExecutionError(ctx context.Context, expected executionFailureState, message string, cause error) error {
	if errors.Is(cause, deployment.ErrPipelineReleaseRunning) {
		if s.logger != nil {
			s.logger.Error("流水线发布任务被重复投递，已有发布仍在执行", "operation", "pipeline_deploy_idempotency",
				"pipeline_run_id", expected.runID, "err", cause)
		}
		return cause
	}
	return s.failExecution(ctx, expected, message, cause)
}

func (s *Service) loadExecution(ctx context.Context, payload DeployTaskPayload, jobID string) (*executionContext, error) {
	return s.loadArtifactDeploymentExecution(ctx, payload, jobID)
}

func (s *Service) loadArtifactDeploymentExecution(ctx context.Context, payload DeployTaskPayload, jobID string) (*executionContext, error) {
	if payload.PipelineRunID == "" || payload.WorkflowNodeID == "" || jobID == "" {
		return nil, ErrPipelineExecutionConfig
	}
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "id = ? AND execution_job_id = ?", payload.PipelineRunID, jobID).Error; err != nil {
		return nil, err
	}
	if run.Status == model.PipelineRunSucceeded && run.DeploymentID != "" {
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
	if !found || node.Type != model.WorkflowNodeDeploy || node.Config.DeploymentPlanID == "" {
		return nil, ErrPipelineExecutionConfig
	}
	plan, hasPlan := snapshot.DeploymentPlans[node.ID]
	target, hasTarget := snapshot.DeploymentTargets[node.ID]
	if !hasPlan || !hasTarget || plan.ID != node.Config.DeploymentPlanID || target.ID == "" {
		return nil, ErrPipelineExecutionConfig
	}
	result := &executionContext{run: run, node: node, snapshot: *snapshot}
	result.deploymentPlan = model.DeploymentPlan{
		ID: plan.ID, Kind: plan.Kind, Script: plan.Script,
		ComposeYAML: plan.ComposeYAML, ServiceName: plan.ServiceName,
		DockerConfig:   plan.DockerConfig,
		TimeoutSeconds: plan.TimeoutSeconds,
	}
	result.target = model.DeploymentTarget{
		ID: target.ID, Name: target.Name, Platform: target.Platform, EnvironmentID: target.EnvironmentID,
		HostID: target.HostID, RuntimeID: target.RuntimeID, WorkingDirectory: target.WorkingDirectory,
		Namespace: target.Namespace, WorkloadName: target.WorkloadName, ContainerName: target.ContainerName,
		RolloutTimeout: target.RolloutTimeout, IsActive: true,
	}
	if !deploymentPlanSupportsTarget(result.deploymentPlan.Kind, result.target.Platform) {
		return nil, ErrDeploymentPlanTargetMismatch
	}
	if result.deploymentPlan.Kind == model.DeploymentPlanCompose &&
		!validComposeDeploymentPlanSnapshot(&result.deploymentPlan, &result.target) {
		return nil, ErrPipelineExecutionConfig
	}
	if result.deploymentPlan.Kind == model.DeploymentPlanDocker &&
		!normalizeDockerDeploymentPlanSnapshot(&result.deploymentPlan, &result.target) {
		return nil, ErrPipelineExecutionConfig
	}
	if err := s.db.WithContext(ctx).First(&result.application, "id = ? AND is_active = ?", run.ApplicationID, true).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).First(&result.component, "pipeline_run_id = ?", run.ID).Error; err != nil {
		return nil, err
	}
	if result.target.Platform == model.DeploymentSSH {
		if !validSSHDeploymentPlanSnapshot(&result.deploymentPlan, &result.target) {
			return nil, ErrPipelineExecutionConfig
		}
		if run.ArtifactID == "" {
			return nil, ErrPipelineExecutionConfig
		}
		if s.artifacts == nil {
			return nil, ErrPipelineExecutionUnavailable
		}
		stored, err := s.artifacts.Find(ctx, run.ArtifactID)
		if err != nil || stored.ApplicationID != run.ApplicationID || !artifactAvailableToPipelineRun(run, stored) ||
			stored.Status != model.ArtifactStatusAvailable || stored.Kind != model.ArtifactKindFileBundle {
			return nil, ErrPipelineExecutionConfig
		}
		result.artifact = *stored
		return result, nil
	}
	if s.docker == nil || s.artifacts == nil || run.ArtifactID == "" {
		return nil, ErrPipelineExecutionUnavailable
	}
	stored, err := s.artifacts.Find(ctx, run.ArtifactID)
	if err != nil || stored.ApplicationID != run.ApplicationID || !artifactAvailableToPipelineRun(run, stored) ||
		stored.Status != model.ArtifactStatusAvailable || stored.Kind != model.ArtifactKindOCIImage {
		return nil, ErrPipelineExecutionConfig
	}
	result.artifact = *stored
	if stored.StorageKind == model.ArtifactStorageKindRegistry {
		if stored.ImageRegistryID == "" || s.db.WithContext(ctx).
			First(&result.registry, "id = ? AND is_active = ?", stored.ImageRegistryID, true).Error != nil {
			return nil, ErrPipelineExecutionConfig
		}
		if !artifact.RegistryImageMatches(stored.ImageRef, stored.Digest, result.registry.Endpoint, result.registry.Namespace) {
			if s.logger != nil {
				s.logger.Error("镜像制品与登记仓库不匹配", "operation", "pipeline_registry_artifact_binding",
					"pipeline_run_id", run.ID, "artifact_id", stored.ID,
					"image_registry_id", stored.ImageRegistryID, "err", ErrPipelineExecutionConfig)
			}
			return nil, ErrPipelineExecutionConfig
		}
	}
	return result, nil
}

// artifactAvailableToPipelineRun 允许手动执行显式固定之前的不可变制品。
// 当前运行持有的 ArtifactID 是选择边界；自动构建产生的制品仍必须属于当前运行。
func artifactAvailableToPipelineRun(run model.PipelineRun, stored *model.Artifact) bool {
	if stored == nil || run.ArtifactID == "" || stored.ID != run.ArtifactID {
		return false
	}
	return stored.PipelineRunID == run.ID || run.Trigger == "manual" || run.Trigger == "release_plan" ||
		(run.Trigger == "retry" && run.RetryOfID != "" && stored.PipelineRunID == run.RetryOfID)
}

func validSSHDeploymentPlanSnapshot(plan *model.DeploymentPlan, target *model.DeploymentTarget) bool {
	return plan != nil && target != nil && target.Platform == model.DeploymentSSH &&
		plan.ID != "" && plan.Kind == model.DeploymentPlanScript &&
		strings.TrimSpace(plan.Script) != "" && len(plan.Script) <= 256*1024 &&
		plan.TimeoutSeconds >= 30 && plan.TimeoutSeconds <= 3600 &&
		target.HostID != "" && target.EnvironmentID != ""
}

func validComposeDeploymentPlanSnapshot(plan *model.DeploymentPlan, target *model.DeploymentTarget) bool {
	return plan != nil && target != nil && plan.ID != "" && plan.Kind == model.DeploymentPlanCompose &&
		target.Platform == model.DeploymentDocker && plan.TimeoutSeconds >= 30 && plan.TimeoutSeconds <= 3600 &&
		dockerengine.ValidateComposeYAML(plan.ComposeYAML, plan.ServiceName) == nil
}

func normalizeDockerDeploymentPlanSnapshot(plan *model.DeploymentPlan, target *model.DeploymentTarget) bool {
	if plan == nil || target == nil || plan.ID == "" || plan.Kind != model.DeploymentPlanDocker ||
		target.Platform != model.DeploymentDocker || plan.TimeoutSeconds < 30 || plan.TimeoutSeconds > 3600 {
		return false
	}
	normalized, err := dockerengine.NormalizeContainerConfig(plan.DockerConfig)
	if err != nil {
		return false
	}
	// 早期快照可能保留空数组、空映射和未展开的默认值。执行前固定为规范形式，
	// 确保流水线与部署服务计算的是同一份配置摘要。
	plan.DockerConfig = normalized
	return true
}

var imageNamePartPattern = regexp.MustCompile(`[^a-z0-9._-]+`)

func (s *Service) executionImage(ctx context.Context, prepared *executionContext) (string, dockerengine.RegistryAuth, error) {
	parsed, err := url.Parse(prepared.registry.Endpoint)
	if err != nil || parsed.Host == "" {
		return "", dockerengine.RegistryAuth{}, ErrInvalidRegistryEndpoint
	}
	name, err := executionImageName(prepared.application)
	if err != nil {
		return "", dockerengine.RegistryAuth{}, err
	}
	parts := make([]string, 0, 4)
	if prefix := strings.Trim(parsed.Path, "/"); prefix != "" {
		parts = append(parts, prefix)
	}
	if prepared.registry.Namespace != "" {
		parts = append(parts, strings.Trim(prepared.registry.Namespace, "/"))
	}
	parts = append(parts, name)
	tag, err := executionImageTag(prepared.run)
	if err != nil {
		return "", dockerengine.RegistryAuth{}, err
	}
	image := parsed.Host + "/" + path.Join(parts...) + ":" + tag
	_ = ctx
	auth, err := s.registryAuth(prepared.registry)
	return image, auth, err
}

func (s *Service) registryAuth(registry model.ImageRegistry) (dockerengine.RegistryAuth, error) {
	parsed, err := url.Parse(registry.Endpoint)
	if err != nil || parsed.Host == "" {
		return dockerengine.RegistryAuth{}, ErrInvalidRegistryEndpoint
	}
	credential := ""
	if registry.CredentialCiphertext != "" {
		credential, err = s.secrets.Decrypt(registry.CredentialCiphertext, []byte("image_registry:"+registry.Name+":credential"))
		if err != nil {
			return dockerengine.RegistryAuth{}, err
		}
	}
	serverAddress := registry.Endpoint
	if registry.Provider == model.RegistryDockerHub {
		serverAddress = regclient.DockerRegistryAuth
	}
	return dockerengine.RegistryAuth{
		ServerAddress: serverAddress, Host: parsed.Host,
		Username: registry.Username, Credential: credential,
	}, nil
}

func localExecutionImage(prepared *executionContext) (string, error) {
	name, err := executionImageName(prepared.application)
	if err != nil {
		return "", err
	}
	tag, err := executionImageTag(prepared.run)
	if err != nil {
		return "", ErrPipelineExecutionConfig
	}
	return "edo.local/" + name + ":" + tag, nil
}

func executionImageName(application model.Application) (string, error) {
	applicationID := strings.TrimSpace(application.ID)
	if applicationID == "" {
		return "", ErrInvalidApplication
	}
	name := strings.TrimSpace(application.Name)
	if applicationNamePattern.MatchString(name) {
		return name, nil
	}
	// 旧数据可能包含中文或空格。新建和修改已经强制使用合法应用名；
	// 这个退化路径只用于避免历史流水线在应用重命名前立即中断。
	name = strings.Trim(imageNamePartPattern.ReplaceAllString(strings.ToLower(application.Name), "-"), "-._")
	if len(name) > 48 {
		name = strings.Trim(name[:48], "-._")
	}
	if name == "" {
		name = "app"
	}
	digest := strings.TrimPrefix(sha256Digest([]byte(applicationID)), "sha256:")
	return name + "-" + digest[:8], nil
}

func executionImageTag(run model.PipelineRun) (string, error) {
	commit := strings.TrimSpace(run.CommitSHA)
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if commit == "" {
		return "", ErrPipelineExecutionConfig
	}
	return commit, nil
}

func (s *Service) latestExecutionApproval(ctx context.Context, runID string) (string, error) {
	var approval model.PipelineRunApproval
	if err := s.db.WithContext(ctx).Where("pipeline_run_id = ?", runID).Order("approved_at DESC").First(&approval).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	return approval.ApprovedBy, nil
}

func (s *Service) updateExecutionPhase(ctx context.Context, run *model.PipelineRun, stage, message string) error {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&model.PipelineRun{}).
		Where("id = ? AND execution_job_id = ? AND status = ?", run.ID, run.ExecutionJobID, model.PipelineRunRunning).
		Updates(map[string]any{"stage": stage, "message": message, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrInvalidWorkflowTransition
	}
	run.Stage, run.Message, run.UpdatedAt = stage, message, now
	s.appendRunLog(ctx, run.ID, stage, "info", message)
	return nil
}

func (s *Service) completeExecution(ctx context.Context, prepared *executionContext, record *model.DeploymentRecord) error {
	if !completedDeploymentMatches(prepared, record) {
		if s.logger != nil {
			s.logger.Error("发布记录与流水线执行快照不一致", "operation", "pipeline_deployment_complete",
				"pipeline_run_id", prepared.run.ID, "workflow_node_id", prepared.node.ID, "err", ErrPipelineExecutionConfig)
		}
		return ErrPipelineExecutionConfig
	}
	_, hasNext, terminal := workflowNextNode(prepared.snapshot.Source, prepared.snapshot.Stages, prepared.node.ID)
	if !hasNext && !terminal {
		return ErrInvalidWorkflowTransition
	}
	message := "当前任务：" + prepared.node.Name + "；状态：已完成"
	status, stage := model.PipelineRunReady, "deploy_succeeded"
	componentStatus := model.PipelineRunRepositoryReady
	if !hasNext {
		status, stage = model.PipelineRunSucceeded, "completed"
		componentStatus = model.PipelineRunRepositorySucceeded
	}
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.PipelineRun{}).
			Where("id = ? AND execution_job_id = ? AND status = ?", prepared.run.ID, prepared.run.ExecutionJobID, model.PipelineRunRunning).
			Updates(map[string]any{
				"status": status, "stage": stage, "message": message,
				"deployment_id": record.ID, "image": record.Image, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvalidWorkflowTransition
		}
		return tx.Model(&model.PipelineRunRepository{}).Where("pipeline_run_id = ?", prepared.run.ID).
			Updates(map[string]any{"status": componentStatus, "updated_at": now}).Error
	})
	if err == nil {
		s.appendRunLog(ctx, prepared.run.ID, stage, "success", message)
	}
	if err != nil || !hasNext || prepared.run.ReleasePlanExecutionID != "" || prepared.run.ReleasePlanExecutionItemID != "" {
		return err
	}
	advanceContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := s.AdvanceRun(advanceContext, prepared.run.ID, prepared.run.CreatedBy, ""); err != nil {
		return s.failExecution(advanceContext,
			taskFailureState(prepared.run.ID, prepared.run.ExecutionJobID, prepared.node.ID, model.PipelineRunReady),
			"部署已完成，但推进后续任务失败", err)
	}
	return nil
}

func completedDeploymentMatches(prepared *executionContext, record *model.DeploymentRecord) bool {
	if prepared == nil || record == nil || record.Status != model.DeploymentSucceeded ||
		record.Operation != model.DeploymentRelease || record.PipelineRunID != prepared.run.ID ||
		record.WorkflowNodeID != prepared.node.ID || record.ArtifactID != prepared.artifact.ID ||
		record.TargetID != prepared.target.ID || record.TargetName != prepared.target.Name ||
		record.Platform != prepared.target.Platform || record.EnvironmentID != prepared.target.EnvironmentID ||
		record.HostID != prepared.target.HostID || record.RuntimeID != prepared.target.RuntimeID ||
		record.WorkingDirectory != prepared.target.WorkingDirectory || record.Namespace != prepared.target.Namespace ||
		record.WorkloadName != prepared.target.WorkloadName || record.ContainerName != prepared.target.ContainerName ||
		record.RolloutTimeout != prepared.target.RolloutTimeout || record.DeploymentPlanID != prepared.deploymentPlan.ID ||
		record.DeploymentPlanKind != prepared.deploymentPlan.Kind {
		return false
	}
	if prepared.target.Platform == model.DeploymentSSH {
		return record.Image == "" && record.ExpectedImageID == "" &&
			record.CommandDigest == model.DeploymentPlanExecutionDigest(
				prepared.deploymentPlan.Kind, prepared.deploymentPlan.Script, prepared.deploymentPlan.TimeoutSeconds,
			)
	}
	if record.Image != prepared.artifact.ImageRef {
		return false
	}
	switch prepared.artifact.StorageKind {
	case model.ArtifactStorageKindRegistry:
		if record.ExpectedImageID != "" {
			return false
		}
	case model.ArtifactStorageKindDockerDaemon:
		if !dockerengine.IsValidImageID(record.ExpectedImageID) {
			return false
		}
	default:
		return false
	}
	switch prepared.deploymentPlan.Kind {
	case model.DeploymentPlanCompose:
		return record.ComposeDigest == model.DeploymentPlanComposeExecutionDigest(
			prepared.deploymentPlan.ComposeYAML, prepared.deploymentPlan.ServiceName, prepared.deploymentPlan.TimeoutSeconds,
		)
	case model.DeploymentPlanDocker:
		return record.DockerConfigDigest == model.DockerContainerConfigDigest(prepared.deploymentPlan.DockerConfig)
	case model.DeploymentPlanKubernetes:
		return prepared.target.Platform == model.DeploymentKubernetes
	default:
		return false
	}
}

var errExecutionFailureStateChanged = errors.New("流水线任务身份已经变化")

func (s *Service) failExecution(ctx context.Context, expected executionFailureState, message string, cause error) error {
	now := time.Now().UTC()
	updateContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err := s.db.WithContext(updateContext).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.PipelineRun{}).
			Where("id = ? AND execution_job_id = ? AND current_node_id = ? AND status = ?",
				expected.runID, expected.jobID, expected.nodeID, expected.status).
			Updates(map[string]any{"status": model.PipelineRunFailed, "stage": "failed", "message": message, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errExecutionFailureStateChanged
		}
		return tx.Model(&model.PipelineRunRepository{}).Where("pipeline_run_id = ?", expected.runID).
			Updates(map[string]any{"status": model.PipelineRunRepositoryFailed, "updated_at": now}).Error
	})
	if errors.Is(err, errExecutionFailureStateChanged) {
		if s.logger != nil {
			s.logger.Warn("忽略已经过期的流水线任务失败结果", "operation", "pipeline_execute_stale_failure",
				"pipeline_run_id", expected.runID, "execution_job_id", expected.jobID,
				"workflow_node_id", expected.nodeID, "expected_status", expected.status, "err", cause)
		}
		return cause
	}
	if err != nil {
		if s.logger != nil {
			s.logger.Error("记录流水线执行失败状态失败", "operation", "pipeline_execute_failure_update",
				"pipeline_run_id", expected.runID, "execution_job_id", expected.jobID,
				"workflow_node_id", expected.nodeID, "err", err, "cause", cause)
		}
		return ErrPipelineExecutionState
	}
	if s.logger != nil {
		s.logger.Error("流水线执行失败", "operation", "pipeline_execute", "pipeline_run_id", expected.runID,
			"execution_job_id", expected.jobID, "workflow_node_id", expected.nodeID, "err", cause)
	}
	s.appendRunLog(updateContext, expected.runID, "failed", "error", message)
	return cause
}

func (s *Service) failCurrentExecution(ctx context.Context, runID, message string, cause error) error {
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).Select("id", "execution_job_id", "current_node_id", "status").First(&run, "id = ?", runID).Error; err != nil {
		if s.logger != nil {
			s.logger.Error("读取流水线失败状态前置条件失败", "operation", "pipeline_execute_failure_state",
				"pipeline_run_id", runID, "err", err)
		}
		return ErrPipelineExecutionState
	}
	return s.failExecution(ctx, failureStateForRun(run), message, cause)
}

func (s *Service) executionTaskObsolete(ctx context.Context, runID, jobID, nodeID string) (bool, error) {
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).Select("id", "execution_job_id", "current_node_id", "status").First(&run, "id = ?", runID).Error; err != nil {
		return false, err
	}
	if run.ExecutionJobID != jobID || run.CurrentNodeID != nodeID {
		return true, nil
	}
	switch run.Status {
	case model.PipelineRunSucceeded, model.PipelineRunFailed, model.PipelineRunCanceled:
		return true, nil
	default:
		return false, nil
	}
}
