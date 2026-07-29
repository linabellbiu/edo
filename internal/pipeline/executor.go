package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"zrt/internal/deployment"
	"zrt/internal/dockerengine"
	"zrt/internal/model"
)

var (
	ErrPipelineExecutionUnavailable = errors.New("流水线执行服务尚未就绪")
	ErrPipelineExecutionRunning     = errors.New("流水线正在执行，请稍后刷新")
	ErrPipelineExecutionConfig      = errors.New("流水线执行配置不完整，请检查应用、构建方案和部署配置")
	errPipelineExecutionComplete    = errors.New("流水线执行已经完成")
)

type DeployTaskPayload struct {
	PipelineRunID  string `json:"pipeline_run_id"`
	WorkflowNodeID string `json:"workflow_node_id"`
}

type executionContext struct {
	run            model.PipelineRun
	node           model.WorkflowNode
	snapshot       workflowSnapshot
	application    model.Application
	component      model.PipelineRunRepository
	buildPlan      model.BuildPlan
	deploymentPlan model.DeploymentPlan
	registry       model.ImageRegistry
	target         model.DeploymentTarget
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
		return s.failExecution(ctx, payload.PipelineRunID, "流水线执行配置不完整，请检查应用、构建方案和部署配置", err)
	}
	s.appendRunLog(ctx, prepared.run.ID, "start", "info", "流水线开始执行："+prepared.run.Ref+" · "+prepared.run.CommitSHA)
	approvedBy, err := s.latestExecutionApproval(ctx, prepared.run.ID)
	if err != nil {
		return s.failExecution(ctx, prepared.run.ID, err.Error(), err)
	}
	if prepared.target.Platform == model.DeploymentSSH {
		return s.executeSSHDeployment(ctx, prepared, approvedBy)
	}
	checkoutDirectory, err := os.MkdirTemp("", "zrt-pipeline-checkout-*")
	if err != nil {
		return s.failExecution(ctx, prepared.run.ID, "准备构建工作区失败，请稍后重试", err)
	}
	defer os.RemoveAll(checkoutDirectory)

	if err := s.updateExecutionPhase(ctx, &prepared.run, "checkout", "正在获取指定 Commit 的代码"); err != nil {
		return s.failExecution(ctx, prepared.run.ID, "更新流水线执行状态失败", err)
	}
	if err := s.repositories.Checkout(ctx, prepared.component.RepositoryID, prepared.run.Ref, prepared.run.CommitSHA, checkoutDirectory); err != nil {
		return s.failExecution(ctx, prepared.run.ID, "获取代码失败，请检查仓库连接和所选 Commit", err)
	}

	contextDirectory, dockerfile, err := buildPaths(checkoutDirectory, prepared.buildPlan)
	if err != nil {
		return s.failExecution(ctx, prepared.run.ID, "构建方案中的路径无效，请检查构建上下文和 Dockerfile", err)
	}
	image, expectedImageID, buildMessage, err := s.buildExecutionImage(ctx, prepared, contextDirectory, dockerfile)
	if err != nil {
		message := executionImageFailureMessage(
			prepared.run.Stage,
			prepared.target.Name,
			prepared.component.ImageRegistryID != "",
			err,
		)
		return s.failExecution(ctx, prepared.run.ID, message, err)
	}

	if err := s.updateExecutionPhase(ctx, &prepared.run, "deploy", buildMessage+"，正在发布到“"+prepared.target.Name+"”"); err != nil {
		return s.failExecution(ctx, prepared.run.ID, "更新流水线执行状态失败", err)
	}
	record, err := s.deployments.RequestAndRun(ctx, prepared.run.CreatedBy, deployment.RequestInput{
		TargetID: prepared.target.ID, Image: image, ExpectedImageID: expectedImageID,
		PipelineRunID: prepared.run.ID, WorkflowNodeID: prepared.node.ID, ApprovedBy: approvedBy,
	})
	if err != nil {
		return s.failExecution(ctx, prepared.run.ID, "发布执行失败，请检查目标环境和发布记录", err)
	}
	return s.completeExecution(ctx, prepared, record)
}

