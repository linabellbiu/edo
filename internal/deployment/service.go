package deployment

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bsm/redislock"
	"github.com/distribution/reference"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"k8s.io/apimachinery/pkg/util/validation"

	"edo/internal/dockerengine"
	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/sshdeploy"
	"edo/internal/task"
)

var (
	ErrInvalidTarget                = errors.New("部署配置无效")
	ErrEnvironmentTargetUnavailable = errors.New("当前环境没有可用的执行主机")
	ErrEnvironmentTargetAmbiguous   = errors.New("当前环境存在多个可用的执行主机，请先调整环境配置")
	ErrTargetExists                 = errors.New("部署配置名称已存在")
	ErrTargetNotFound               = errors.New("部署配置不存在")
	ErrInvalidImage                 = errors.New("容器镜像引用无效")
	ErrImmutableImageRequired       = errors.New("镜像仓库或 Kubernetes 发布必须使用带摘要的不可变镜像")
	ErrDeploymentNotFound           = errors.New("发布记录不存在")
	ErrInvalidDeploymentState       = errors.New("发布记录当前状态不允许此操作")
	ErrRollbackUnavailable          = errors.New("该发布记录没有可回滚的上一镜像")
	ErrCommandPipelineRequired      = errors.New("命令脚本发布必须从流水线部署节点发起")
	ErrPipelineReleaseIdentity      = errors.New("流水线发布缺少幂等标识")
	ErrPipelineReleaseRunning       = errors.New("该流水线节点已有发布正在执行，不能重复发布")
	ErrPipelineReleaseFailed        = errors.New("该流水线节点的发布已经失败，请重新执行流水线")
	ErrPipelineReleaseConflict      = errors.New("该流水线节点已经创建发布记录，不能重复发布")
)

var targetNamePattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_. -]{0,127}$`)
var workloadNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,252}$`)

type TargetInput struct {
	Name             string
	Description      string
	Platform         model.DeploymentPlatform
	EnvironmentID    string
	RuntimeID        string
	HostID           string
	WorkingDirectory string
	Namespace        string
	WorkloadName     string
	ContainerName    string
	RolloutTimeout   int
}

type RequestInput struct {
	TargetID           string
	ApplicationID      string
	ApplicationName    string
	ArtifactID         string
	Image              string
	ImageDisplay       string
	ExpectedImageID    string
	PipelineRunID      string
	WorkflowNodeID     string
	ApprovedBy         string
	DeploymentPlanID   string
	PlanKind           model.DeploymentPlanKind
	ComposeYAML        string
	ComposeService     string
	ComposeDigest      string
	DockerConfig       model.DockerContainerConfig
	DockerConfigDigest string
	RegistryAuth       dockerengine.RegistryAuth
	TimeoutSeconds     int
	Stdout             io.Writer
	Stderr             io.Writer
}

type CommandRequestInput struct {
	TargetID         string
	ArtifactID       string
	PipelineRunID    string
	WorkflowNodeID   string
	ApprovedBy       string
	DeploymentPlanID string
	PlanKind         model.DeploymentPlanKind
	Script           string
	ScriptDigest     string
	TimeoutSeconds   int
	Environment      map[string]string
	Artifact         io.Reader
	ArtifactName     string
	ArtifactDigest   string
	Stdout           io.Writer
	Stderr           io.Writer
}

type TaskPayload struct {
	DeploymentID string `json:"deployment_id"`
}

type hostScriptRunner interface {
	RunHostDeploymentScript(context.Context, sshdeploy.Input) (sshdeploy.Result, error)
}

type Service struct {
	db            *gorm.DB
	docker        *dockerengine.Service
	kube          *kube.Service
	ssh           hostScriptRunner
	locks         *redislock.Client
	lockKeyPrefix string
	logger        *slog.Logger
}

func NewService(
	db *gorm.DB,
	docker *dockerengine.Service,
	kubeService *kube.Service,
	sshService hostScriptRunner,
	locks *redislock.Client,
	lockKeyPrefix string,
	logger *slog.Logger,
) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		db:            db,
		docker:        docker,
		kube:          kubeService,
		ssh:           sshService,
		locks:         locks,
		lockKeyPrefix: strings.TrimSuffix(strings.TrimSpace(lockKeyPrefix), ":"),
		logger:        logger,
	}
}

// WithTransaction 让部署位置与上层聚合资源共享同一个数据库事务。
// 返回的是浅拷贝，不会改变正在处理其他请求的 Service。
func (s *Service) WithTransaction(tx *gorm.DB) *Service {
	if s == nil || tx == nil {
		return s
	}
	clone := *s
	clone.db = tx
	clone.docker = s.docker.WithTransaction(tx)
	clone.kube = s.kube.WithTransaction(tx)
	return &clone
}

func (s *Service) ListTargets(ctx context.Context) ([]model.DeploymentTarget, error) {
	var targets []model.DeploymentTarget
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&targets).Error; err != nil {
		return nil, fmt.Errorf("查询发布目标失败: %w", err)
	}
	return targets, nil
}

func (s *Service) CreateTarget(ctx context.Context, actorID string, input TargetInput) (*model.DeploymentTarget, error) {
	input, err := s.normalizeTarget(ctx, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	target := &model.DeploymentTarget{
		ID: uuid.NewString(), Name: input.Name, Description: input.Description,
		Platform:      input.Platform,
		EnvironmentID: input.EnvironmentID, HostID: input.HostID,
		RuntimeID: input.RuntimeID, WorkingDirectory: input.WorkingDirectory,
		Namespace: input.Namespace, WorkloadName: input.WorkloadName,
		ContainerName: input.ContainerName, RolloutTimeout: input.RolloutTimeout,
		IsActive: true, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(target).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrTargetExists
		}
		return nil, fmt.Errorf("创建发布目标失败: %w", err)
	}
	return target, nil
}

func (s *Service) UpdateTarget(ctx context.Context, id string, input TargetInput) (*model.DeploymentTarget, error) {
	var existing model.DeploymentTarget
	if err := s.db.WithContext(ctx).First(&existing, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTargetNotFound
		}
		return nil, fmt.Errorf("查询发布目标失败: %w", err)
	}
	input, err := s.normalizeTarget(ctx, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&existing).Updates(map[string]any{
		"name": input.Name, "description": input.Description,
		"platform":       input.Platform,
		"environment_id": input.EnvironmentID, "host_id": input.HostID,
		"runtime_id": input.RuntimeID, "working_directory": input.WorkingDirectory, "namespace": input.Namespace,
		"workload_name": input.WorkloadName, "container_name": input.ContainerName,
		"rollout_timeout": input.RolloutTimeout, "updated_at": now,
	}).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrTargetExists
		}
		return nil, fmt.Errorf("更新发布目标失败: %w", err)
	}
	existing.Name, existing.Description = input.Name, input.Description
	existing.Platform = input.Platform
	existing.EnvironmentID, existing.HostID = input.EnvironmentID, input.HostID
	existing.RuntimeID, existing.WorkingDirectory, existing.Namespace = input.RuntimeID, input.WorkingDirectory, input.Namespace
	existing.WorkloadName, existing.ContainerName = input.WorkloadName, input.ContainerName
	existing.RolloutTimeout, existing.UpdatedAt = input.RolloutTimeout, now
	return &existing, nil
}

