package deployment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/util/validation"

	"zrt/internal/dockerengine"
	"zrt/internal/kube"
	"zrt/internal/model"
	"zrt/internal/task"
)

var (
	ErrInvalidTarget          = errors.New("发布目标配置无效")
	ErrTargetExists           = errors.New("发布目标名称已存在")
	ErrTargetNotFound         = errors.New("发布目标不存在")
	ErrInvalidImage           = errors.New("容器镜像引用无效")
	ErrImmutableImageRequired = errors.New("生产环境必须使用带摘要的不可变镜像")
	ErrDeploymentNotFound     = errors.New("发布记录不存在")
	ErrApprovalRequired       = errors.New("生产发布需要审批")
	ErrSelfApproval           = errors.New("发布申请人不能审批自己的生产发布")
	ErrInvalidDeploymentState = errors.New("发布记录当前状态不允许此操作")
	ErrRollbackUnavailable    = errors.New("该发布记录没有可回滚的上一镜像")
)

var targetNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]{1,127}$`)
var workloadNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,252}$`)

type TargetInput struct {
	Name           string
	Platform       model.DeploymentPlatform
	Environment    model.EnvironmentType
	RuntimeID      string
	Namespace      string
	WorkloadName   string
	ContainerName  string
	RolloutTimeout int
}

type RequestInput struct {
	TargetID string
	Image    string
}

type TaskPayload struct {
	DeploymentID string `json:"deployment_id"`
}

type Service struct {
	db     *gorm.DB
	docker *dockerengine.Service
	kube   *kube.Service
	logger *slog.Logger
}

func NewService(db *gorm.DB, docker *dockerengine.Service, kubeService *kube.Service, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{db: db, docker: docker, kube: kubeService, logger: logger}
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
		ID: uuid.NewString(), Name: input.Name, Platform: input.Platform, Environment: input.Environment,
		RuntimeID: input.RuntimeID, Namespace: input.Namespace, WorkloadName: input.WorkloadName,
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
		"name": input.Name, "platform": input.Platform, "environment": input.Environment,
		"runtime_id": input.RuntimeID, "namespace": input.Namespace,
		"workload_name": input.WorkloadName, "container_name": input.ContainerName,
		"rollout_timeout": input.RolloutTimeout, "updated_at": now,
	}).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrTargetExists
		}
		return nil, fmt.Errorf("更新发布目标失败: %w", err)
	}
	existing.Name, existing.Platform, existing.Environment = input.Name, input.Platform, input.Environment
	existing.RuntimeID, existing.Namespace = input.RuntimeID, input.Namespace
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
	image, err := validateImage(input.Image, target.Environment == model.EnvironmentProduction)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	status := model.DeploymentQueued
	if target.Environment == model.EnvironmentProduction {
		status = model.DeploymentAwaitingApproval
	}
	record := &model.DeploymentRecord{
		ID: uuid.NewString(), TargetID: target.ID, Operation: model.DeploymentRelease,
		Image: image, Status: status, RequestedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	applyTargetSnapshot(record, target)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(record).Error; err != nil {
			return err
		}
		if status == model.DeploymentQueued {
			return s.enqueue(ctx, tx, record, "deploy.runtime")
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("创建发布申请失败: %w", err)
	}
	return record, nil
}