func (s *Service) executeSSHDeployment(ctx context.Context, prepared *executionContext, approvedBy string) error {
	if err := s.updateExecutionPhase(ctx, &prepared.run, "deploy", "正在执行命令脚本并发布到“"+prepared.target.Name+"”"); err != nil {
		return s.failExecution(ctx, prepared.run.ID, "更新流水线执行状态失败", err)
	}
	output := s.newExecutionLogWriter(ctx, prepared.run.ID, "deploy", "命令部署")
	defer output.Close()
	record, err := s.deployments.RequestCommandAndRun(ctx, prepared.run.CreatedBy, deployment.CommandRequestInput{
		TargetID: prepared.target.ID, PipelineRunID: prepared.run.ID, WorkflowNodeID: prepared.node.ID,
		ApprovedBy: approvedBy, DeploymentPlanID: prepared.component.DeploymentPlanID,
		PlanKind: prepared.component.DeploymentPlanKind, Script: prepared.component.DeploymentPlanScript,
		ScriptDigest:   prepared.component.DeploymentPlanDigest,
		TimeoutSeconds: prepared.component.DeploymentPlanTimeoutSeconds,
		Environment: map[string]string{
			"ZRT_PIPELINE_RUN_ID":      prepared.run.ID,
			"ZRT_APPLICATION_ID":       prepared.application.ID,
			"ZRT_APPLICATION_NAME":     prepared.application.Name,
			"ZRT_GIT_REF":              prepared.run.Ref,
			"ZRT_COMMIT_SHA":           prepared.run.CommitSHA,
			"ZRT_DEPLOYMENT_TARGET_ID": prepared.target.ID,
		},
		Stdout: output, Stderr: output,
	})
	if err != nil {
		return s.failExecution(ctx, prepared.run.ID, "命令脚本部署失败，请查看流水线日志", err)
	}
	return s.completeExecution(ctx, prepared, record)
}

func executionImageFailureMessage(stage, targetName string, pushesToRegistry bool, cause error) string {
	if stage == "transfer" {
		var timeoutError net.Error
		if errors.Is(cause, context.DeadlineExceeded) || (errors.As(cause, &timeoutError) && timeoutError.Timeout()) {
			return "无法连接“" + targetName + "”，SSH 连接超时"
		}
		return "镜像传输到“" + targetName + "”失败，请检查 SSH 和 Docker"
	}
	if pushesToRegistry {
		return "镜像构建或推送失败，请检查任务日志、构建方案和镜像仓库"
	}
	return "镜像构建失败，请检查任务日志和构建方案"
}