func (s *Service) SetTargetActive(ctx context.Context, id string, active bool) error {
	result := s.db.WithContext(ctx).Model(&model.DeploymentTarget{}).Where("id = ?", id).
		Updates(map[string]any{"is_active": active, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("修改发布目标状态失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrTargetNotFound
	}
	return nil
}

func (s *Service) List(ctx context.Context, limit int) ([]model.DeploymentRecord, error) {
	return s.list(ctx, "", limit)
}

func (s *Service) ListForPipelineRun(ctx context.Context, pipelineRunID string, limit int) ([]model.DeploymentRecord, error) {
	normalizedID := strings.TrimSpace(pipelineRunID)
	if normalizedID == "" || normalizedID != pipelineRunID || len(normalizedID) > 36 || strings.ContainsAny(normalizedID, "\x00\r\n") {
		return nil, ErrInvalidDeploymentState
	}
	return s.list(ctx, normalizedID, limit)
}

func (s *Service) list(ctx context.Context, pipelineRunID string, limit int) ([]model.DeploymentRecord, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	var records []model.DeploymentRecord
	query := s.db.WithContext(ctx)
	if pipelineRunID != "" {
		query = query.Where("pipeline_run_id = ?", pipelineRunID)
	}
	if err := query.Order("created_at DESC").Limit(limit).Find(&records).Error; err != nil {
		return nil, fmt.Errorf("查询发布记录失败: %w", err)
	}
	return records, nil
}

func (s *Service) Request(ctx context.Context, actorID string, input RequestInput) (*model.DeploymentRecord, error) {
	target, err := s.findTarget(ctx, input.TargetID)
	if err != nil {
		return nil, err
	}
	if target.Platform == model.DeploymentSSH {
		return nil, ErrCommandPipelineRequired
	}
	image, err := validateImage(input.Image, true)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	record := &model.DeploymentRecord{
		ID: uuid.NewString(), PipelineRunID: input.PipelineRunID, WorkflowNodeID: input.WorkflowNodeID,
		ArtifactID: input.ArtifactID, TargetID: target.ID, Operation: model.DeploymentRelease,
		Image: image, Status: model.DeploymentQueued, RequestedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	applyTargetSnapshot(record, target)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(record).Error; err != nil {
			return err
		}
		return s.enqueue(ctx, tx, record, "deploy.runtime")
	})
	if err != nil {
		return nil, fmt.Errorf("创建发布申请失败: %w", err)
	}
	return record, nil
}

// RequestAndRun 用于流水线部署节点的单次执行任务。审核人仅在上游实际经过审核节点时作为审计快照写入。
// 发布任务自身不自动重试，避免重复产生外部副作用。
func (s *Service) RequestAndRun(ctx context.Context, actorID string, input RequestInput) (*model.DeploymentRecord, error) {
	target, err := s.findTarget(ctx, input.TargetID)
	if err != nil {
		return nil, err
	}
	return s.requestAndRun(ctx, actorID, input, target)
}

// RequestSnapshotAndRun 只供流水线执行器使用。target 必须来自服务端创建运行时保存的
// 不可变快照，执行过程中不再读取可能已被修改的部署目标。
func (s *Service) RequestSnapshotAndRun(
	ctx context.Context,
	actorID string,
	target model.DeploymentTarget,
	input RequestInput,
) (*model.DeploymentRecord, error) {
	return s.requestAndRun(ctx, actorID, input, &target)
}

func (s *Service) requestAndRun(
	ctx context.Context,
	actorID string,
	input RequestInput,
	target *model.DeploymentTarget,
) (*model.DeploymentRecord, error) {
	if !validExecutionTargetSnapshot(input.TargetID, target) {
		return nil, ErrInvalidTarget
	}
	if target.Platform == model.DeploymentSSH {
		return nil, ErrCommandPipelineRequired
	}
	image, err := validatePipelineImage(input.Image, input.ExpectedImageID, target)
	if err != nil {
		return nil, err
	}
	input.ImageDisplay = strings.TrimSpace(input.ImageDisplay)
	if len(input.ImageDisplay) > 255 || strings.ContainsAny(input.ImageDisplay, "\x00\r\n") {
		return nil, ErrInvalidImage
	}
	if input.PlanKind == model.DeploymentPlanCompose {
		input.DeploymentPlanID = strings.TrimSpace(input.DeploymentPlanID)
		input.ComposeYAML = strings.TrimSpace(input.ComposeYAML)
		if input.ComposeYAML != "" {
			input.ComposeYAML += "\n"
		}
		input.ComposeService = strings.TrimSpace(input.ComposeService)
		input.ComposeDigest = strings.TrimSpace(input.ComposeDigest)
		expectedDigest := model.DeploymentPlanComposeExecutionDigest(input.ComposeYAML, input.ComposeService, input.TimeoutSeconds)
		if target.Platform != model.DeploymentDocker || input.DeploymentPlanID == "" ||
			input.TimeoutSeconds < 30 || input.TimeoutSeconds > 3600 || input.ComposeDigest == "" ||
			input.ComposeDigest != expectedDigest || s.docker == nil ||
			dockerengine.ValidateComposeYAML(input.ComposeYAML, input.ComposeService) != nil {
			return nil, ErrInvalidTarget
		}
		input.DockerConfig, input.DockerConfigDigest = model.DockerContainerConfig{}, ""
	} else if input.PlanKind == model.DeploymentPlanDocker {
		input.DeploymentPlanID = strings.TrimSpace(input.DeploymentPlanID)
		input.DockerConfigDigest = strings.TrimSpace(input.DockerConfigDigest)
		normalized, configErr := dockerengine.NormalizeContainerConfig(input.DockerConfig)
		expectedDigest := model.DockerContainerConfigDigest(normalized)
		if target.Platform != model.DeploymentDocker || input.DeploymentPlanID == "" || configErr != nil ||
			input.DockerConfigDigest == "" || input.DockerConfigDigest != expectedDigest || s.docker == nil {
			return nil, ErrInvalidTarget
		}
		input.DockerConfig = normalized
		input.ComposeYAML, input.ComposeService, input.ComposeDigest, input.TimeoutSeconds = "", "", "", 0
	} else {
		input.ComposeYAML, input.ComposeService, input.ComposeDigest, input.TimeoutSeconds = "", "", "", 0
		input.DockerConfig, input.DockerConfigDigest = model.DockerContainerConfig{}, ""
	}
	if target.Platform == model.DeploymentDocker && input.PlanKind == model.DeploymentPlanDocker {
		workloadName, nameErr := ResolveDockerWorkloadName(
			target.WorkloadName, input.ApplicationName, input.ApplicationID, input.DeploymentPlanID, target.ID,
		)
		if nameErr != nil {
			return nil, nameErr
		}
		targetSnapshot := *target
		targetSnapshot.WorkloadName = workloadName
		target = &targetSnapshot
	} else if target.Platform == model.DeploymentDocker && strings.TrimSpace(target.WorkloadName) == "" {
		return nil, ErrInvalidTarget
	}
	input.PipelineRunID = strings.TrimSpace(input.PipelineRunID)
	input.WorkflowNodeID = strings.TrimSpace(input.WorkflowNodeID)
	idempotencyKey, err := pipelineReleaseIdempotencyKey(input.PipelineRunID, input.WorkflowNodeID)
	if err != nil {
		s.logger.Error("生成流水线发布幂等标识失败", "operation", "pipeline_release_idempotency",
			"pipeline_run_id", input.PipelineRunID, "workflow_node_id", input.WorkflowNodeID, "err", err)
		return nil, err
	}
	now := time.Now().UTC()
	record := &model.DeploymentRecord{
		ID: uuid.NewString(), IdempotencyKey: &idempotencyKey,
		PipelineRunID: input.PipelineRunID, WorkflowNodeID: input.WorkflowNodeID,
		ArtifactID: input.ArtifactID, TargetID: target.ID, Operation: model.DeploymentRelease,
		Image: image, ImageDisplay: strings.TrimSpace(input.ImageDisplay),
		ExpectedImageID: strings.TrimSpace(input.ExpectedImageID),
		Status:          model.DeploymentQueued, RequestedBy: actorID, CreatedAt: now, UpdatedAt: now,
		DeploymentPlanID: input.DeploymentPlanID, DeploymentPlanKind: input.PlanKind,
		ComposeYAML: input.ComposeYAML, ComposeService: input.ComposeService,
		ComposeDigest: input.ComposeDigest, ComposeTimeout: input.TimeoutSeconds,
		DockerConfig: input.DockerConfig, DockerConfigDigest: input.DockerConfigDigest,
	}
	if input.ApprovedBy != "" {
		record.ApprovedBy, record.ApprovedAt = &input.ApprovedBy, &now
	}
	applyTargetSnapshot(record, target)
	record, created, err := s.createPipelineReleaseRecord(ctx, record)
	if err != nil {
		return record, err
	}
	if !created {
		return record, nil
	}
	executionErr := s.run(ctx, record.ID, input.ExpectedImageID, &commandExecution{
		registryAuth: input.RegistryAuth, stdout: input.Stdout, stderr: input.Stderr,
	})
	return s.loadPipelineReleaseResult(ctx, record, executionErr)
}

type commandExecution struct {
	environment    map[string]string
	artifact       io.Reader
	artifactName   string
	artifactDigest string
	stdout         io.Writer
	stderr         io.Writer
	registryAuth   dockerengine.RegistryAuth
}

func (s *Service) RequestCommandAndRun(ctx context.Context, actorID string, input CommandRequestInput) (*model.DeploymentRecord, error) {
	target, err := s.findTarget(ctx, input.TargetID)
	if err != nil {
		return nil, err
	}
	return s.requestCommandAndRun(ctx, actorID, input, target)
}

// RequestCommandSnapshotAndRun 与 RequestSnapshotAndRun 相同，但用于 SSH 脚本部署。
func (s *Service) RequestCommandSnapshotAndRun(
	ctx context.Context,
	actorID string,
	target model.DeploymentTarget,
	input CommandRequestInput,
) (*model.DeploymentRecord, error) {
	return s.requestCommandAndRun(ctx, actorID, input, &target)
}

func (s *Service) requestCommandAndRun(
	ctx context.Context,
	actorID string,
	input CommandRequestInput,
	target *model.DeploymentTarget,
) (*model.DeploymentRecord, error) {
	if !validExecutionTargetSnapshot(input.TargetID, target) {
		return nil, ErrInvalidTarget
	}
	input.ScriptDigest = strings.TrimSpace(input.ScriptDigest)
	input.DeploymentPlanID = strings.TrimSpace(input.DeploymentPlanID)
	expectedDigest := model.DeploymentPlanExecutionDigest(input.PlanKind, input.Script, input.TimeoutSeconds)
	if target.Platform != model.DeploymentSSH || input.PlanKind != model.DeploymentPlanScript ||
		input.DeploymentPlanID == "" || strings.TrimSpace(input.Script) == "" || len(input.Script) > 256*1024 ||
		input.TimeoutSeconds < 30 || input.TimeoutSeconds > 3600 || input.ScriptDigest == "" ||
		input.ScriptDigest != expectedDigest || s.ssh == nil {
		return nil, ErrInvalidTarget
	}
	input.PipelineRunID = strings.TrimSpace(input.PipelineRunID)
	input.WorkflowNodeID = strings.TrimSpace(input.WorkflowNodeID)
	idempotencyKey, err := pipelineReleaseIdempotencyKey(input.PipelineRunID, input.WorkflowNodeID)
	if err != nil {
		s.logger.Error("生成命令脚本流水线发布幂等标识失败", "operation", "pipeline_release_idempotency",
			"pipeline_run_id", input.PipelineRunID, "workflow_node_id", input.WorkflowNodeID, "err", err)
		return nil, err
	}
	now := time.Now().UTC()
	record := &model.DeploymentRecord{
		ID: uuid.NewString(), IdempotencyKey: &idempotencyKey,
		PipelineRunID: input.PipelineRunID, WorkflowNodeID: input.WorkflowNodeID,
		ArtifactID: input.ArtifactID, TargetID: target.ID, Operation: model.DeploymentRelease, Status: model.DeploymentQueued,
		RequestedBy: actorID, CreatedAt: now, UpdatedAt: now,
		DeploymentPlanID: input.DeploymentPlanID, DeploymentPlanKind: input.PlanKind,
		CommandScript: input.Script, CommandDigest: input.ScriptDigest, CommandTimeout: input.TimeoutSeconds,
	}
	if input.ApprovedBy != "" {
		record.ApprovedBy, record.ApprovedAt = &input.ApprovedBy, &now
	}
	applyTargetSnapshot(record, target)
	record, created, err := s.createPipelineReleaseRecord(ctx, record)
	if err != nil {
		return record, err
	}
	if !created {
		return record, nil
	}
	environment := make(map[string]string, len(input.Environment)+1)
	for key, value := range input.Environment {
		environment[key] = value
	}
	environment["EDO_DEPLOYMENT_ID"] = record.ID
	executionErr := s.run(ctx, record.ID, "", &commandExecution{
		environment: environment, artifact: input.Artifact, artifactName: input.ArtifactName,
		artifactDigest: input.ArtifactDigest, stdout: input.Stdout, stderr: input.Stderr,
	})
	return s.loadPipelineReleaseResult(ctx, record, executionErr)
}

func (s *Service) loadPipelineReleaseResult(
	ctx context.Context,
	record *model.DeploymentRecord,
	executionErr error,
) (*model.DeploymentRecord, error) {
	if record == nil || record.ID == "" {
		if executionErr != nil {
			return record, executionErr
		}
		return nil, ErrDeploymentNotFound
	}
	loadContext := ctx
	cancel := func() {}
	if executionErr != nil {
		loadContext, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	}
	defer cancel()
	if err := s.db.WithContext(loadContext).First(record, "id = ?", record.ID).Error; err != nil {
		if executionErr != nil {
			s.logger.Error("读取流水线发布失败结果失败", "operation", "pipeline_release_result_load",
				"deployment_id", record.ID, "err", err, "cause", executionErr)
			return record, executionErr
		}
		return nil, fmt.Errorf("读取流水线发布结果失败: %w", err)
	}
	return record, executionErr
}

func validExecutionTargetSnapshot(targetID string, target *model.DeploymentTarget) bool {
	if target == nil || strings.TrimSpace(targetID) == "" || target.ID != strings.TrimSpace(targetID) ||
		target.RolloutTimeout < 30 || target.RolloutTimeout > 3600 {
		return false
	}
	switch target.Platform {
	case model.DeploymentSSH:
		return target.EnvironmentID != "" && target.HostID != ""
	case model.DeploymentDocker:
		return target.RuntimeID != "" && (strings.TrimSpace(target.WorkloadName) == "" || workloadNamePattern.MatchString(target.WorkloadName))
	case model.DeploymentKubernetes:
		return target.RuntimeID != "" && len(validation.IsDNS1123Label(target.Namespace)) == 0 &&
			len(validation.IsDNS1123Subdomain(target.WorkloadName)) == 0 &&
			len(validation.IsDNS1123Label(target.ContainerName)) == 0
	default:
		return false
	}
}

func pipelineReleaseIdempotencyKey(pipelineRunID, workflowNodeID string) (string, error) {
	pipelineRunID = strings.TrimSpace(pipelineRunID)
	workflowNodeID = strings.TrimSpace(workflowNodeID)
	if pipelineRunID == "" || workflowNodeID == "" || len(pipelineRunID) > 36 || len(workflowNodeID) > 64 ||
		strings.ContainsAny(pipelineRunID, "\r\n\x00") || strings.ContainsAny(workflowNodeID, "\r\n\x00") {
		return "", ErrPipelineReleaseIdentity
	}
	digest := sha256.Sum256([]byte(string(model.DeploymentRelease) + "\x00" + pipelineRunID + "\x00" + workflowNodeID))
	return fmt.Sprintf("%x", digest), nil
}

func (s *Service) createPipelineReleaseRecord(
	ctx context.Context,
	record *model.DeploymentRecord,
) (*model.DeploymentRecord, bool, error) {
	if record == nil || record.IdempotencyKey == nil || *record.IdempotencyKey == "" ||
		record.Operation != model.DeploymentRelease {
		s.logger.Error("创建流水线发布记录时幂等上下文无效", "operation", "pipeline_release_create", "err", ErrPipelineReleaseIdentity)
		return nil, false, ErrPipelineReleaseIdentity
	}
	if err := s.db.WithContext(ctx).Create(record).Error; err == nil {
		return record, true, nil
	} else if !errors.Is(err, gorm.ErrDuplicatedKey) {
		s.logger.Error("创建流水线发布记录失败", "operation", "pipeline_release_create",
			"pipeline_run_id", record.PipelineRunID, "workflow_node_id", record.WorkflowNodeID, "err", err)
		return nil, false, fmt.Errorf("创建流水线发布记录失败: %w", err)
	}

	var existing model.DeploymentRecord
	if err := s.db.WithContext(ctx).Where("idempotency_key = ?", *record.IdempotencyKey).First(&existing).Error; err != nil {
		s.logger.Error("读取已存在的流水线发布记录失败", "operation", "pipeline_release_idempotency",
			"pipeline_run_id", record.PipelineRunID, "workflow_node_id", record.WorkflowNodeID, "err", err)
		return nil, false, fmt.Errorf("读取流水线发布记录失败: %w", err)
	}
	if !samePipelineReleaseSemantics(&existing, record) {
		s.logger.Error("流水线发布幂等标识对应的执行语义不一致", "operation", "pipeline_release_idempotency",
			"pipeline_run_id", record.PipelineRunID, "workflow_node_id", record.WorkflowNodeID,
			"deployment_id", existing.ID, "err", ErrPipelineReleaseConflict)
		return &existing, false, ErrPipelineReleaseConflict
	}
	if existing.Status == model.DeploymentSucceeded {
		s.logger.Info("复用已成功的流水线发布记录", "operation", "pipeline_release_idempotency",
			"pipeline_run_id", existing.PipelineRunID, "workflow_node_id", existing.WorkflowNodeID,
			"deployment_id", existing.ID)
		return &existing, false, nil
	}

	conflict := ErrPipelineReleaseConflict
	if existing.Status == model.DeploymentQueued || existing.Status == model.DeploymentRunning {
		conflict = ErrPipelineReleaseRunning
	} else if existing.Status == model.DeploymentFailed {
		conflict = ErrPipelineReleaseFailed
	}
	s.logger.Error("拒绝重复执行流水线发布", "operation", "pipeline_release_idempotency",
		"pipeline_run_id", existing.PipelineRunID, "workflow_node_id", existing.WorkflowNodeID,
		"deployment_id", existing.ID, "deployment_status", existing.Status, "err", conflict)
	return &existing, false, conflict
}

func samePipelineReleaseSemantics(existing, requested *model.DeploymentRecord) bool {
	if existing == nil || requested == nil {
		return false
	}
	return existing.Operation == requested.Operation &&
		existing.PipelineRunID == requested.PipelineRunID && existing.WorkflowNodeID == requested.WorkflowNodeID &&
		existing.ArtifactID == requested.ArtifactID && existing.TargetID == requested.TargetID &&
		existing.TargetName == requested.TargetName && existing.Platform == requested.Platform &&
		existing.EnvironmentID == requested.EnvironmentID && existing.HostID == requested.HostID &&
		existing.RuntimeID == requested.RuntimeID && existing.WorkingDirectory == requested.WorkingDirectory &&
		existing.Namespace == requested.Namespace && existing.WorkloadName == requested.WorkloadName &&
		existing.ContainerName == requested.ContainerName && existing.RolloutTimeout == requested.RolloutTimeout &&
		existing.Image == requested.Image && existing.ImageDisplay == requested.ImageDisplay &&
		existing.ExpectedImageID == requested.ExpectedImageID &&
		existing.DeploymentPlanID == requested.DeploymentPlanID && existing.DeploymentPlanKind == requested.DeploymentPlanKind &&
		existing.CommandScript == requested.CommandScript && existing.CommandDigest == requested.CommandDigest &&
		existing.CommandTimeout == requested.CommandTimeout && existing.ComposeYAML == requested.ComposeYAML &&
		existing.ComposeService == requested.ComposeService && existing.ComposeDigest == requested.ComposeDigest &&
		existing.ComposeTimeout == requested.ComposeTimeout && existing.DockerConfigDigest == requested.DockerConfigDigest &&
		reflect.DeepEqual(existing.DockerConfig, requested.DockerConfig)
}

func (s *Service) Rollback(ctx context.Context, sourceID, actorID string) (*model.DeploymentRecord, error) {
	var source model.DeploymentRecord
	if err := s.db.WithContext(ctx).First(&source, "id = ?", sourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDeploymentNotFound
		}
		s.logger.Error("查询待回滚发布记录失败", "operation", "deployment_rollback_source", "source_deployment_id", sourceID, "err", err)
		return nil, fmt.Errorf("查询待回滚发布记录失败: %w", err)
	}
	if source.Platform == model.DeploymentSSH || source.DeploymentPlanKind == model.DeploymentPlanCompose ||
		source.Status != model.DeploymentSucceeded || source.PreviousImage == "" {
		return nil, ErrRollbackUnavailable
	}
	image := strings.TrimSpace(source.PreviousImage)
	expectedImageID := ""
	switch source.Platform {
	case model.DeploymentDocker:
		if !dockerengine.IsValidImageID(source.PreviousImageID) {
			return nil, ErrRollbackUnavailable
		}
		// Docker 回滚直接按上一容器实际使用的 Image ID 执行；标签只用于历史展示，
		// 不能再次成为回滚时的可变解析入口。
		image = source.PreviousImageID
		expectedImageID = source.PreviousImageID
	case model.DeploymentKubernetes:
		var err error
		image, err = validateImage(image, true)
		if err != nil {
			return nil, ErrRollbackUnavailable
		}
	default:
		return nil, ErrRollbackUnavailable
	}
	now := time.Now().UTC()
	prototype := &model.DeploymentRecord{
		TargetID: source.TargetID, Operation: model.DeploymentRollback,
		Image: image, ExpectedImageID: expectedImageID, Status: model.DeploymentQueued,
		DeploymentPlanID: source.DeploymentPlanID, DeploymentPlanKind: source.DeploymentPlanKind,
		DockerConfig: source.DockerConfig, DockerConfigDigest: source.DockerConfigDigest,
		RequestedBy: actorID, ApprovedBy: &actorID, ApprovedAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}
	copyTargetSnapshot(prototype, &source)

	record, created, err := s.createRollbackAttempt(ctx, &source, prototype)
	if err != nil {
		s.logger.Error("创建回滚任务失败", "operation", "deployment_rollback_create", "source_deployment_id", source.ID, "err", err)
		return nil, fmt.Errorf("创建回滚任务失败: %w", err)
	}
	if !created {
		s.logger.Info("复用已有的回滚任务", "operation", "deployment_rollback_idempotency",
			"source_deployment_id", source.ID, "deployment_id", record.ID,
			"rollback_attempt", record.RollbackAttempt, "deployment_status", record.Status)
	}
	return record, nil
}

