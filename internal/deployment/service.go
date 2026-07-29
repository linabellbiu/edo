package deployment

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/bsm/redislock"
	"github.com/distribution/reference"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/util/validation"

	"zrt/internal/dockerengine"
	"zrt/internal/kube"
	"zrt/internal/model"
	"zrt/internal/sshdeploy"
	"zrt/internal/task"
)

var (
	ErrInvalidTarget           = errors.New("部署配置无效")
	ErrTargetExists            = errors.New("部署配置名称已存在")
	ErrTargetNotFound          = errors.New("部署配置不存在")
	ErrInvalidImage            = errors.New("容器镜像引用无效")
	ErrImmutableImageRequired  = errors.New("镜像仓库或 Kubernetes 发布必须使用带摘要的不可变镜像")
	ErrDeploymentNotFound      = errors.New("发布记录不存在")
	ErrInvalidDeploymentState  = errors.New("发布记录当前状态不允许此操作")
	ErrRollbackUnavailable     = errors.New("该发布记录没有可回滚的上一镜像")
	ErrCommandPipelineRequired = errors.New("命令脚本发布必须从流水线部署节点发起")
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
	TargetID        string
	Image           string
	ExpectedImageID string
	PipelineRunID   string
	WorkflowNodeID  string
	ApprovedBy      string
}

type CommandRequestInput struct {
	TargetID         string
	PipelineRunID    string
	WorkflowNodeID   string
	ApprovedBy       string
	DeploymentPlanID string
	PlanKind         model.DeploymentPlanKind
	Script           string
	ScriptDigest     string
	TimeoutSeconds   int
	Environment      map[string]string
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
		Platform: input.Platform, Environment: "", // 旧数据库列仍有 NOT NULL 约束，但不再承载业务语义。
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
	if limit < 1 || limit > 200 {
		limit = 50
	}
	var records []model.DeploymentRecord
	if err := s.db.WithContext(ctx).Order("created_at DESC").Limit(limit).Find(&records).Error; err != nil {
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
		TargetID: target.ID, Operation: model.DeploymentRelease,
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
	if target.Platform == model.DeploymentSSH {
		return nil, ErrCommandPipelineRequired
	}
	image, err := validatePipelineImage(input.Image, input.ExpectedImageID, target)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	record := &model.DeploymentRecord{
		ID: uuid.NewString(), PipelineRunID: input.PipelineRunID, WorkflowNodeID: input.WorkflowNodeID,
		TargetID: target.ID, Operation: model.DeploymentRelease, Image: image,
		Status: model.DeploymentQueued, RequestedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if input.ApprovedBy != "" {
		record.ApprovedBy, record.ApprovedAt = &input.ApprovedBy, &now
	}
	applyTargetSnapshot(record, target)
	if err := s.db.WithContext(ctx).Create(record).Error; err != nil {
		return nil, fmt.Errorf("创建流水线发布记录失败: %w", err)
	}
	if err := s.run(ctx, record.ID, input.ExpectedImageID, nil); err != nil {
		return record, err
	}
	if err := s.db.WithContext(ctx).First(record, "id = ?", record.ID).Error; err != nil {
		return nil, fmt.Errorf("读取流水线发布结果失败: %w", err)
	}
	return record, nil
}

type commandExecution struct {
	environment map[string]string
	stdout      io.Writer
	stderr      io.Writer
}

func (s *Service) RequestCommandAndRun(ctx context.Context, actorID string, input CommandRequestInput) (*model.DeploymentRecord, error) {
	target, err := s.findTarget(ctx, input.TargetID)
	if err != nil {
		return nil, err
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
	now := time.Now().UTC()
	record := &model.DeploymentRecord{
		ID: uuid.NewString(), PipelineRunID: input.PipelineRunID, WorkflowNodeID: input.WorkflowNodeID,
		TargetID: target.ID, Operation: model.DeploymentRelease, Status: model.DeploymentQueued,
		RequestedBy: actorID, CreatedAt: now, UpdatedAt: now,
		DeploymentPlanID: input.DeploymentPlanID, DeploymentPlanKind: input.PlanKind,
		CommandScript: input.Script, CommandDigest: input.ScriptDigest, CommandTimeout: input.TimeoutSeconds,
	}
	if input.ApprovedBy != "" {
		record.ApprovedBy, record.ApprovedAt = &input.ApprovedBy, &now
	}
	applyTargetSnapshot(record, target)
	if err := s.db.WithContext(ctx).Create(record).Error; err != nil {
		return nil, fmt.Errorf("创建命令脚本流水线发布记录失败: %w", err)
	}
	environment := make(map[string]string, len(input.Environment)+1)
	for key, value := range input.Environment {
		environment[key] = value
	}
	environment["ZRT_DEPLOYMENT_ID"] = record.ID
	if err := s.run(ctx, record.ID, "", &commandExecution{
		environment: environment, stdout: input.Stdout, stderr: input.Stderr,
	}); err != nil {
		return record, err
	}
	if err := s.db.WithContext(ctx).First(record, "id = ?", record.ID).Error; err != nil {
		return nil, fmt.Errorf("读取命令脚本流水线发布结果失败: %w", err)
	}
	return record, nil
}

func (s *Service) Rollback(ctx context.Context, sourceID, actorID string) (*model.DeploymentRecord, error) {
	var source model.DeploymentRecord
	if err := s.db.WithContext(ctx).First(&source, "id = ?", sourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDeploymentNotFound
		}
		return nil, fmt.Errorf("查询待回滚发布记录失败: %w", err)
	}
	if source.Platform == model.DeploymentSSH || source.Status != model.DeploymentSucceeded || source.PreviousImage == "" {
		return nil, ErrRollbackUnavailable
	}
	now := time.Now().UTC()
	record := &model.DeploymentRecord{
		ID: uuid.NewString(), TargetID: source.TargetID, Operation: model.DeploymentRollback,
		Image: source.PreviousImage, Status: model.DeploymentQueued,
		RequestedBy: actorID, ApprovedBy: &actorID, ApprovedAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}
	copyTargetSnapshot(record, &source)
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(record).Error; err != nil {
			return err
		}
		return s.enqueue(ctx, tx, record, "rollback.runtime")
	}); err != nil {
		return nil, fmt.Errorf("创建回滚任务失败: %w", err)
	}
	return record, nil
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
	timeout := time.Duration(record.RolloutTimeout) * time.Second
	var previousImage string
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
			Timeout:     commandTimeout,
			Environment: command.environment, Stdout: command.stdout, Stderr: command.stderr,
		})
		if result.ExitCode >= 0 {
			commandExitCode = &result.ExitCode
		}
		err = commandErr
	case model.DeploymentDocker:
		if expectedImageID == "" {
			previousImage, warning, err = s.docker.DeployContainer(
				ctx, record.RuntimeID, record.WorkloadName, record.Image, record.ID, timeout,
			)
		} else {
			previousImage, warning, err = s.docker.DeployPreparedContainer(
				ctx, record.RuntimeID, record.WorkloadName, record.Image, expectedImageID, record.ID, timeout,
			)
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
		}
		return s.markFailed(ctx, deploymentID, code, message, err, previousImage, commandExitCode)
	}
	warningMessage := ""
	if warning != nil {
		warningMessage = "发布已完成，但旧资源自动清理失败，请人工检查"
		s.logger.Warn("发布成功后清理旧资源失败", "operation", "deployment_post_cleanup", "deployment_id", deploymentID, "target_id", record.TargetID, "err", warning)
	}
	finishedAt := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&model.DeploymentRecord{}).Where("id = ?", deploymentID).
		Updates(map[string]any{
			"status": model.DeploymentSucceeded, "previous_image": previousImage,
			"finished_at": finishedAt, "updated_at": finishedAt,
			"error_code": "", "error_message": "", "warning_message": warningMessage,
			"command_exit_code": commandExitCode,
		}).Error; err != nil {
		return fmt.Errorf("记录发布成功状态失败: %w", err)
	}
	return nil
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