func (s *Service) loadExecution(ctx context.Context, payload DeployTaskPayload, jobID string) (*executionContext, error) {
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
	var node *model.WorkflowNode
	for i := range snapshot.Nodes {
		if snapshot.Nodes[i].ID == payload.WorkflowNodeID {
			node = &snapshot.Nodes[i]
			break
		}
	}
	if node == nil || node.Type != model.WorkflowNodeDeploy || node.Config.DeploymentTargetID == "" {
		return nil, ErrPipelineExecutionConfig
	}
	result := &executionContext{run: run, node: *node, snapshot: *snapshot}
	if err := s.db.WithContext(ctx).First(&result.application, "id = ? AND is_active = ?", run.ApplicationID, true).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Preload("Repository").First(&result.component, "pipeline_run_id = ?", run.ID).Error; err != nil {
		return nil, err
	}
	targetSnapshot, hasTargetSnapshot := snapshot.DeploymentTargets[node.ID]
	if hasTargetSnapshot {
		if targetSnapshot.ID == "" || targetSnapshot.ID != node.Config.DeploymentTargetID {
			return nil, ErrPipelineExecutionConfig
		}
		result.target = model.DeploymentTarget{
			ID: targetSnapshot.ID, Name: targetSnapshot.Name, Platform: targetSnapshot.Platform,
			EnvironmentID: targetSnapshot.EnvironmentID, HostID: targetSnapshot.HostID,
			RuntimeID: targetSnapshot.RuntimeID, WorkingDirectory: targetSnapshot.WorkingDirectory,
			Namespace: targetSnapshot.Namespace, WorkloadName: targetSnapshot.WorkloadName,
			ContainerName: targetSnapshot.ContainerName, RolloutTimeout: targetSnapshot.RolloutTimeout,
			IsActive: true,
		}
	} else if err := s.db.WithContext(ctx).First(&result.target, "id = ? AND is_active = ?", node.Config.DeploymentTargetID, true).Error; err != nil {
		return nil, err
	}
	planSnapshot, hasPlanSnapshot := snapshot.DeploymentPlans[node.ID]
	if hasPlanSnapshot {
		if planSnapshot.ID == "" || (node.Config.DeploymentPlanID != "" && planSnapshot.ID != node.Config.DeploymentPlanID) {
			return nil, ErrPipelineExecutionConfig
		}
		result.deploymentPlan = model.DeploymentPlan{
			ID: planSnapshot.ID, Kind: planSnapshot.Kind, Script: planSnapshot.Script,
			HelmChart: planSnapshot.HelmChart, HelmValues: planSnapshot.HelmValues,
			ComposeFile: planSnapshot.ComposeFile, ServiceName: planSnapshot.ServiceName,
			TimeoutSeconds: planSnapshot.TimeoutSeconds,
		}
	}
	if result.target.Platform == model.DeploymentSSH {
		if hasPlanSnapshot {
			if !validSSHDeploymentPlanSnapshot(&result.deploymentPlan, &result.target) {
				return nil, errors.New("SSH 部署方案快照无效")
			}
			return result, nil
		}
		if !validSSHExecutionSnapshot(&result.component, &result.target) {
			return nil, errors.New("SSH 部署方案快照无效")
		}
		result.deploymentPlan = model.DeploymentPlan{
			ID: result.component.DeploymentPlanID, Kind: result.component.DeploymentPlanKind,
			Script:         result.component.DeploymentPlanScript,
			TimeoutSeconds: result.component.DeploymentPlanTimeoutSeconds,
		}
		return result, nil
	}
	if s.docker == nil {
		return nil, ErrPipelineExecutionUnavailable
	}
	if err := s.db.WithContext(ctx).First(&result.buildPlan, "id = ? AND is_active = ?", result.component.BuildPlanID, true).Error; err != nil {
		return nil, err
	}
	if result.buildPlan.Kind != model.BuildPlanDockerfile {
		return nil, errors.New("当前只支持执行 Dockerfile 构建方案")
	}
	if !hasPlanSnapshot {
		if err := s.db.WithContext(ctx).First(&result.deploymentPlan, "id = ? AND is_active = ?", result.component.DeploymentPlanID, true).Error; err != nil {
			return nil, err
		}
	}
	if !deploymentPlanSupportsTarget(result.deploymentPlan.Kind, result.target.Platform) {
		return nil, ErrDeploymentPlanTargetMismatch
	}
	if result.component.ImageRegistryID != "" {
		if err := s.db.WithContext(ctx).First(&result.registry, "id = ? AND is_active = ?", result.component.ImageRegistryID, true).Error; err != nil {
			return nil, err
		}
	} else {
		if result.target.Platform != model.DeploymentDocker {
			return nil, errors.New("Kubernetes 发布必须绑定镜像仓库")
		}
		endpoint, err := s.docker.Find(ctx, result.target.RuntimeID)
		if err != nil {
			return nil, err
		}
		parsed, err := url.Parse(endpoint.Host)
		if err != nil || (parsed.Scheme != "ssh" && !dockerengine.IsLocalEndpointID(endpoint.ID)) {
			return nil, errors.New("未绑定镜像仓库时，只能部署到本地 Docker 或 Docker SSH 主机")
		}
	}
	return result, nil
}