func (s *Service) createRollbackAttempt(
	ctx context.Context,
	source *model.DeploymentRecord,
	prototype *model.DeploymentRecord,
) (*model.DeploymentRecord, bool, error) {
	if source == nil || prototype == nil {
		return nil, false, ErrInvalidDeploymentState
	}
	legacyKey, err := rollbackIdempotencyKey(source.ID, 1)
	if err != nil {
		return nil, false, err
	}
	var selected *model.DeploymentRecord
	created := false
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 在读取最近尝试之前先用不改变值的 UPDATE 获取写锁。PostgreSQL/MySQL
		// 只锁定来源行；SQLite 没有行锁，但提前取得写锁可避免多个读事务随后
		// 同时升级为写事务。幂等键唯一索引仍是最后一道并发保护。
		if err := tx.Model(&model.DeploymentRecord{}).Where("id = ?", source.ID).
			UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return fmt.Errorf("锁定回滚来源发布记录失败: %w", err)
		}
		latest, err := findLatestRollbackAttempt(tx, source.ID, legacyKey)
		if err != nil {
			return err
		}
		attempt := 1
		if latest != nil {
			if latest.RollbackAttempt < 1 {
				return fmt.Errorf("回滚尝试次数无效: deployment_id=%s", latest.ID)
			}
			if latest.Status != model.DeploymentFailed {
				selected = latest
				return nil
			}
			attempt = latest.RollbackAttempt + 1
			if attempt <= latest.RollbackAttempt {
				return fmt.Errorf("回滚尝试次数溢出: deployment_id=%s", latest.ID)
			}
		}
		idempotencyKey, err := rollbackIdempotencyKey(source.ID, attempt)
		if err != nil {
			return err
		}
		record := *prototype
		record.ID = uuid.NewString()
		record.IdempotencyKey = &idempotencyKey
		record.RollbackSourceID = source.ID
		record.RollbackAttempt = attempt
		result := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "idempotency_key"}},
			DoNothing: true,
		}).Create(&record)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var existing model.DeploymentRecord
			if err := tx.Where("idempotency_key = ?", idempotencyKey).First(&existing).Error; err != nil {
				return fmt.Errorf("读取并发创建的回滚任务失败: %w", err)
			}
			if existing.Operation != model.DeploymentRollback || existing.RollbackSourceID != source.ID ||
				existing.RollbackAttempt != attempt {
				return fmt.Errorf("回滚幂等标识对应的执行语义不一致: deployment_id=%s", existing.ID)
			}
			selected = &existing
			return nil
		}
		created = true
		selected = &record
		return s.enqueue(ctx, tx, &record, "rollback.runtime")
	}); err != nil {
		return nil, false, err
	}
	if selected == nil {
		return nil, false, errors.New("回滚任务状态异常")
	}
	return selected, created, nil
}