func (s *Service) enqueue(ctx context.Context, tx *gorm.DB, record *model.DeploymentRecord, kind string) error {
	job, err := task.NewService(tx, 1).Create(ctx, task.CreateInput{
		Kind: kind, Subject: "zrt.task." + kind, Payload: TaskPayload{DeploymentID: record.ID},
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
	switch input.Platform {
	case model.DeploymentSSH:
		if input.HostID == "" {
			return TargetInput{}, ErrInvalidTarget
		}
		if input.WorkingDirectory != "" && !validWorkingDirectory(input.WorkingDirectory) {
			return TargetInput{}, ErrInvalidTarget
		}
		var host model.Host
		if err := s.db.WithContext(ctx).First(&host,
			"id = ? AND is_active = ?", input.HostID, true,
		).Error; err != nil || host.EnvironmentID == "" {
			return TargetInput{}, ErrInvalidTarget
		}
		capabilityKind := model.HostCapabilitySSH
		switch host.Mode {
		case model.HostModeLocal:
			if !host.IsBuiltin || host.ID != model.BuiltinLocalHostID {
				return TargetInput{}, ErrInvalidTarget
			}
			capabilityKind = model.HostCapabilityLocalExec
		case model.HostModeSSH:
			if host.IsBuiltin {
				return TargetInput{}, ErrInvalidTarget
			}
		default:
			return TargetInput{}, ErrInvalidTarget
		}
		var capability model.HostCapability
		if err := s.db.WithContext(ctx).First(&capability,
			"host_id = ? AND kind = ? AND status = ?", host.ID, capabilityKind, model.HostCapabilityReady,
		).Error; err != nil {
			return TargetInput{}, ErrInvalidTarget
		}
		var environment model.Environment
		if err := s.db.WithContext(ctx).Select("id").First(&environment, "id = ? AND is_active = ?", host.EnvironmentID, true).Error; err != nil {
			return TargetInput{}, ErrInvalidTarget
		}
		input.EnvironmentID = environment.ID
		input.RuntimeID, input.Namespace, input.WorkloadName, input.ContainerName = "", "", "", ""
	case model.DeploymentDocker:
		if input.RuntimeID == "" || !workloadNamePattern.MatchString(input.WorkloadName) {
			return TargetInput{}, ErrInvalidTarget
		}
		input.EnvironmentID, input.HostID, input.WorkingDirectory = "", "", ""
		input.Namespace = ""
		input.ContainerName = ""
		endpoint, err := s.docker.Find(ctx, input.RuntimeID)
		if err != nil || !endpoint.IsActive {
			return TargetInput{}, ErrInvalidTarget
		}
		input.HostID = endpoint.HostID
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
		input.EnvironmentID, input.HostID, input.WorkingDirectory = "", "", ""
	default:
		return TargetInput{}, ErrInvalidTarget
	}
	return input, nil
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

func validatePipelineImage(value, expectedImageID string, target *model.DeploymentTarget) (string, error) {
	expectedImageID = strings.TrimSpace(expectedImageID)
	if expectedImageID == "" {
		return validateImage(value, true)
	}
	if target.Platform != model.DeploymentDocker || !dockerengine.IsZRTLocalImage(value) || !dockerengine.IsValidImageID(expectedImageID) {
		return "", ErrInvalidImage
	}
	return validateImage(value, false)
}