func validSSHDeploymentPlanSnapshot(plan *model.DeploymentPlan, target *model.DeploymentTarget) bool {
	return plan != nil && target != nil && target.Platform == model.DeploymentSSH &&
		plan.ID != "" && plan.Kind == model.DeploymentPlanScript &&
		strings.TrimSpace(plan.Script) != "" && len(plan.Script) <= 256*1024 &&
		plan.TimeoutSeconds >= 30 && plan.TimeoutSeconds <= 3600 &&
		target.HostID != "" && target.EnvironmentID != ""
}

func validSSHExecutionSnapshot(component *model.PipelineRunRepository, target *model.DeploymentTarget) bool {
	if component == nil || target == nil || target.Platform != model.DeploymentSSH ||
		component.DeploymentPlanKind != model.DeploymentPlanScript ||
		strings.TrimSpace(component.DeploymentPlanScript) == "" ||
		component.DeploymentPlanTimeoutSeconds < 30 || component.DeploymentPlanTimeoutSeconds > 3600 ||
		component.DeploymentPlanDigest == "" || target.HostID == "" || target.EnvironmentID == "" {
		return false
	}
	return component.DeploymentPlanDigest == model.DeploymentPlanExecutionDigest(
		component.DeploymentPlanKind,
		component.DeploymentPlanScript,
		component.DeploymentPlanTimeoutSeconds,
	)
}

func (s *Service) buildExecutionImage(
	ctx context.Context,
	prepared *executionContext,
	contextDirectory, dockerfile string,
) (string, string, string, error) {
	timeout := time.Duration(prepared.buildPlan.TimeoutSeconds) * time.Second
	buildOutput := s.newBuildLogWriter(ctx, prepared.run.ID, "build")
	defer buildOutput.Close()
	if prepared.component.ImageRegistryID == "" {
		image, err := localExecutionImage(prepared)
		if err != nil {
			return "", "", "", err
		}
		if err := s.updateExecutionPhase(ctx, &prepared.run, "build", "正在 Docker 构建运行时构建本地镜像 "+image); err != nil {
			return "", "", "", err
		}
		imageID, err := s.docker.BuildLocal(
			ctx, contextDirectory, dockerfile, image, timeout, buildOutput,
		)
		if err != nil {
			return "", "", "", err
		}
		if dockerengine.IsLocalEndpointID(prepared.target.RuntimeID) {
			return image, imageID, "本地镜像已在 Docker 构建运行时构建并校验", nil
		}
		if err := s.updateExecutionPhase(ctx, &prepared.run, "transfer", "正在通过 SSH 将本地镜像传输到“"+prepared.target.Name+"”"); err != nil {
			return "", "", "", err
		}
		targetImageID, err := s.docker.TransferImageToSSH(ctx, prepared.target.RuntimeID, image, imageID, timeout)
		if err != nil {
			return "", "", "", err
		}
		return image, targetImageID, "本地镜像已构建、传输并校验", nil
	}

	image, registryAuth, err := s.executionImage(ctx, prepared)
	if err != nil {
		return "", "", "", err
	}
	if err := s.updateExecutionPhase(ctx, &prepared.run, "build", "正在构建并推送镜像 "+image); err != nil {
		return "", "", "", err
	}
	digest, cacheWarning, err := s.docker.BuildAndPush(
		ctx, contextDirectory, dockerfile, image, registryAuth, timeout, buildOutput,
	)
	if err != nil {
		return "", "", "", err
	}
	if cacheWarning != nil && s.logger != nil {
		s.logger.Warn("构建缓存未命中或更新失败，本次正式镜像已成功推送",
			"operation", "pipeline_build_cache", "pipeline_run_id", prepared.run.ID, "err", cacheWarning)
	}
	return digest, "", "镜像已推送", nil
}