func findLatestRollbackAttempt(tx *gorm.DB, sourceID, legacyKey string) (*model.DeploymentRecord, error) {
	var latest model.DeploymentRecord
	err := tx.Where("rollback_source_id = ? AND operation = ?", sourceID, model.DeploymentRollback).
		Order("rollback_attempt DESC").First(&latest).Error
	if err == nil {
		return &latest, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("查询最近一次回滚任务失败: %w", err)
	}

	// 202607300051 之前的回滚只保存了来源发布的哈希幂等键。该键能够精确
	// 定位第 1 次尝试，惰性补齐关联可保留历史记录，并允许失败后创建第 2 次尝试。
	var legacy model.DeploymentRecord
	err = tx.Where("idempotency_key = ? AND operation = ?", legacyKey, model.DeploymentRollback).First(&legacy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询历史回滚任务失败: %w", err)
	}
	if legacy.RollbackSourceID == "" && legacy.RollbackAttempt == 0 {
		result := tx.Model(&model.DeploymentRecord{}).
			Where("id = ? AND rollback_source_id = ? AND rollback_attempt = ?", legacy.ID, "", 0).
			Updates(map[string]any{"rollback_source_id": sourceID, "rollback_attempt": 1})
		if result.Error != nil {
			return nil, fmt.Errorf("补齐历史回滚任务关联失败: %w", result.Error)
		}
		legacy.RollbackSourceID = sourceID
		legacy.RollbackAttempt = 1
	}
	if legacy.RollbackSourceID != sourceID || legacy.RollbackAttempt != 1 {
		return nil, fmt.Errorf("历史回滚幂等标识对应的执行语义不一致: deployment_id=%s", legacy.ID)
	}
	return &legacy, nil
}