func (s *Service) Approve(ctx context.Context, deploymentID, actorID string) (*model.DeploymentRecord, error) {
	var record model.DeploymentRecord
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&record, "id = ?", deploymentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDeploymentNotFound
			}
			return err
		}
		if record.Status != model.DeploymentAwaitingApproval {
			return ErrInvalidDeploymentState
		}
		if record.RequestedBy == actorID {
			return ErrSelfApproval
		}
		now := time.Now().UTC()
		result := tx.Model(&model.DeploymentRecord{}).
			Where("id = ? AND status = ?", record.ID, model.DeploymentAwaitingApproval).
			Updates(map[string]any{
				"status": model.DeploymentQueued, "approved_by": actorID,
				"approved_at": now, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvalidDeploymentState
		}
		record.Status, record.ApprovedBy, record.ApprovedAt, record.UpdatedAt = model.DeploymentQueued, &actorID, &now, now
		return s.enqueue(ctx, tx, &record, "deploy.runtime")
	})
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *Service) Rollback(ctx context.Context, sourceID, actorID string) (*model.DeploymentRecord, error) {
	var source model.DeploymentRecord
	if err := s.db.WithContext(ctx).First(&source, "id = ?", sourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDeploymentNotFound
		}
		return nil, fmt.Errorf("查询待回滚发布记录失败: %w", err)
	}
	if source.Status != model.DeploymentSucceeded || source.PreviousImage == "" {
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

	var record model.DeploymentRecord
	if err := s.db.WithContext(ctx).First(&record, "id = ?", deploymentID).Error; err != nil {
		return s.markFailed(ctx, deploymentID, "deployment_record_missing", "发布记录读取失败", err, "")
	}
	timeout := time.Duration(record.RolloutTimeout) * time.Second
	var previousImage string
	var warning error
	var err error
	switch record.Platform {
	case model.DeploymentDocker:
		previousImage, warning, err = s.docker.DeployContainer(
			ctx, record.RuntimeID, record.WorkloadName, record.Image, record.ID, timeout,
		)
	case model.DeploymentKubernetes:
		previousImage, err = s.kube.DeployImage(
			ctx, record.RuntimeID, record.Namespace, record.WorkloadName,
			record.ContainerName, record.Image, record.ID, timeout,
		)
	default:
		err = ErrInvalidTarget
	}
	if err != nil {
		return s.markFailed(ctx, deploymentID, "deployment_execution_failed", "发布执行失败，需人工确认目标状态", err, previousImage)
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
		}).Error; err != nil {
		return fmt.Errorf("记录发布成功状态失败: %w", err)
	}
	return nil
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
	input.RuntimeID = strings.TrimSpace(input.RuntimeID)
	input.Namespace = strings.TrimSpace(input.Namespace)
	input.WorkloadName = strings.TrimSpace(input.WorkloadName)
	input.ContainerName = strings.TrimSpace(input.ContainerName)
	if input.RolloutTimeout == 0 {
		input.RolloutTimeout = 300
	}
	if !targetNamePattern.MatchString(input.Name) || input.RuntimeID == "" ||
		input.RolloutTimeout < 30 || input.RolloutTimeout > 3600 || !validEnvironment(input.Environment) {
		return TargetInput{}, ErrInvalidTarget
	}
	switch input.Platform {
	case model.DeploymentDocker:
		if !workloadNamePattern.MatchString(input.WorkloadName) {
			return TargetInput{}, ErrInvalidTarget
		}
		input.Namespace = ""
		input.ContainerName = ""
		endpoint, err := s.docker.Find(ctx, input.RuntimeID)
		if err != nil || !endpoint.IsActive {
			return TargetInput{}, ErrInvalidTarget
		}
	case model.DeploymentKubernetes:
		if len(validation.IsDNS1123Label(input.Namespace)) > 0 ||
			len(validation.IsDNS1123Subdomain(input.WorkloadName)) > 0 ||
			len(validation.IsDNS1123Label(input.ContainerName)) > 0 {
			return TargetInput{}, ErrInvalidTarget
		}
		cluster, err := s.kube.Find(ctx, input.RuntimeID)
		if err != nil || !cluster.IsActive {
			return TargetInput{}, ErrInvalidTarget
		}
	default:
		return TargetInput{}, ErrInvalidTarget
	}
	return input, nil
}

func (s *Service) markFailed(ctx context.Context, id, code, message string, cause error, previousImage string) error {
	now := time.Now().UTC()
	updateContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.db.WithContext(updateContext).Model(&model.DeploymentRecord{}).Where("id = ?", id).
		Updates(map[string]any{
			"status": model.DeploymentFailed, "previous_image": previousImage,
			"error_code": code, "error_message": message,
			"finished_at": now, "updated_at": now,
		}).Error; err != nil {
		return fmt.Errorf("记录发布失败状态时发生错误: %v；原始错误: %w", err, cause)
	}
	return cause
}

func applyTargetSnapshot(record *model.DeploymentRecord, target *model.DeploymentTarget) {
	record.TargetName = target.Name
	record.Platform = target.Platform
	record.Environment = target.Environment
	record.RuntimeID = target.RuntimeID
	record.Namespace = target.Namespace
	record.WorkloadName = target.WorkloadName
	record.ContainerName = target.ContainerName
	record.RolloutTimeout = target.RolloutTimeout
}

func copyTargetSnapshot(destination, source *model.DeploymentRecord) {
	destination.TargetName = source.TargetName
	destination.Platform = source.Platform
	destination.Environment = source.Environment
	destination.RuntimeID = source.RuntimeID
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

func validEnvironment(environment model.EnvironmentType) bool {
	return environment == model.EnvironmentDevelopment || environment == model.EnvironmentStaging || environment == model.EnvironmentProduction
}