func buildPaths(checkoutDirectory string, plan model.BuildPlan) (string, string, error) {
	contextPath := filepath.Clean(strings.TrimSpace(plan.ContextPath))
	dockerfilePath := filepath.Clean(strings.TrimSpace(plan.DockerfilePath))
	if contextPath == "" {
		contextPath = "."
	}
	if filepath.IsAbs(contextPath) || contextPath == ".." || strings.HasPrefix(contextPath, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(dockerfilePath) || dockerfilePath == ".." || strings.HasPrefix(dockerfilePath, ".."+string(filepath.Separator)) {
		return "", "", ErrInvalidBuildPlan
	}
	contextDirectory := filepath.Join(checkoutDirectory, contextPath)
	dockerfileAbsolute := filepath.Join(checkoutDirectory, dockerfilePath)
	dockerfileRelative, err := filepath.Rel(contextDirectory, dockerfileAbsolute)
	if err != nil || dockerfileRelative == ".." || strings.HasPrefix(dockerfileRelative, ".."+string(filepath.Separator)) {
		return "", "", ErrInvalidBuildPlan
	}
	return contextDirectory, dockerfileRelative, nil
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
	commit := prepared.run.CommitSHA
	if len(commit) > 12 {
		commit = commit[:12]
	}
	image := parsed.Host + "/" + path.Join(parts...) + ":" + commit
	credential := ""
	if prepared.registry.CredentialCiphertext != "" {
		credential, err = s.secrets.Decrypt(prepared.registry.CredentialCiphertext, []byte("image_registry:"+prepared.registry.Name+":credential"))
		if err != nil {
			return "", dockerengine.RegistryAuth{}, err
		}
	}
	_ = ctx
	return image, dockerengine.RegistryAuth{
		ServerAddress: prepared.registry.Endpoint, Host: parsed.Host,
		Username: prepared.registry.Username, Credential: credential,
	}, nil
}

func localExecutionImage(prepared *executionContext) (string, error) {
	name, err := executionImageName(prepared.application)
	if err != nil {
		return "", err
	}
	commit := prepared.run.CommitSHA
	if len(commit) > 12 {
		commit = commit[:12]
	}
	runID := strings.ReplaceAll(prepared.run.ID, "-", "")
	if len(runID) > 8 {
		runID = runID[:8]
	}
	if commit == "" || runID == "" {
		return "", ErrPipelineExecutionConfig
	}
	return "zrt.local/" + name + ":" + commit + "-" + runID, nil
}

func executionImageName(application model.Application) (string, error) {
	name := strings.Trim(imageNamePartPattern.ReplaceAllString(strings.ToLower(application.Name), "-"), "-._")
	if name != "" {
		return name, nil
	}
	applicationID := strings.ReplaceAll(application.ID, "-", "")
	if len(applicationID) > 8 {
		applicationID = applicationID[:8]
	}
	if applicationID == "" {
		return "", ErrInvalidApplication
	}
	return "app-" + applicationID, nil
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
	hasNext := false
	for i := range prepared.snapshot.Edges {
		if prepared.snapshot.Edges[i].Source == prepared.node.ID {
			hasNext = true
			break
		}
	}
	message := "当前节点：" + prepared.node.Name + "；状态：已完成"
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
	return err
}

func (s *Service) failExecution(ctx context.Context, runID, message string, cause error) error {
	if s.logger != nil {
		s.logger.Error("流水线执行失败", "operation", "pipeline_execute", "pipeline_run_id", runID, "err", cause)
	}
	now := time.Now().UTC()
	updateContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	s.appendRunLog(updateContext, runID, "failed", "error", message)
	if err := s.db.WithContext(updateContext).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.PipelineRun{}).Where("id = ? AND status <> ?", runID, model.PipelineRunSucceeded).
			Updates(map[string]any{"status": model.PipelineRunFailed, "stage": "failed", "message": message, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&model.PipelineRunRepository{}).Where("pipeline_run_id = ?", runID).
			Updates(map[string]any{"status": model.PipelineRunRepositoryFailed, "updated_at": now}).Error
	}); err != nil {
		return fmt.Errorf("记录流水线执行失败状态失败: %v；原始错误: %w", err, cause)
	}
	return cause
}