func rollbackIdempotencyKey(sourceID string, attempt int) (string, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" || len(sourceID) > 36 || strings.ContainsAny(sourceID, "\r\n\x00") {
		return "", ErrDeploymentNotFound
	}
	if attempt < 1 {
		return "", ErrInvalidDeploymentState
	}
	seed := string(model.DeploymentRollback) + "\x00" + sourceID
	// 第 1 次尝试保持旧算法，确保升级后仍能识别并复用既有回滚记录。
	if attempt > 1 {
		seed += "\x00" + strconv.Itoa(attempt)
	}
	digest := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%x", digest), nil
}

func (s *Service) Run(ctx context.Context, deploymentID string) error {
	return s.run(ctx, deploymentID, "", nil)
}

func (s *Service) run(ctx context.Context, deploymentID, expectedImageID string, command *commandExecution) error {
	var record model.DeploymentRecord
	if err := s.db.WithContext(ctx).First(&record, "id = ?", deploymentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDeploymentNotFound
		}
		return fmt.Errorf("读取待执行发布记录失败: %w", err)
	}
	lock, err := s.acquireTargetLock(ctx, &record)
	if err != nil {
		internalErr := fmt.Errorf("获取发布环境并发锁失败: %w", err)
		s.logger.Error("获取发布环境并发锁失败", "operation", "deployment_lock", "deployment_id", deploymentID, "target_id", record.TargetID, "err", err)
		return s.markFailed(ctx, deploymentID, "deployment_lock_failed", "等待部署运行环境可用失败，请稍后重试", internalErr, "", nil)
	}
	if lock != nil {
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := lock.Release(releaseCtx); err != nil && !errors.Is(err, redislock.ErrLockNotHeld) {
				s.logger.Error("释放发布环境并发锁失败", "operation", "deployment_unlock", "deployment_id", deploymentID, "target_id", record.TargetID, "err", err)
			}
		}()
	}

	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&model.DeploymentRecord{}).
		Where("id = ? AND status = ?", deploymentID, model.DeploymentQueued).
		Updates(map[string]any{"status": model.DeploymentRunning, "started_at": now, "updated_at": now})
	if result.Error != nil {
		return fmt.Errorf("领取发布任务失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		var existing model.DeploymentRecord
		if err := s.db.WithContext(ctx).First(&existing, "id = ?", deploymentID).Error; err != nil {
			return ErrDeploymentNotFound
		}
		if existing.Status == model.DeploymentSucceeded {
			return nil
		}
		return ErrInvalidDeploymentState
	}

	if err := s.db.WithContext(ctx).First(&record, "id = ?", deploymentID).Error; err != nil {
		return s.markFailed(ctx, deploymentID, "deployment_record_missing", "发布记录读取失败", err, "", nil)
	}
	expectedImageID = strings.TrimSpace(expectedImageID)
	if record.ExpectedImageID != "" {
		if expectedImageID != "" && expectedImageID != record.ExpectedImageID {
			internalErr := errors.New("执行请求中的镜像身份与发布记录不一致")
			s.logger.Error("拒绝使用不一致的镜像身份执行发布", "operation", "deployment_image_identity",
				"deployment_id", deploymentID, "target_id", record.TargetID, "err", internalErr)
			return s.markFailed(ctx, deploymentID, "deployment_image_identity_mismatch", "待发布镜像身份校验失败", internalErr, "", nil)
		}
		expectedImageID = record.ExpectedImageID
	}
	if record.Operation == model.DeploymentRollback {
		validRollbackIdentity := (record.Platform == model.DeploymentDocker && dockerengine.IsValidImageID(expectedImageID)) ||
			(record.Platform == model.DeploymentKubernetes && expectedImageID == "" && immutableImageReference(record.Image))
		if !validRollbackIdentity {
			internalErr := errors.New("回滚记录缺少不可变镜像身份")
			s.logger.Error("拒绝执行无法验证镜像身份的回滚", "operation", "deployment_rollback_identity",
				"deployment_id", deploymentID, "target_id", record.TargetID, "err", internalErr)
			return s.markFailed(ctx, deploymentID, "rollback_image_identity_missing", "回滚镜像身份不可验证", internalErr, "", nil)
		}
	}
	timeout := time.Duration(record.RolloutTimeout) * time.Second
	var previousImage string
	var previousImageID string
	var warning error
	var commandExitCode *int
	switch record.Platform {
	case model.DeploymentSSH:
		commandTimeout, validTimeout := effectiveSSHTimeout(&record)
		if command == nil || s.ssh == nil || record.CommandScript == "" || !validTimeout {
			err = ErrInvalidTarget
			break
		}
		result, commandErr := s.ssh.RunHostDeploymentScript(ctx, sshdeploy.Input{
			HostID: record.HostID, EnvironmentID: record.EnvironmentID,
			WorkingDirectory: record.WorkingDirectory, Script: record.CommandScript,
			Timeout: commandTimeout, Environment: command.environment,
			Artifact: command.artifact, ArtifactName: command.artifactName, ArtifactDigest: command.artifactDigest,
			Stdout: command.stdout, Stderr: command.stderr,
		})
		if result.ExitCode >= 0 {
			commandExitCode = &result.ExitCode
		}
		err = commandErr
	case model.DeploymentDocker:
		if record.DeploymentPlanKind == model.DeploymentPlanCompose {
			composeTimeout, validTimeout := effectiveComposeTimeout(&record)
			expectedDigest := model.DeploymentPlanComposeExecutionDigest(record.ComposeYAML, record.ComposeService, record.ComposeTimeout)
			if !validTimeout || record.ComposeDigest == "" || record.ComposeDigest != expectedDigest || s.docker == nil {
				err = ErrInvalidTarget
				break
			}
			previousImage, err = s.docker.DeployCompose(ctx, dockerengine.ComposeDeployInput{
				EndpointID: record.RuntimeID, TargetID: record.TargetID,
				ServiceName: record.ComposeService, YAML: record.ComposeYAML,
				Image: record.Image, ExpectedImageID: expectedImageID,
				DeploymentID: record.ID, Timeout: composeTimeout,
				RegistryAuth: executionRegistryAuth(command),
				Stdout:       executionOutput(command, true), Stderr: executionOutput(command, false),
			})
		} else {
			dockerConfig, configErr := dockerengine.NormalizeContainerConfig(record.DockerConfig)
			if record.DeploymentPlanKind == model.DeploymentPlanDocker &&
				(configErr != nil || record.DockerConfigDigest == "" || record.DockerConfigDigest != model.DockerContainerConfigDigest(dockerConfig)) {
				err = ErrInvalidTarget
				break
			}
			if configErr != nil {
				err = ErrInvalidTarget
				break
			}
			if expectedImageID == "" {
				previous, deployWarning, deployErr := s.docker.DeployContainer(
					ctx, record.RuntimeID, record.TargetID, record.WorkloadName, record.Image, record.ImageDisplay,
					record.ID, timeout, dockerConfig,
					executionRegistryAuth(command), executionOutput(command, true), executionOutput(command, false),
				)
				previousImage, previousImageID, warning, err = previous.Reference, previous.ID, deployWarning, deployErr
			} else {
				previous, deployWarning, deployErr := s.docker.DeployPreparedContainer(
					ctx, record.RuntimeID, record.TargetID, record.WorkloadName, record.Image, record.ImageDisplay,
					expectedImageID, record.ID, timeout, dockerConfig,
					executionRegistryAuth(command), executionOutput(command, true), executionOutput(command, false),
				)
				previousImage, previousImageID, warning, err = previous.Reference, previous.ID, deployWarning, deployErr
			}
		}
	case model.DeploymentKubernetes:
		previousImage, err = s.kube.DeployImage(
			ctx, record.RuntimeID, record.Namespace, record.WorkloadName,
			record.ContainerName, record.Image, record.ID, timeout,
		)
	default:
		err = ErrInvalidTarget
	}
	if err != nil {
		message := "发布执行失败，需人工确认目标状态"
		code := "deployment_execution_failed"
		if record.Platform == model.DeploymentSSH {
			code, message = "ssh_command_failed", "命令脚本部署失败，请查看流水线日志"
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				code, message = "ssh_command_timeout", "命令脚本部署超时，请查看流水线日志"
			}
			s.logger.Error("命令脚本部署失败", "operation", "command_deployment_execute", "deployment_id", deploymentID,
				"target_id", record.TargetID, "host_id", record.HostID, "exit_code", commandExitCode, "err", err)
		} else if record.DeploymentPlanKind == model.DeploymentPlanCompose {
			code, message = dockerComposeFailureDetails(err)
			s.logger.Error("Docker Compose 部署失败", "operation", "compose_deployment_execute", "deployment_id", deploymentID,
				"target_id", record.TargetID, "runtime_id", record.RuntimeID, "service", record.ComposeService, "err", err)
		} else if record.Platform == model.DeploymentDocker {
			code, message = dockerContainerFailureDetails(err)
			s.logger.Error("Docker 容器部署失败", "operation", "docker_container_deployment_execute",
				"deployment_id", deploymentID, "target_id", record.TargetID, "runtime_id", record.RuntimeID,
				"container_name", record.WorkloadName, "err", err)
		}
		return s.markFailed(ctx, deploymentID, code, message, err, previousImage, commandExitCode)
	}
	warningMessage := ""
	if warning != nil {
		warningMessage = "发布已完成，但旧资源自动清理失败，请人工检查"
		s.logger.Warn("发布成功后清理旧资源失败", "operation", "deployment_post_cleanup", "deployment_id", deploymentID, "target_id", record.TargetID, "err", warning)
	}
	if previousImageID != "" && !dockerengine.IsValidImageID(previousImageID) {
		s.logger.Warn("旧 Docker 镜像身份无效，后续将不允许自动回滚", "operation", "deployment_previous_image_identity",
			"deployment_id", deploymentID, "target_id", record.TargetID)
		previousImageID = ""
	}
	finishedAt := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&model.DeploymentRecord{}).Where("id = ?", deploymentID).
		Updates(map[string]any{
			"status": model.DeploymentSucceeded, "previous_image": previousImage, "previous_image_id": previousImageID,
			"finished_at": finishedAt, "updated_at": finishedAt,
			"error_code": "", "error_message": "", "warning_message": warningMessage,
			"command_exit_code": commandExitCode,
		}).Error; err != nil {
		return fmt.Errorf("记录发布成功状态失败: %w", err)
	}
	return nil
}

func dockerContainerFailureDetails(err error) (string, string) {
	switch {
	case errors.Is(err, dockerengine.ErrContainerRollbackFailed):
		return "docker_container_rollback_failed", "Docker 容器发布失败：新容器未就绪且旧容器恢复失败，请立即检查目标容器"
	case errors.Is(err, dockerengine.ErrContainerStopTimeout):
		return "docker_previous_container_stop_timeout", "Docker 容器发布失败：停止旧容器超时，未继续替换容器"
	case errors.Is(err, dockerengine.ErrContainerStopFailed):
		return "docker_previous_container_stop_failed", "Docker 容器发布失败：无法停止旧容器，未继续替换容器"
	case errors.Is(err, dockerengine.ErrContainerRestarted):
		return "docker_container_restarted", "Docker 容器启动失败：容器启动后退出并进入重启，请查看容器日志"
	case errors.Is(err, dockerengine.ErrContainerNotRunning):
		return "docker_container_not_running", "Docker 容器启动失败：容器未保持运行，请查看容器日志"
	case errors.Is(err, dockerengine.ErrContainerUnhealthy):
		return "docker_container_unhealthy", "Docker 容器启动失败：健康检查未通过，请查看容器日志"
	case errors.Is(err, dockerengine.ErrContainerReadinessTimeout):
		return "docker_container_readiness_timeout", "Docker 容器启动超时：未在规定时间内就绪，请查看容器日志"
	case errors.Is(err, ErrInvalidTarget):
		return "docker_deployment_config_invalid", "Docker 部署配置无效，请检查部署方案"
	default:
		return "docker_deployment_failed", "Docker 容器部署失败，请查看流水线日志和容器日志"
	}
}

func dockerComposeFailureDetails(err error) (string, string) {
	switch {
	case errors.Is(err, dockerengine.ErrComposeRollbackFailed):
		return "compose_rollback_failed", "Docker Compose 发布失败：服务未就绪且旧服务恢复失败，请立即检查目标容器"
	case errors.Is(err, dockerengine.ErrContainerRestarted):
		return "compose_container_restarted", "Docker Compose 服务启动失败：容器启动后退出并进入重启，请查看容器日志"
	case errors.Is(err, dockerengine.ErrContainerNotRunning):
		return "compose_container_not_running", "Docker Compose 服务启动失败：容器未保持运行，请查看容器日志"
	case errors.Is(err, dockerengine.ErrContainerUnhealthy):
		return "compose_container_unhealthy", "Docker Compose 服务启动失败：健康检查未通过，请查看容器日志"
	case errors.Is(err, dockerengine.ErrContainerReadinessTimeout), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "compose_container_readiness_timeout", "Docker Compose 服务启动超时：未在规定时间内就绪，请查看容器日志"
	case errors.Is(err, ErrInvalidTarget), errors.Is(err, dockerengine.ErrInvalidComposeYAML):
		return "compose_deployment_config_invalid", "Docker Compose 部署配置无效，请检查部署方案"
	default:
		return "compose_deployment_failed", "Docker Compose 部署失败，请查看流水线日志和容器日志"
	}
}

func (s *Service) acquireTargetLock(ctx context.Context, record *model.DeploymentRecord) (*redislock.Lock, error) {
	if s.locks == nil {
		return nil, nil
	}
	if record == nil || record.TargetID == "" || s.lockKeyPrefix == "" {
		return nil, ErrInvalidTarget
	}
	rolloutTimeout := time.Duration(record.RolloutTimeout) * time.Second
	if record.Platform == model.DeploymentSSH {
		var valid bool
		rolloutTimeout, valid = effectiveSSHTimeout(record)
		if !valid {
			return nil, ErrInvalidTarget
		}
	} else if record.DeploymentPlanKind == model.DeploymentPlanCompose {
		var valid bool
		rolloutTimeout, valid = effectiveComposeTimeout(record)
		if !valid {
			return nil, ErrInvalidTarget
		}
	}
	if rolloutTimeout < 30*time.Second {
		rolloutTimeout = 30 * time.Second
	}
	return s.locks.Obtain(
		ctx,
		s.lockKeyPrefix+":"+record.TargetID,
		rolloutTimeout+2*time.Minute,
		&redislock.Options{
			RetryStrategy: redislock.ExponentialBackoff(50*time.Millisecond, time.Second),
			Metadata:      record.ID,
		},
	)
}

func effectiveSSHTimeout(record *model.DeploymentRecord) (time.Duration, bool) {
	if record == nil || record.CommandTimeout < 30 || record.CommandTimeout > 3600 ||
		record.RolloutTimeout < 30 || record.RolloutTimeout > 3600 {
		return 0, false
	}
	seconds := record.CommandTimeout
	if record.RolloutTimeout < seconds {
		seconds = record.RolloutTimeout
	}
	return time.Duration(seconds) * time.Second, true
}

func effectiveComposeTimeout(record *model.DeploymentRecord) (time.Duration, bool) {
	if record == nil || record.ComposeTimeout < 30 || record.ComposeTimeout > 3600 ||
		record.RolloutTimeout < 30 || record.RolloutTimeout > 3600 {
		return 0, false
	}
	seconds := record.ComposeTimeout
	if record.RolloutTimeout < seconds {
		seconds = record.RolloutTimeout
	}
	return time.Duration(seconds) * time.Second, true
}

func executionOutput(execution *commandExecution, stdout bool) io.Writer {
	if execution == nil {
		return io.Discard
	}
	if stdout {
		if execution.stdout != nil {
			return execution.stdout
		}
	} else if execution.stderr != nil {
		return execution.stderr
	}
	return io.Discard
}

func executionRegistryAuth(execution *commandExecution) dockerengine.RegistryAuth {
	if execution == nil {
		return dockerengine.RegistryAuth{}
	}
	return execution.registryAuth
}

func (s *Service) enqueue(ctx context.Context, tx *gorm.DB, record *model.DeploymentRecord, kind string) error {
	job, err := task.NewService(tx, 1).Create(ctx, task.CreateInput{
		Kind: kind, Subject: "edo.task." + kind, Payload: TaskPayload{DeploymentID: record.ID},
		IdempotencyKey: "deployment:" + record.ID,
	})
	if err != nil {
		return err
	}
	record.JobID = job.ID
	return tx.Model(record).Update("job_id", job.ID).Error
}

func (s *Service) findTarget(ctx context.Context, id string) (*model.DeploymentTarget, error) {
	var target model.DeploymentTarget
	if err := s.db.WithContext(ctx).First(&target, "id = ? AND is_active = ?", id, true).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTargetNotFound
		}
		return nil, fmt.Errorf("查询发布目标失败: %w", err)
	}
	return &target, nil
}

func (s *Service) normalizeTarget(ctx context.Context, input TargetInput) (TargetInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.EnvironmentID = strings.TrimSpace(input.EnvironmentID)
	input.HostID = strings.TrimSpace(input.HostID)
	input.RuntimeID = strings.TrimSpace(input.RuntimeID)
	input.WorkingDirectory = strings.TrimSpace(input.WorkingDirectory)
	input.Namespace = strings.TrimSpace(input.Namespace)
	input.WorkloadName = strings.TrimSpace(input.WorkloadName)
	input.ContainerName = strings.TrimSpace(input.ContainerName)
	if input.RolloutTimeout == 0 {
		input.RolloutTimeout = 300
	}
	if !targetNamePattern.MatchString(input.Name) || len([]rune(input.Description)) > 500 ||
		input.RolloutTimeout < 30 || input.RolloutTimeout > 3600 {
		return TargetInput{}, ErrInvalidTarget
	}
	if input.EnvironmentID == "" {
		return TargetInput{}, ErrInvalidTarget
	}
	resolvedHost, resolvedCapability, err := s.resolveEnvironmentTarget(ctx, input.EnvironmentID, input.Platform, input.RuntimeID)
	if err != nil {
		return TargetInput{}, err
	}
	input.HostID = resolvedHost.ID
	switch input.Platform {
	case model.DeploymentSSH:
		if input.WorkingDirectory != "" && !validWorkingDirectory(input.WorkingDirectory) {
			return TargetInput{}, ErrInvalidTarget
		}
		input.RuntimeID, input.Namespace, input.WorkloadName, input.ContainerName = "", "", "", ""
	case model.DeploymentDocker:
		input.RuntimeID = resolvedCapability.RuntimeID
		if input.WorkloadName != "" && !workloadNamePattern.MatchString(input.WorkloadName) {
			return TargetInput{}, ErrInvalidTarget
		}
		input.WorkingDirectory = ""
		input.Namespace = ""
		input.ContainerName = ""
		endpoint, err := s.docker.Find(ctx, input.RuntimeID)
		if err != nil || !endpoint.IsActive || endpoint.HostID != input.HostID {
			return TargetInput{}, ErrInvalidTarget
		}
	case model.DeploymentKubernetes:
		if input.RuntimeID == "" || len(validation.IsDNS1123Label(input.Namespace)) > 0 ||
			len(validation.IsDNS1123Subdomain(input.WorkloadName)) > 0 ||
			len(validation.IsDNS1123Label(input.ContainerName)) > 0 {
			return TargetInput{}, ErrInvalidTarget
		}
		cluster, err := s.kube.Find(ctx, input.RuntimeID)
		if err != nil || !cluster.IsActive {
			return TargetInput{}, ErrInvalidTarget
		}
		input.WorkingDirectory = ""
	default:
		return TargetInput{}, ErrInvalidTarget
	}
	return input, nil
}

// resolveEnvironmentTarget 根据环境归属和已就绪能力解析唯一执行主机。
// 部署方案不接受客户端指定主机，避免环境与主机重复配置或被请求参数绕过。
func (s *Service) resolveEnvironmentTarget(
	ctx context.Context,
	environmentID string,
	platform model.DeploymentPlatform,
	runtimeID string,
) (model.Host, model.HostCapability, error) {
	var environment model.Environment
	if err := s.db.WithContext(ctx).Select("id").First(&environment, "id = ? AND is_active = ?", environmentID, true).Error; err != nil {
		s.logger.Warn("解析环境执行主机失败", "operation", "deployment_target_resolve", "environment_id", environmentID, "platform", platform, "err", err)
		return model.Host{}, model.HostCapability{}, ErrInvalidTarget
	}
	var memberships []model.EnvironmentHost
	if err := s.db.WithContext(ctx).Where("environment_id = ?", environmentID).Find(&memberships).Error; err != nil {
		s.logger.Error("查询环境主机关系失败", "operation", "deployment_target_resolve", "environment_id", environmentID, "platform", platform, "err", err)
		return model.Host{}, model.HostCapability{}, fmt.Errorf("查询环境执行主机失败: %w", err)
	}
	hostIDs := make([]string, 0, len(memberships))
	for i := range memberships {
		hostIDs = append(hostIDs, memberships[i].HostID)
	}
	if len(hostIDs) == 0 {
		s.logger.Warn("环境没有关联主机", "operation", "deployment_target_resolve", "environment_id", environmentID, "platform", platform)
		return model.Host{}, model.HostCapability{}, ErrEnvironmentTargetUnavailable
	}
	var hosts []model.Host
	if err := s.db.WithContext(ctx).Where("id IN ? AND is_active = ?", hostIDs, true).Order("id ASC").Find(&hosts).Error; err != nil {
		s.logger.Error("查询环境可用主机失败", "operation", "deployment_target_resolve", "environment_id", environmentID, "platform", platform, "err", err)
		return model.Host{}, model.HostCapability{}, fmt.Errorf("查询环境执行主机失败: %w", err)
	}
	var capabilities []model.HostCapability
	if err := s.db.WithContext(ctx).Where("host_id IN ? AND status = ?", hostIDs, model.HostCapabilityReady).Find(&capabilities).Error; err != nil {
		s.logger.Error("查询环境主机能力失败", "operation", "deployment_target_resolve", "environment_id", environmentID, "platform", platform, "err", err)
		return model.Host{}, model.HostCapability{}, fmt.Errorf("查询环境执行能力失败: %w", err)
	}
	capabilitiesByHost := make(map[string][]model.HostCapability, len(hosts))
	for i := range capabilities {
		capabilitiesByHost[capabilities[i].HostID] = append(capabilitiesByHost[capabilities[i].HostID], capabilities[i])
	}
	type candidate struct {
		host       model.Host
		capability model.HostCapability
	}
	candidates := make([]candidate, 0, len(hosts))
	for i := range hosts {
		expectedKind := model.HostCapabilityKind("")
		switch platform {
		case model.DeploymentSSH:
			switch hosts[i].Mode {
			case model.HostModeLocal:
				if hosts[i].IsBuiltin && hosts[i].ID == model.BuiltinLocalHostID {
					expectedKind = model.HostCapabilityLocalExec
				}
			case model.HostModeSSH:
				if !hosts[i].IsBuiltin {
					expectedKind = model.HostCapabilitySSH
				}
			}
		case model.DeploymentDocker:
			expectedKind = model.HostCapabilityDocker
		case model.DeploymentKubernetes:
			expectedKind = model.HostCapabilityKubernetes
		default:
			return model.Host{}, model.HostCapability{}, ErrInvalidTarget
		}
		for _, capability := range capabilitiesByHost[hosts[i].ID] {
			if capability.Kind != expectedKind {
				continue
			}
			if platform != model.DeploymentSSH && capability.RuntimeID == "" {
				continue
			}
			if platform == model.DeploymentKubernetes && capability.RuntimeID != strings.TrimSpace(runtimeID) {
				continue
			}
			if platform == model.DeploymentDocker {
				if s.docker == nil {
					continue
				}
				endpoint, findErr := s.docker.Find(ctx, capability.RuntimeID)
				if findErr != nil || !endpoint.IsActive || endpoint.HostID != hosts[i].ID {
					continue
				}
			}
			if platform == model.DeploymentKubernetes {
				if s.kube == nil {
					continue
				}
				cluster, findErr := s.kube.Find(ctx, capability.RuntimeID)
				if findErr != nil || !cluster.IsActive {
					continue
				}
			}
			candidates = append(candidates, candidate{host: hosts[i], capability: capability})
			break
		}
	}
	if len(candidates) == 0 {
		s.logger.Warn("环境没有匹配部署方式的可用主机", "operation", "deployment_target_resolve", "environment_id", environmentID, "platform", platform, "runtime_id", runtimeID)
		return model.Host{}, model.HostCapability{}, ErrEnvironmentTargetUnavailable
	}
	if len(candidates) > 1 {
		s.logger.Warn("环境存在多个匹配部署方式的可用主机", "operation", "deployment_target_resolve", "environment_id", environmentID, "platform", platform, "runtime_id", runtimeID, "candidate_count", len(candidates))
		return model.Host{}, model.HostCapability{}, ErrEnvironmentTargetAmbiguous
	}
	return candidates[0].host, candidates[0].capability, nil
}

// ResolveDockerWorkloadName 固定 Docker 容器部署在本次运行中使用的容器名称。
// 用户未配置名称时，名称稳定绑定应用、部署方案和内部目标，流水线快照与发布记录
// 必须共同使用这个结果，避免执行阶段生成名称后破坏不可变快照的一致性。
func ResolveDockerWorkloadName(configuredName, applicationName, applicationID, deploymentPlanID, targetID string) (string, error) {
	configuredName = strings.TrimSpace(configuredName)
	if configuredName != "" {
		if !workloadNamePattern.MatchString(configuredName) {
			return "", ErrInvalidTarget
		}
		return configuredName, nil
	}
	applicationName = strings.TrimSpace(applicationName)
	applicationID = strings.TrimSpace(applicationID)
	deploymentPlanID = strings.TrimSpace(deploymentPlanID)
	targetID = strings.TrimSpace(targetID)
	if applicationName == "" || applicationID == "" || deploymentPlanID == "" || targetID == "" ||
		!workloadNamePattern.MatchString(applicationName) {
		return "", ErrInvalidTarget
	}
	digest := sha256.Sum256([]byte(applicationID + "\x00" + deploymentPlanID + "\x00" + targetID))
	name := fmt.Sprintf("%s-%x", applicationName, digest[:4])
	if !workloadNamePattern.MatchString(name) {
		return "", ErrInvalidTarget
	}
	return name, nil
}

func validWorkingDirectory(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 1024 && strings.HasPrefix(value, "/") &&
		!strings.ContainsAny(value, "\r\n\x00") && path.Clean(value) == value
}

func (s *Service) markFailed(ctx context.Context, id, code, message string, cause error, previousImage string, commandExitCode *int) error {
	now := time.Now().UTC()
	updateContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.db.WithContext(updateContext).Model(&model.DeploymentRecord{}).Where("id = ?", id).
		Updates(map[string]any{
			"status": model.DeploymentFailed, "previous_image": previousImage,
			"error_code": code, "error_message": message,
			"command_exit_code": commandExitCode,
			"finished_at":       now, "updated_at": now,
		}).Error; err != nil {
		return fmt.Errorf("记录发布失败状态时发生错误: %v；原始错误: %w", err, cause)
	}
	return cause
}

func applyTargetSnapshot(record *model.DeploymentRecord, target *model.DeploymentTarget) {
	record.TargetName = target.Name
	record.Platform = target.Platform
	record.EnvironmentID = target.EnvironmentID
	record.HostID = target.HostID
	record.RuntimeID = target.RuntimeID
	record.WorkingDirectory = target.WorkingDirectory
	record.Namespace = target.Namespace
	record.WorkloadName = target.WorkloadName
	record.ContainerName = target.ContainerName
	record.RolloutTimeout = target.RolloutTimeout
}

func copyTargetSnapshot(destination, source *model.DeploymentRecord) {
	destination.TargetName = source.TargetName
	destination.Platform = source.Platform
	destination.EnvironmentID = source.EnvironmentID
	destination.HostID = source.HostID
	destination.RuntimeID = source.RuntimeID
	destination.WorkingDirectory = source.WorkingDirectory
	destination.Namespace = source.Namespace
	destination.WorkloadName = source.WorkloadName
	destination.ContainerName = source.ContainerName
	destination.RolloutTimeout = source.RolloutTimeout
}

func validateImage(value string, immutable bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 1024 {
		return "", ErrInvalidImage
	}
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return "", ErrInvalidImage
	}
	if immutable {
		if _, ok := named.(reference.Digested); !ok {
			return "", ErrImmutableImageRequired
		}
	}
	return reference.FamiliarString(named), nil
}

func immutableImageReference(value string) bool {
	_, err := validateImage(value, true)
	return err == nil
}

func validatePipelineImage(value, expectedImageID string, target *model.DeploymentTarget) (string, error) {
	expectedImageID = strings.TrimSpace(expectedImageID)
	if expectedImageID == "" {
		return validateImage(value, true)
	}
	if target.Platform != model.DeploymentDocker || !dockerengine.IsEDOLocalImage(value) || !dockerengine.IsValidImageID(expectedImageID) {
		return "", ErrInvalidImage
	}
	return validateImage(value, false)
}
