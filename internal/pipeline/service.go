package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	distributionreference "github.com/distribution/reference"
	"github.com/google/uuid"
	"github.com/regclient/regclient"
	registryconfig "github.com/regclient/regclient/config"
	"github.com/regclient/regclient/scheme/reg"
	registryerrors "github.com/regclient/regclient/types/errs"
	registryreference "github.com/regclient/regclient/types/ref"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/artifact"
	"edo/internal/deployment"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/notification"
	"edo/internal/repository"
	"edo/internal/secret"
)

var (
	ErrInvalidApplication             = errors.New("应用配置无效")
	ErrInvalidApplicationName         = errors.New("应用名必须以小写英文字母开头，只能使用小写英文字母和单个下划线")
	ErrApplicationExists              = errors.New("应用名称已存在")
	ErrApplicationNotFound            = errors.New("应用不存在")
	ErrInvalidBuildPlan               = errors.New("构建方案配置无效")
	ErrBuildPlanExists                = errors.New("构建方案名称已存在")
	ErrBuildPlanNotFound              = errors.New("构建方案不存在")
	ErrBuildPlanInUse                 = errors.New("构建方案仍被流水线使用，请先更换相关构建任务")
	ErrInvalidScriptEnvironment       = errors.New("脚本环境变量无效；CI、HOME、TMPDIR 和 EDO 流水线元数据名称由系统保留")
	ErrInvalidRegistry                = errors.New("镜像仓库配置无效")
	ErrInvalidRegistryName            = errors.New("镜像仓库名称格式无效")
	ErrInvalidRegistryProvider        = errors.New("镜像仓库类型无效")
	ErrInvalidRegistryEndpoint        = errors.New("镜像仓库地址格式无效")
	ErrRegistryProviderEndpoint       = errors.New("Docker Hub 使用系统固定地址，其他仓库请选择 Harbor 或通用 Registry")
	ErrInsecureRegistryEndpoint       = errors.New("HTTP 镜像仓库需要显式允许不安全连接")
	ErrInvalidRegistryNamespace       = errors.New("镜像仓库命名空间格式无效")
	ErrInvalidRegistryUsername        = errors.New("镜像仓库用户名过长")
	ErrInvalidRegistrySecret          = errors.New("镜像仓库密码或 Token 过长")
	ErrRegistryLoginFailed            = errors.New("镜像仓库登录失败，请检查用户名和密码或 Token")
	ErrRegistryConnectionFailed       = errors.New("无法连接镜像仓库，请检查地址和网络")
	ErrRegistryExists                 = errors.New("镜像仓库名称已存在")
	ErrInvalidDeploymentPlan          = errors.New("部署方案配置无效")
	ErrDeploymentPlanExists           = errors.New("部署方案名称已存在")
	ErrDeploymentPlanNotFound         = errors.New("部署方案不存在")
	ErrDeploymentPlanInUse            = errors.New("部署方案仍被流水线使用，请先更换相关部署任务")
	ErrDeploymentPlanTargetMismatch   = errors.New("部署方案的执行方式与所选运行环境不匹配")
	ErrWorkflowTemplateNotFound       = errors.New("流水线方案不存在或未启用")
	ErrPipelineIncomplete             = errors.New("应用尚未绑定完整的构建与发布流程")
	ErrApplicationRepositoryInvariant = errors.New("应用代码仓库关联数据不完整")
)

var resourceNamePattern = regexp.MustCompile(`^[A-Za-z0-9\p{Han}][A-Za-z0-9\p{Han}_. -]{0,127}$`)
var applicationNamePattern = regexp.MustCompile(`^[a-z]+(?:_[a-z]+)*$`)

// 镜像仓库名称是显示标签，也允许管理员直接使用 host/namespace 形式命名。
var registryNamePattern = regexp.MustCompile(`^[A-Za-z0-9\p{Han}][A-Za-z0-9\p{Han}_. /:-]{0,127}$`)

type ApplicationInput struct {
	Name                string
	Description         string
	RepositoryID        string
	PollIntervalSeconds int
	WorkflowTemplateID  string
}

type BuildPlanInput struct {
	Name                 string
	Kind                 model.BuildPlanKind
	Description          string
	Script               string
	DockerfilePath       string
	ContextPath          string
	WorkingDirectory     string
	ArtifactPath         string
	RuntimeImage         string
	ImageRegistryID      string
	TargetStage          string
	Pull                 *bool
	CacheEnabled         *bool
	BuildArgs            map[string]string
	EnvironmentVariables map[string]string
	TimeoutSeconds       int
}

type RegistryInput struct {
	Name              string
	Provider          model.RegistryProvider
	Endpoint          string
	Namespace         string
	Username          string
	Credential        *string
	AllowInsecureHTTP bool
}

type DeploymentPlanInput struct {
	Name             string
	Kind             model.DeploymentPlanKind
	DeploymentTarget *deployment.TargetInput
	Description      string
	Script           string
	ComposeYAML      string
	ServiceName      string
	DockerConfig     model.DockerContainerConfig
	TimeoutSeconds   int
}

type Service struct {
	db                     *gorm.DB
	repositories           *repository.Service
	secrets                *secret.Manager
	docker                 *dockerengine.Service
	scriptRunner           scriptContainerRunner
	artifacts              *artifact.Service
	deployments            *deployment.Service
	notifications          workflowNotificationEnqueuer
	workflowRuntimes       WorkflowRuntimeManager
	logger                 *slog.Logger
	workflowRuntimeMu      sync.Mutex
	workflowRuntimeJobs    map[string]workflowRuntimePreparation
	releasePlanExecutionMu sync.Mutex
	pipelineAdvanceMu      sync.Mutex
}

func NewService(db *gorm.DB, repositories *repository.Service, secrets *secret.Manager) *Service {
	return &Service{db: db, repositories: repositories, secrets: secrets, logger: slog.Default()}
}

func (s *Service) ConfigureExecution(docker *dockerengine.Service, deployments *deployment.Service, logger *slog.Logger) {
	s.docker, s.scriptRunner, s.workflowRuntimes, s.deployments = docker, docker, docker, deployments
	if logger != nil {
		s.logger = logger
	}
}

func (s *Service) ConfigureArtifacts(artifacts *artifact.Service) {
	s.artifacts = artifacts
}

type workflowNotificationEnqueuer interface {
	Enqueue(context.Context, notification.EnqueueInput) (*model.Notification, error)
}

func (s *Service) ConfigureNotifications(notifications workflowNotificationEnqueuer) {
	s.notifications = notifications
}

func (s *Service) ListApplications(ctx context.Context) ([]model.Application, error) {
	var applications []model.Application
	err := s.db.WithContext(ctx).
		Preload("Repository").
		Preload("Workflows", func(db *gorm.DB) *gorm.DB { return db.Order("created_at ASC") }).
		Preload("Workflows.WorkflowTemplate").
		Preload("Repositories").
		Preload("Repositories.Observations").
		Preload("Repositories.Repository").
		Order("name ASC").Find(&applications).Error
	if err != nil {
		return nil, fmt.Errorf("查询应用失败: %w", err)
	}
	for i := range applications {
		setLegacyApplicationWorkflow(&applications[i])
	}
	return applications, nil
}

func (s *Service) FindApplication(ctx context.Context, id string) (*model.Application, error) {
	var application model.Application
	err := s.db.WithContext(ctx).
		Preload("Repository").
		Preload("Workflows", func(db *gorm.DB) *gorm.DB { return db.Order("created_at ASC") }).
		Preload("Workflows.WorkflowTemplate").
		Preload("Repositories").
		Preload("Repositories.Observations").
		Preload("Repositories.Repository").
		First(&application, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrApplicationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询应用失败: %w", err)
	}
	setLegacyApplicationWorkflow(&application)
	return &application, nil
}

func setLegacyApplicationWorkflow(application *model.Application) {
	application.Workflow = nil
	application.WorkflowTemplateID = ""
	application.WorkflowTemplate = nil
	// 旧的包内单流水线调用只在结果唯一时才允许继续工作；多流水线应用必须
	// 显式携带 workflow_id，绝不能为兼容调用任取第一条流水线。
	if len(application.Workflows) == 1 {
		application.Workflow = &application.Workflows[0]
		application.WorkflowTemplateID = application.Workflow.WorkflowTemplateID
		application.WorkflowTemplate = application.Workflow.WorkflowTemplate
	}
}

func (s *Service) CreateApplication(ctx context.Context, actorID string, input ApplicationInput) (*model.Application, error) {
	input, err := s.normalizeApplication(ctx, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	application := &model.Application{
		ID: uuid.NewString(), Name: input.Name, Description: input.Description,
		RepositoryID: input.RepositoryID, PollIntervalSeconds: input.PollIntervalSeconds,
		SyncStatus: model.ApplicationSyncIdle, IsActive: true,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	application.WorkflowTemplateID = input.WorkflowTemplateID
	repositoryLink := buildApplicationRepositoryModel(application.ID, input.RepositoryID, now)
	workflow, err := s.newApplicationWorkflow(ctx, application, actorID, now)
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(application).Error; err != nil {
			return err
		}
		if err := tx.Create(&repositoryLink).Error; err != nil {
			return err
		}
		return tx.Create(workflow).Error
	}); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrApplicationExists
		}
		return nil, fmt.Errorf("创建应用失败: %w", err)
	}
	return s.FindApplication(ctx, application.ID)
}

func (s *Service) UpdateApplication(ctx context.Context, id string, input ApplicationInput) (*model.Application, error) {
	existing, err := s.FindApplication(ctx, id)
	if err != nil {
		return nil, err
	}
	input, err = s.normalizeApplication(ctx, input)
	if err != nil {
		return nil, err
	}
	updates := map[string]any{
		"name": input.Name, "description": input.Description, "repository_id": input.RepositoryID,
		"poll_interval_seconds": input.PollIntervalSeconds,
		"updated_at":            time.Now().UTC(),
	}
	if existing.RepositoryID != input.RepositoryID {
		updates["sync_status"] = model.ApplicationSyncIdle
		updates["sync_message"] = ""
		updates["last_checked_at"] = nil
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Application{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
			return err
		}
		if err := saveApplicationRepository(tx, existing, input.RepositoryID, time.Now().UTC()); err != nil {
			return err
		}
		return nil
	}); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrApplicationExists
		}
		return nil, fmt.Errorf("更新应用失败: %w", err)
	}
	return s.FindApplication(ctx, id)
}

func (s *Service) SetApplicationActive(ctx context.Context, id string, active bool) error {
	result := s.db.WithContext(ctx).Model(&model.Application{}).Where("id = ?", id).
		Updates(map[string]any{"is_active": active, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("修改应用状态失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrApplicationNotFound
	}
	return nil
}

func (s *Service) normalizeApplication(ctx context.Context, input ApplicationInput) (ApplicationInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.RepositoryID = strings.TrimSpace(input.RepositoryID)
	input.WorkflowTemplateID = strings.TrimSpace(input.WorkflowTemplateID)
	if len(input.Name) > 128 || !applicationNamePattern.MatchString(input.Name) {
		return input, ErrInvalidApplicationName
	}
	if input.RepositoryID == "" {
		return input, ErrInvalidApplication
	}
	var activeRepository model.GitRepository
	if err := s.db.WithContext(ctx).First(&activeRepository, "id = ? AND is_active = ?", input.RepositoryID, true).Error; err != nil {
		return input, ErrInvalidApplication
	}
	if input.WorkflowTemplateID != "" {
		var template model.ReleaseWorkflowTemplate
		if err := s.db.WithContext(ctx).First(&template, "id = ? AND is_active = ?", input.WorkflowTemplateID, true).Error; err != nil {
			return input, ErrWorkflowTemplateNotFound
		}
	}
	if utf8.RuneCountInString(input.Description) > 500 || input.RepositoryID == "" {
		return input, ErrInvalidApplication
	}
	if input.PollIntervalSeconds == 0 {
		input.PollIntervalSeconds = 3
	}
	repo, err := s.repositories.Find(ctx, input.RepositoryID)
	if err != nil || !repo.IsActive {
		return input, ErrInvalidApplication
	}
	if !validPollIntervalSeconds(input.PollIntervalSeconds) {
		return input, ErrInvalidApplication
	}
	return input, nil
}

func deploymentPlanSupportsTarget(kind model.DeploymentPlanKind, platform model.DeploymentPlatform) bool {
	switch kind {
	case model.DeploymentPlanScript:
		return platform == model.DeploymentSSH
	case model.DeploymentPlanKubernetes:
		return platform == model.DeploymentKubernetes
	case model.DeploymentPlanDocker, model.DeploymentPlanCompose:
		return platform == model.DeploymentDocker
	default:
		return false
	}
}

func buildApplicationRepositoryModel(applicationID, repositoryID string, now time.Time) model.ApplicationRepository {
	return model.ApplicationRepository{
		ID: uuid.NewString(), ApplicationID: applicationID, RepositoryID: repositoryID,
		CreatedAt: now, UpdatedAt: now,
	}
}

func saveApplicationRepository(tx *gorm.DB, application *model.Application, repositoryID string, now time.Time) error {
	if len(application.Repositories) > 1 {
		return ErrApplicationRepositoryInvariant
	}
	if len(application.Repositories) == 0 {
		repository := buildApplicationRepositoryModel(application.ID, repositoryID, now)
		return tx.Create(&repository).Error
	}
	link := application.Repositories[0]
	updates := map[string]any{"repository_id": repositoryID, "updated_at": now}
	if link.RepositoryID != repositoryID {
		updates["last_observed_ref"] = ""
		updates["last_observed_commit"] = ""
		updates["last_checked_at"] = nil
		if err := tx.Where("application_repository_id = ?", link.ID).
			Delete(&model.ApplicationRepositoryObservation{}).Error; err != nil {
			return err
		}
	}
	return tx.Model(&model.ApplicationRepository{}).Where("id = ?", link.ID).Updates(updates).Error
}

func (s *Service) ListBuildPlans(ctx context.Context) ([]model.BuildPlan, error) {
	var plans []model.BuildPlan
	if err := s.db.WithContext(ctx).Preload("ImageRegistry").Order("name ASC").Find(&plans).Error; err != nil {
		return nil, fmt.Errorf("查询构建方案失败: %w", err)
	}
	return plans, nil
}

func (s *Service) CreateBuildPlan(ctx context.Context, actorID string, input BuildPlanInput) (*model.BuildPlan, error) {
	input, err := normalizeBuildPlanInput(input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	plan := &model.BuildPlan{
		ID: uuid.NewString(), Name: input.Name, Kind: input.Kind, ConfigVersion: 1, Description: input.Description,
		Script: input.Script, DockerfilePath: input.DockerfilePath, ContextPath: input.ContextPath,
		WorkingDirectory: input.WorkingDirectory, ArtifactPath: input.ArtifactPath, RuntimeImage: input.RuntimeImage,
		ImageRegistryID: input.ImageRegistryID, TargetStage: input.TargetStage,
		Pull: *input.Pull, CacheEnabled: *input.CacheEnabled, BuildArgs: input.BuildArgs,
		EnvironmentVariables: input.EnvironmentVariables, TimeoutSeconds: input.TimeoutSeconds,
		IsActive: true, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.validateBuildPlanRegistry(ctx, plan.ImageRegistryID); err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Create(plan).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrBuildPlanExists
		}
		return nil, fmt.Errorf("创建构建方案失败: %w", err)
	}
	return plan, nil
}

func normalizeBuildPlanInput(input BuildPlanInput) (BuildPlanInput, error) {
	input.Name, input.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
	input.DockerfilePath = strings.TrimSpace(input.DockerfilePath)
	input.ContextPath, input.WorkingDirectory = strings.TrimSpace(input.ContextPath), strings.TrimSpace(input.WorkingDirectory)
	input.ArtifactPath, input.RuntimeImage = strings.TrimSpace(input.ArtifactPath), strings.TrimSpace(input.RuntimeImage)
	input.ImageRegistryID = strings.TrimSpace(input.ImageRegistryID)
	input.TargetStage = strings.TrimSpace(input.TargetStage)
	if input.ContextPath == "" {
		input.ContextPath = "."
	}
	if input.WorkingDirectory == "" {
		input.WorkingDirectory = "."
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 1800
	}
	if input.Pull == nil {
		input.Pull = boolPointer(true)
	}
	if input.CacheEnabled == nil {
		input.CacheEnabled = boolPointer(true)
	}
	if input.BuildArgs == nil {
		input.BuildArgs = map[string]string{}
	}
	if input.EnvironmentVariables == nil {
		input.EnvironmentVariables = map[string]string{}
	}
	switch input.Kind {
	case model.BuildPlanDockerfile:
		input.Script = ""
		input.WorkingDirectory = "."
		input.ArtifactPath, input.RuntimeImage = "", ""
		input.EnvironmentVariables = map[string]string{}
		if input.DockerfilePath == "" {
			input.DockerfilePath = "Dockerfile"
		}
	case model.BuildPlanScript:
		input.DockerfilePath, input.ImageRegistryID = "", ""
		input.TargetStage = ""
		input.BuildArgs = map[string]string{}
		if input.RuntimeImage == "" {
			input.RuntimeImage = model.DefaultRuntimeImage
		}
		if !validScriptEnvironmentVariables(input.EnvironmentVariables) {
			return input, ErrInvalidScriptEnvironment
		}
	}
	validKind := (input.Kind == model.BuildPlanScript && strings.TrimSpace(input.Script) != "" && validRuntimeImageReference(input.RuntimeImage)) ||
		(input.Kind == model.BuildPlanDockerfile && input.DockerfilePath != "")
	if !validResourceName(input.Name) || !validKind || input.TimeoutSeconds < 30 || input.TimeoutSeconds > 7200 ||
		len(input.Script) > 256*1024 || utf8.RuneCountInString(input.Description) > 500 ||
		len(input.DockerfilePath) > 512 || len(input.ContextPath) > 512 || len(input.WorkingDirectory) > 512 ||
		len(input.ArtifactPath) > 512 || len(input.RuntimeImage) > 512 || len(input.ImageRegistryID) > 36 || len(input.TargetStage) > 128 ||
		(input.TargetStage != "" && !buildTargetStagePattern.MatchString(input.TargetStage)) ||
		!validBuildVariables(input.BuildArgs) ||
		(input.Kind == model.BuildPlanDockerfile && !dockerengine.ValidBuildArgs(input.BuildArgs)) {
		return input, ErrInvalidBuildPlan
	}
	return input, nil
}

func boolPointer(value bool) *bool { return &value }

var buildVariableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var buildTargetStagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// validRuntimeImageReference 只接受固定 tag 或 digest。裸镜像名和 latest 会让
// 同一份流水线快照在不同时间解析到不同内容，不能作为可审计的执行配置。
func validRuntimeImageReference(value string) bool {
	if value == "" || len(value) > 512 {
		return false
	}
	named, err := distributionreference.ParseNormalizedNamed(value)
	if err != nil {
		return false
	}
	if tagged, ok := named.(distributionreference.Tagged); ok {
		if strings.EqualFold(tagged.Tag(), "latest") {
			return false
		}
		return true
	}
	_, ok := named.(distributionreference.Digested)
	return ok
}

func validBuildVariables(values map[string]string) bool {
	if len(values) > 100 {
		return false
	}
	for name, value := range values {
		if !buildVariableNamePattern.MatchString(name) || len(value) > 16*1024 {
			return false
		}
	}
	return true
}

var reservedScriptEnvironmentNames = map[string]struct{}{
	"CI": {}, "HOME": {}, "TMPDIR": {},
	"EDO_PIPELINE_RUN_ID": {}, "EDO_APPLICATION_ID": {}, "EDO_GIT_REF": {}, "EDO_COMMIT_SHA": {},
	"EDO_TARGET_PLATFORM": {}, "EDO_TARGET_ARCH": {}, "GOOS": {}, "GOARCH": {},
}

// 脚本运行元数据和受控目录由执行器固定注入。保存阶段直接拒绝同名变量，
// 避免用户配置看似成功、运行时却被系统静默覆盖。
func validScriptEnvironmentVariables(values map[string]string) bool {
	if !validBuildVariables(values) {
		return false
	}
	for name := range values {
		if _, reserved := reservedScriptEnvironmentNames[name]; reserved {
			return false
		}
	}
	return true
}

func (s *Service) validateBuildPlanRegistry(ctx context.Context, registryID string) error {
	if registryID == "" {
		return nil
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.ImageRegistry{}).
		Where("id = ? AND is_active = ?", registryID, true).Count(&count).Error; err != nil {
		return fmt.Errorf("校验构建镜像仓库失败: %w", err)
	}
	if count != 1 {
		return ErrInvalidBuildPlan
	}
	return nil
}

func (s *Service) UpdateBuildPlan(ctx context.Context, id string, input BuildPlanInput) (*model.BuildPlan, error) {
	input, err := normalizeBuildPlanInput(input)
	if err != nil {
		return nil, err
	}
	var existing model.BuildPlan
	if err := s.db.WithContext(ctx).First(&existing, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBuildPlanNotFound
		}
		return nil, fmt.Errorf("读取待更新构建方案失败: %w", err)
	}
	if err := s.validateBuildPlanRegistry(ctx, input.ImageRegistryID); err != nil {
		return nil, err
	}
	buildArgsJSON, err := json.Marshal(input.BuildArgs)
	if err != nil {
		return nil, ErrInvalidBuildPlan
	}
	environmentJSON, err := json.Marshal(input.EnvironmentVariables)
	if err != nil {
		return nil, ErrInvalidBuildPlan
	}
	result := s.db.WithContext(ctx).Model(&model.BuildPlan{}).Where("id = ?", id).Updates(map[string]any{
		"name": input.Name, "kind": input.Kind, "description": input.Description,
		"script": input.Script, "dockerfile_path": input.DockerfilePath, "context_path": input.ContextPath,
		"working_directory": input.WorkingDirectory, "artifact_path": input.ArtifactPath, "runtime_image": input.RuntimeImage,
		"image_registry_id": input.ImageRegistryID, "target_stage": input.TargetStage, "platform": "",
		"pull": *input.Pull, "cache_enabled": *input.CacheEnabled, "build_args": string(buildArgsJSON),
		"environment_variables": string(environmentJSON), "timeout_seconds": input.TimeoutSeconds,
		"config_version": existing.ConfigVersion + 1, "updated_at": time.Now().UTC(),
	})
	if result.Error != nil {
		err = result.Error
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrBuildPlanExists
		}
		return nil, fmt.Errorf("更新构建方案失败: %w", err)
	}
	var plan model.BuildPlan
	if err := s.db.WithContext(ctx).Preload("ImageRegistry").First(&plan, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBuildPlanNotFound
		}
		return nil, fmt.Errorf("读取更新后的构建方案失败: %w", err)
	}
	return &plan, nil
}

func (s *Service) SetBuildPlanActive(ctx context.Context, id string, active bool) error {
	var plan model.BuildPlan
	if err := s.db.WithContext(ctx).First(&plan, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBuildPlanNotFound
		}
		return fmt.Errorf("读取待修改状态的构建方案失败: %w", err)
	}
	result := s.db.WithContext(ctx).Model(&model.BuildPlan{}).Where("id = ?", id).
		Updates(map[string]any{"is_active": active, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("修改构建方案状态失败: %w", result.Error)
	}
	return nil
}

func (s *Service) DeleteBuildPlan(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var plan model.BuildPlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&plan, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBuildPlanNotFound
			}
			return fmt.Errorf("读取待删除构建方案失败: %w", err)
		}
		inUse, err := workflowResourceInUse(tx, model.WorkflowNodeBuild, plan.ID)
		if err != nil {
			return fmt.Errorf("检查构建方案流水线关联失败: %w", err)
		}
		if inUse {
			return ErrBuildPlanInUse
		}
		if err := tx.Delete(&plan).Error; err != nil {
			return fmt.Errorf("删除构建方案失败: %w", err)
		}
		return nil
	})
}

// workflowResourceInUse 在调用者的同一事务中检查所有应用流水线和公共方案。
// 停用、草稿配置也可能之后重新启用，因此不能绕过引用保护。
func workflowResourceInUse(tx *gorm.DB, nodeType model.WorkflowNodeType, resourceID string) (bool, error) {
	var workflows []model.ReleaseWorkflow
	if err := tx.Find(&workflows).Error; err != nil {
		return false, err
	}
	for i := range workflows {
		if workflowStagesReference(workflows[i].Stages, nodeType, resourceID) {
			return true, nil
		}
	}
	var templates []model.ReleaseWorkflowTemplate
	if err := tx.Find(&templates).Error; err != nil {
		return false, err
	}
	for i := range templates {
		if workflowStagesReference(templates[i].Stages, nodeType, resourceID) {
			return true, nil
		}
	}
	return false, nil
}

func workflowStagesReference(stages []model.WorkflowStage, nodeType model.WorkflowNodeType, resourceID string) bool {
	for i := range stages {
		for j := range stages[i].Tasks {
			task := stages[i].Tasks[j]
			if task.Type != nodeType {
				continue
			}
			if nodeType == model.WorkflowNodeBuild && task.Config.BuildPlanID == resourceID {
				return true
			}
			if nodeType == model.WorkflowNodeDeploy && task.Config.DeploymentPlanID == resourceID {
				return true
			}
		}
	}
	return false
}

func (s *Service) ListRegistries(ctx context.Context) ([]model.ImageRegistry, error) {
	var registries []model.ImageRegistry
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&registries).Error; err != nil {
		return nil, fmt.Errorf("查询镜像仓库失败: %w", err)
	}
	return registries, nil
}

func (s *Service) CreateRegistry(ctx context.Context, actorID string, input RegistryInput) (*model.ImageRegistry, error) {
	input, parsed, err := normalizeRegistryInput(input)
	if err != nil {
		return nil, err
	}
	credentialCiphertext := ""
	if input.Credential != nil && *input.Credential != "" {
		credentialCiphertext, err = s.secrets.Encrypt(*input.Credential, []byte("image_registry:"+input.Name+":credential"))
		if err != nil {
			return nil, fmt.Errorf("加密镜像仓库凭据失败: %w", err)
		}
	}
	now := time.Now().UTC()
	registry := &model.ImageRegistry{
		ID: uuid.NewString(), Name: input.Name, Provider: input.Provider, Endpoint: strings.TrimSuffix(parsed.String(), "/"),
		Namespace: input.Namespace, Username: input.Username, CredentialCiphertext: credentialCiphertext,
		AllowInsecureHTTP: input.AllowInsecureHTTP, IsActive: true, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(registry).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrRegistryExists
		}
		return nil, fmt.Errorf("创建镜像仓库失败: %w", err)
	}
	return registry, nil
}

// TestRegistry 使用与创建相同的配置校验，但不会持久化配置或凭据。
func (s *Service) TestRegistry(ctx context.Context, input RegistryInput) error {
	input, parsed, err := normalizeRegistryInput(input)
	if err != nil {
		return err
	}
	tlsConfig := registryconfig.TLSEnabled
	if parsed.Scheme == "http" {
		tlsConfig = registryconfig.TLSDisabled
	}
	credential := ""
	if input.Credential != nil {
		credential = *input.Credential
	}
	host := registryconfig.HostNewName(parsed.Host)
	host.TLS = tlsConfig
	host.PathPrefix = strings.Trim(parsed.Path, "/")
	host.User = input.Username
	if input.Username == "" {
		host.Token = credential
	} else {
		host.Pass = credential
	}
	reference, err := registryreference.NewHost(host.Name)
	if err != nil {
		return ErrInvalidRegistryEndpoint
	}
	testContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client := regclient.New(
		regclient.WithConfigHost(*host),
		regclient.WithRegOpts(reg.WithDelay(100*time.Millisecond, 500*time.Millisecond), reg.WithRetryLimit(1)),
		regclient.WithUserAgent("edo"),
	)
	if _, err := client.Ping(testContext, reference); err != nil {
		if errors.Is(err, registryerrors.ErrHTTPUnauthorized) || errors.Is(err, registryerrors.ErrNoLogin) {
			return fmt.Errorf("%w: %v", ErrRegistryLoginFailed, err)
		}
		return fmt.Errorf("%w: %v", ErrRegistryConnectionFailed, err)
	}
	return nil
}

func normalizeRegistryInput(input RegistryInput) (RegistryInput, *url.URL, error) {
	input.Name, input.Endpoint = strings.TrimSpace(input.Name), strings.TrimSpace(input.Endpoint)
	input.Namespace, input.Username = strings.Trim(strings.TrimSpace(input.Namespace), "/"), strings.TrimSpace(input.Username)
	if !registryNamePattern.MatchString(input.Name) {
		return input, nil, ErrInvalidRegistryName
	}
	if !validRegistryProvider(input.Provider) {
		return input, nil, ErrInvalidRegistryProvider
	}
	if input.Provider == model.RegistryDockerHub {
		if input.Endpoint != "" && !isDockerHubEndpoint(input.Endpoint) {
			return input, nil, ErrRegistryProviderEndpoint
		}
		input.Endpoint = model.DockerHubEndpoint
		input.AllowInsecureHTTP = false
	}
	if !validRegistryNamespace(input.Namespace) {
		return input, nil, ErrInvalidRegistryNamespace
	}
	if utf8.RuneCountInString(input.Username) > 255 {
		return input, nil, ErrInvalidRegistryUsername
	}
	parsed, err := url.Parse(input.Endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return input, nil, ErrInvalidRegistryEndpoint
	}
	if parsed.Scheme == "http" && !input.AllowInsecureHTTP {
		return input, nil, ErrInsecureRegistryEndpoint
	}
	if input.Credential != nil && len(*input.Credential) > 64*1024 {
		return input, nil, ErrInvalidRegistrySecret
	}
	return input, parsed, nil
}

func isDockerHubEndpoint(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	host, endpointPath := strings.ToLower(parsed.Host), strings.Trim(parsed.Path, "/")
	switch host {
	case "docker.io", "registry-1.docker.io":
		return endpointPath == ""
	case "index.docker.io":
		return endpointPath == "" || endpointPath == "v1"
	default:
		return false
	}
}

func (s *Service) ListDeploymentPlans(ctx context.Context) ([]model.DeploymentPlan, error) {
	var plans []model.DeploymentPlan
	if err := s.db.WithContext(ctx).Preload("DeploymentTarget").Order("name ASC").Find(&plans).Error; err != nil {
		return nil, fmt.Errorf("查询部署方案失败: %w", err)
	}
	return plans, nil
}

func normalizeDeploymentPlanInput(input DeploymentPlanInput) (DeploymentPlanInput, error) {
	input.Name, input.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
	input.ComposeYAML, input.ServiceName = strings.TrimSpace(input.ComposeYAML), strings.TrimSpace(input.ServiceName)
	if input.ComposeYAML != "" {
		input.ComposeYAML += "\n"
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 600
	}
	validKind := (input.Kind == model.DeploymentPlanScript && strings.TrimSpace(input.Script) != "") ||
		input.Kind == model.DeploymentPlanKubernetes ||
		(input.Kind == model.DeploymentPlanCompose && input.ComposeYAML != "" && input.ServiceName != "") ||
		input.Kind == model.DeploymentPlanDocker
	if input.DeploymentTarget == nil || !validResourceName(input.Name) || !validKind || input.TimeoutSeconds < 30 || input.TimeoutSeconds > 3600 ||
		len(input.Script) > 256*1024 || len(input.ComposeYAML) > dockerengine.MaximumComposeYAMLBytes ||
		utf8.RuneCountInString(input.Description) > 500 {
		return input, ErrInvalidDeploymentPlan
	}
	switch input.Kind {
	case model.DeploymentPlanScript:
		input.ComposeYAML, input.ServiceName, input.DockerConfig = "", "", model.DockerContainerConfig{}
	case model.DeploymentPlanKubernetes:
		input.Script, input.ComposeYAML, input.ServiceName, input.DockerConfig = "", "", "", model.DockerContainerConfig{}
	case model.DeploymentPlanCompose:
		input.Script, input.DockerConfig = "", model.DockerContainerConfig{}
		if err := dockerengine.ValidateComposeYAML(input.ComposeYAML, input.ServiceName); err != nil {
			return input, ErrInvalidDeploymentPlan
		}
	case model.DeploymentPlanDocker:
		input.Script, input.ComposeYAML = "", ""
		var err error
		input.DockerConfig, err = dockerengine.NormalizeContainerConfig(input.DockerConfig)
		if err != nil {
			return input, ErrInvalidDeploymentPlan
		}
	}
	return input, nil
}

func (s *Service) CreateDeploymentPlan(ctx context.Context, actorID string, input DeploymentPlanInput) (*model.DeploymentPlan, error) {
	input, err := normalizeDeploymentPlanInput(input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	plan := &model.DeploymentPlan{
		ID: uuid.NewString(), Name: input.Name, Kind: input.Kind, Description: input.Description,
		Script:      input.Script,
		ComposeYAML: input.ComposeYAML, ServiceName: input.ServiceName, TimeoutSeconds: input.TimeoutSeconds,
		DockerConfig: input.DockerConfig,
		IsActive:     true, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if s.deployments == nil || !deploymentPlanSupportsTarget(input.Kind, input.DeploymentTarget.Platform) {
			return ErrDeploymentPlanTargetMismatch
		}
		target, createErr := s.deployments.WithTransaction(tx).CreateTarget(ctx, actorID, *input.DeploymentTarget)
		if createErr != nil {
			return createErr
		}
		plan.DeploymentTargetID, plan.DeploymentTarget = target.ID, target
		return tx.Create(plan).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrDeploymentPlanExists
		}
		if errors.Is(err, ErrDeploymentPlanTargetMismatch) || errors.Is(err, deployment.ErrInvalidTarget) ||
			errors.Is(err, deployment.ErrTargetExists) || errors.Is(err, deployment.ErrTargetNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("创建部署方案失败: %w", err)
	}
	return plan, nil
}

func (s *Service) UpdateDeploymentPlan(ctx context.Context, id string, input DeploymentPlanInput) (*model.DeploymentPlan, error) {
	input, err := normalizeDeploymentPlanInput(input)
	if err != nil {
		return nil, err
	}
	var plan model.DeploymentPlan
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&plan, "id = ?", strings.TrimSpace(id)).Error; findErr != nil {
			if errors.Is(findErr, gorm.ErrRecordNotFound) {
				return ErrDeploymentPlanNotFound
			}
			return findErr
		}
		targetID := plan.DeploymentTargetID
		if s.deployments == nil || !deploymentPlanSupportsTarget(input.Kind, input.DeploymentTarget.Platform) {
			return ErrDeploymentPlanTargetMismatch
		}
		deploymentService := s.deployments.WithTransaction(tx)
		var target *model.DeploymentTarget
		if targetID == "" {
			created, createErr := deploymentService.CreateTarget(ctx, plan.CreatedBy, *input.DeploymentTarget)
			if createErr != nil {
				return createErr
			}
			target = created
		} else {
			updated, updateErr := deploymentService.UpdateTarget(ctx, targetID, *input.DeploymentTarget)
			if updateErr != nil {
				return updateErr
			}
			target = updated
		}
		targetID = target.ID
		plan.Name, plan.Kind, plan.Description = input.Name, input.Kind, input.Description
		plan.DeploymentTargetID, plan.DeploymentTarget = targetID, target
		plan.Script = input.Script
		plan.ComposeYAML, plan.ServiceName = input.ComposeYAML, input.ServiceName
		plan.DockerConfig = input.DockerConfig
		plan.TimeoutSeconds, plan.UpdatedAt = input.TimeoutSeconds, time.Now().UTC()
		return tx.Save(&plan).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrDeploymentPlanExists
		}
		if errors.Is(err, ErrDeploymentPlanNotFound) || errors.Is(err, ErrDeploymentPlanTargetMismatch) ||
			errors.Is(err, deployment.ErrInvalidTarget) || errors.Is(err, deployment.ErrTargetExists) ||
			errors.Is(err, deployment.ErrTargetNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("更新部署方案失败: %w", err)
	}
	return &plan, nil
}

func (s *Service) SetDeploymentPlanActive(ctx context.Context, id string, active bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var plan model.DeploymentPlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&plan, "id = ?", strings.TrimSpace(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDeploymentPlanNotFound
			}
			return fmt.Errorf("读取待修改状态的部署方案失败: %w", err)
		}
		if plan.IsActive == active {
			return nil
		}
		if err := tx.Model(&model.DeploymentPlan{}).Where("id = ?", plan.ID).
			Updates(map[string]any{"is_active": active, "updated_at": time.Now().UTC()}).Error; err != nil {
			return fmt.Errorf("修改部署方案状态失败: %w", err)
		}
		return nil
	})
}

func (s *Service) DeleteDeploymentPlan(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var plan model.DeploymentPlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&plan, "id = ?", strings.TrimSpace(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDeploymentPlanNotFound
			}
			return fmt.Errorf("读取待删除部署方案失败: %w", err)
		}
		inUse, err := workflowResourceInUse(tx, model.WorkflowNodeDeploy, plan.ID)
		if err != nil {
			return fmt.Errorf("检查部署方案流水线关联失败: %w", err)
		}
		if inUse {
			return ErrDeploymentPlanInUse
		}
		if err := tx.Delete(&plan).Error; err != nil {
			return fmt.Errorf("删除部署方案失败: %w", err)
		}
		// DeploymentTarget 是部署方案的内部执行位置，不是用户可独立管理的资源。
		// 历史发布已在 DeploymentRecord 保存完整快照；继续保留孤儿 target 反而会
		// 永久阻止环境或主机删除。
		if plan.DeploymentTargetID != "" {
			var otherPlans int64
			if err := tx.Model(&model.DeploymentPlan{}).
				Where("deployment_target_id = ?", plan.DeploymentTargetID).Count(&otherPlans).Error; err != nil {
				return fmt.Errorf("检查部署位置关联失败: %w", err)
			}
			if otherPlans == 0 {
				if err := tx.Delete(&model.DeploymentTarget{}, "id = ?", plan.DeploymentTargetID).Error; err != nil {
					return fmt.Errorf("清理部署方案执行位置失败: %w", err)
				}
			}
		}
		return nil
	})
}

func (s *Service) ListRuns(ctx context.Context, limit int) ([]model.PipelineRun, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	var runs []model.PipelineRun
	if err := pipelineRunQuery(s.db.WithContext(ctx)).
		Order("created_at DESC").Limit(limit).Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("查询流水线运行失败: %w", err)
	}
	if err := s.enrichPipelineRuns(ctx, runs); err != nil {
		return nil, err
	}
	return runs, nil
}

func (s *Service) FindRun(ctx context.Context, id string) (*model.PipelineRun, error) {
	var run model.PipelineRun
	if err := pipelineRunQuery(s.db.WithContext(ctx)).First(&run, "id = ?", strings.TrimSpace(id)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPipelineRunNotFound
		}
		return nil, fmt.Errorf("读取流水线运行失败: %w", err)
	}
	runs := []model.PipelineRun{run}
	if err := s.enrichPipelineRuns(ctx, runs); err != nil {
		return nil, err
	}
	return &runs[0], nil
}

func pipelineRunQuery(db *gorm.DB) *gorm.DB {
	return db.Preload("Application").
		Preload("Repositories", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Preload("Repositories.Repository").Preload("Repositories.BuildPlan").Preload("Repositories.DeploymentPlan")
}

func (s *Service) enrichPipelineRuns(ctx context.Context, runs []model.PipelineRun) error {
	if err := s.attachRunReleasePlanIDs(ctx, runs); err != nil {
		return err
	}
	for i := range runs {
		if runs[i].WorkflowSnapshot == "" {
			continue
		}
		var snapshot workflowSnapshot
		if json.Unmarshal([]byte(runs[i].WorkflowSnapshot), &snapshot) == nil && snapshot.SchemaVersion == model.WorkflowSchemaVersion {
			snapshot.ApprovalEnabled = workflowHasApprovalNode(snapshot.Stages)
			runs[i].ApprovalRequired = snapshot.ApprovalEnabled
			graph := &model.PipelineRunGraph{
				SchemaVersion: snapshot.SchemaVersion,
				Source: model.PipelineRunGraphNode{
					ID: snapshot.Source.ID, Type: snapshot.Source.Type, Name: snapshot.Source.Name,
				},
				Stages: make([]model.PipelineRunGraphStage, 0, len(snapshot.Stages)),
			}
			if snapshot.Source.ID == runs[i].CurrentNodeID {
				runs[i].CurrentNodeName = snapshot.Source.Name
			}
			for _, stage := range snapshot.Stages {
				graphStage := model.PipelineRunGraphStage{ID: stage.ID, Name: stage.Name, Tasks: make([]model.PipelineRunGraphNode, 0, len(stage.Tasks))}
				for _, node := range stage.Tasks {
					graphNode := model.PipelineRunGraphNode{ID: node.ID, Type: node.Type, Name: node.Name}
					if target, ok := snapshot.DeploymentTargets[node.ID]; ok {
						graphNode.EnvironmentID = target.EnvironmentID
					}
					graphStage.Tasks = append(graphStage.Tasks, graphNode)
					if node.ID == runs[i].CurrentNodeID {
						runs[i].CurrentNodeName = node.Name
					}
				}
				graph.Stages = append(graph.Stages, graphStage)
			}
			runs[i].ExecutionGraph = graph
		}
	}
	return nil
}

func (s *Service) attachRunReleasePlanIDs(ctx context.Context, runs []model.PipelineRun) error {
	executionIDs := make([]string, 0, len(runs))
	seen := make(map[string]struct{}, len(runs))
	for i := range runs {
		id := strings.TrimSpace(runs[i].ReleasePlanExecutionID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		executionIDs = append(executionIDs, id)
	}
	if len(executionIDs) == 0 {
		return nil
	}
	var executions []model.ReleasePlanExecution
	if err := s.db.WithContext(ctx).Select("id", "release_plan_id").Where("id IN ?", executionIDs).Find(&executions).Error; err != nil {
		return fmt.Errorf("查询流水线关联发布计划失败: %w", err)
	}
	planIDs := make(map[string]string, len(executions))
	for i := range executions {
		planIDs[executions[i].ID] = executions[i].ReleasePlanID
	}
	for i := range runs {
		runs[i].ReleasePlanID = planIDs[runs[i].ReleasePlanExecutionID]
	}
	return nil
}

func (s *Service) PrepareRun(ctx context.Context, applicationID, actorID string) (*model.PipelineRun, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	if application.Workflow == nil || !application.Workflow.IsActive {
		return nil, ErrWorkflowNotActive
	}
	return s.prepareWorkflowRun(ctx, application, application.Workflow, actorID)
}

func (s *Service) PrepareWorkflowRun(ctx context.Context, applicationID, workflowID, actorID string) (*model.PipelineRun, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	workflow, err := s.FindApplicationWorkflow(ctx, applicationID, workflowID)
	if err != nil {
		return nil, err
	}
	return s.prepareWorkflowRun(ctx, application, workflow, actorID)
}

func (s *Service) prepareWorkflowRun(ctx context.Context, application *model.Application, workflow *model.ReleaseWorkflow, actorID string) (*model.PipelineRun, error) {
	if workflow == nil || !workflow.IsActive {
		return nil, ErrWorkflowNotActive
	}
	if !workflowHasManualReleaseSource(workflow) {
		return nil, ErrManualReleaseDisabled
	}
	if !pipelineExecutionConfiguredForWorkflow(application, workflow) {
		return s.createBlockedRun(ctx, application, workflow, actorID, pipelineExecutionIncompleteMessageForWorkflow(application, workflow))
	}
	return s.createManualSelectionRun(ctx, application, workflow, actorID)
}

func (s *Service) createBlockedRun(ctx context.Context, application *model.Application, workflow *model.ReleaseWorkflow, actorID, message string) (*model.PipelineRun, error) {
	run, err := s.createPendingRun(ctx, application, workflow, actorID, message)
	if err != nil {
		return nil, err
	}
	return run, ErrPipelineIncomplete
}

func (s *Service) createManualSelectionRun(ctx context.Context, application *model.Application, workflow *model.ReleaseWorkflow, actorID string) (*model.PipelineRun, error) {
	return s.createPendingRun(ctx, application, workflow, actorID, "请选择每个代码仓库要发布的 Commit")
}

func (s *Service) createPendingRun(ctx context.Context, application *model.Application, workflow *model.ReleaseWorkflow, actorID, message string) (*model.PipelineRun, error) {
	now := time.Now().UTC()
	run := &model.PipelineRun{
		ID: uuid.NewString(), DepartmentID: application.DepartmentID,
		ApplicationID: application.ID, Trigger: "manual",
		WorkflowID: workflow.ID, WorkflowRevision: workflow.Revision,
		Ref: "", CommitSHA: "",
		Status: model.PipelineRunBlocked, Stage: "configured", Message: message,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	components, err := pipelineRunRepositories(application, run.ID, "", "", now)
	if err != nil {
		return nil, err
	}
	for i := range components {
		components[i].Status = model.PipelineRunRepositoryPending
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		return tx.Create(&components).Error
	}); err != nil {
		return nil, fmt.Errorf("创建流水线运行失败: %w", err)
	}
	run.Repositories = components
	return run, nil
}

func (s *Service) SyncApplication(ctx context.Context, applicationID, trigger string) (*model.Application, *model.PipelineRun, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return nil, nil, err
	}
	if !application.IsActive {
		return application, nil, ErrApplicationNotFound
	}
	now := time.Now().UTC()
	_ = s.db.WithContext(ctx).Model(&model.Application{}).Where("id = ?", application.ID).
		Updates(map[string]any{"sync_status": model.ApplicationSyncChecking, "sync_message": "", "updated_at": now}).Error
	pollSources := applicationPollSources(application)
	link, err := applicationRepositoryLink(application)
	if err != nil {
		s.logger.Error("应用代码仓库关联违反数据不变量", "operation", "pipeline_sync_application", "application_id", application.ID, "repository_id", application.RepositoryID, "err", err)
		return application, nil, err
	}
	links := []model.ApplicationRepository{*link}
	status, message := model.ApplicationSyncSynced, "代码已是最新状态"
	var lastRun *model.PipelineRun
	found := false
	pullRequestSyncFailed := false
	configurationChanged := applicationConfigurationChangedSinceLastCheck(application)
	includePullRequests := false
	for i := range pollSources {
		if containsEvent(pollSources[i].Source.Config.Events, "pr") {
			includePullRequests = true
			break
		}
	}
	for linkIndex := range links {
		link := &links[linkIndex]
		refs, err := s.repositories.PollState(ctx, link.RepositoryID, includePullRequests)
		if err != nil {
			s.markSyncFailure(ctx, application.ID, err)
			return application, nil, fmt.Errorf("读取仓库“%s”失败: %w", link.Repository.Name, err)
		}
		if refs.PullRequestError != nil {
			pullRequestSyncFailed = true
			s.logger.Warn("同步代码仓库 PR/MR 元数据失败", "operation", "pipeline_sync_pull_requests", "application_id", application.ID, "repository_id", link.RepositoryID, "err", refs.PullRequestError)
		}
		if len(pollSources) == 0 {
			continue
		}
		linkHadBaseline := link.LastCheckedAt != nil || application.LastCheckedAt != nil
		observedByWatchKey := make(map[string]model.ApplicationRepositoryObservation, len(link.Observations))
		observedByEventRef := make(map[string]model.ApplicationRepositoryObservation, len(link.Observations))
		for i := range link.Observations {
			observation := link.Observations[i]
			observedByWatchKey[observation.WatchKey] = observation
			observedByEventRef[observationEventRefKey(observation.WorkflowID, observation.Event, observation.Ref)] = observation
		}
		seenWatchKeys := make([]string, 0)
		for _, binding := range pollSources {
			workflow, source := binding.Workflow, binding.Source
			for _, candidate := range workflowPollCandidates(source, refs) {
				if candidate.Commit == "" {
					continue
				}
				found = true
				watchKey := repositoryObservationWatchKey(workflow.ID, source.ID, candidate.Event, candidate.Ref)
				seenWatchKeys = append(seenWatchKeys, watchKey)
				observation, exists := observedByWatchKey[watchKey]
				if !exists {
					if _, sameRefExists := observedByEventRef[observationEventRefKey(workflow.ID, candidate.Event, candidate.Ref)]; sameRefExists {
						// 新增、复制或切换触发节点时，已有 Ref 只建立该节点自己的基线，
						// 不能因为节点 ID 变化而重复发布同一个 Commit。
						observation = model.ApplicationRepositoryObservation{
							ID: uuid.NewString(), ApplicationRepositoryID: link.ID, CreatedAt: now,
						}
						exists = true
					}
				}
				candidateAction := ""
				if candidate.Event == "pr" {
					remoteAction := normalizePullRequestAction(candidate.Action)
					switch {
					case remoteAction == "merged":
						candidateAction = "merged"
					case !exists:
						candidateAction = "opened"
					case observation.CommitSHA != candidate.Commit:
						candidateAction = "updated"
					default:
						candidateAction = normalizePullRequestAction(observation.Action)
						if candidateAction == "" || candidateAction == "merged" {
							candidateAction = "opened"
						}
					}
				}
				changed := exists && observation.CommitSHA != "" && observation.CommitSHA != candidate.Commit
				if candidate.Event == "pr" && exists && observation.CommitSHA != "" && candidateAction == "merged" &&
					normalizePullRequestAction(observation.Action) != "merged" {
					// Fast-forward 合并时 merge commit 可能等于 PR head；动作游标仍须识别
					// opened/updated -> merged 的状态变化。
					changed = true
				}
				if !exists && linkHadBaseline && !configurationChanged {
					// 完成过基线后出现的新分支、Tag 或 PR 都是新的远端事件。
					changed = true
				}
				if !exists {
					observation = model.ApplicationRepositoryObservation{
						ID: uuid.NewString(), ApplicationRepositoryID: link.ID, CreatedAt: now,
					}
				}
				observation.WatchKey, observation.WorkflowID, observation.SourceNodeID = watchKey, workflow.ID, source.ID
				observation.Event, observation.Action = candidate.Event, candidateAction
				observation.Ref, observation.CommitSHA = candidate.Ref, candidate.Commit
				observation.LastCheckedAt, observation.UpdatedAt = &now, now
				if err := s.db.WithContext(ctx).Save(&observation).Error; err != nil {
					return application, nil, fmt.Errorf("更新仓库监听状态失败: %w", err)
				}
				observedByWatchKey[watchKey] = observation
				observedByEventRef[observationEventRefKey(workflow.ID, candidate.Event, candidate.Ref)] = observation
				link.LastObservedRef, link.LastObservedCommit, link.LastCheckedAt = candidate.Ref, candidate.Commit, &now
				if err := s.db.WithContext(ctx).Model(&model.ApplicationRepository{}).Where("id = ?", link.ID).Updates(map[string]any{
					"last_observed_ref": candidate.Ref, "last_observed_commit": candidate.Commit, "last_checked_at": now, "updated_at": now,
				}).Error; err != nil {
					return application, nil, fmt.Errorf("更新应用仓库监听状态失败: %w", err)
				}
				if !changed {
					continue
				}
				if candidate.Event == "pr" && !containsEvent(source.Config.PRActions, candidateAction) {
					continue
				}
				candidate.Action = candidateAction
				status, message = model.ApplicationSyncChanged, pollChangeMessage(candidate.Event)
				run, createErr := s.createObservedRun(ctx, application, workflow, *link, source, candidate, polledRunTrigger(candidate.Event), message, now)
				if createErr != nil {
					return application, nil, createErr
				}
				if run != nil {
					lastRun = run
				}
			}
		}
		stale := s.db.WithContext(ctx).Where("application_repository_id = ?", link.ID)
		if len(seenWatchKeys) > 0 {
			stale = stale.Where("watch_key NOT IN ?", seenWatchKeys)
		}
		// 最近合并列表是有界窗口。合并记录离开窗口后仍保留动作游标，避免旧 PR
		// 因评论等原因重新进入“最近更新”列表时被误当作新的合并事件。
		stale = stale.Where("NOT (event = ? AND action = ?)", "pr", "merged")
		if refs.PullRequestError != nil {
			// API 暂时不可用不代表远端 PR 已删除；保留全部 PR 游标，避免恢复后重放。
			stale = stale.Where("event <> ?", "pr")
		}
		if err := stale.Delete(&model.ApplicationRepositoryObservation{}).Error; err != nil {
			return application, nil, fmt.Errorf("清理失效仓库监听状态失败: %w", err)
		}
	}
	if len(pollSources) == 0 {
		message = "所有仓库均可读取；当前流水线没有可定时检查的分支、PR 或 Tag 规则"
		if err := s.markSyncReadable(ctx, application, now, message); err != nil {
			return application, nil, err
		}
		return application, nil, nil
	}
	if pullRequestSyncFailed {
		const partialFailure = "分支与 Tag 已检查；PR/MR 同步失败，请检查平台 API 令牌、仓库地址或网络"
		if status == model.ApplicationSyncChanged {
			message += "；PR/MR 同步失败，请检查平台 API 令牌、仓库地址或网络"
		} else {
			status, message = model.ApplicationSyncFailed, partialFailure
		}
	}
	if !found && !pullRequestSyncFailed {
		message = "所有仓库均可读取；未找到流水线配置的分支、PR 或 Tag"
		if err := s.markSyncReadable(ctx, application, now, message); err != nil {
			return application, nil, err
		}
		return application, nil, nil
	}
	updates := map[string]any{
		"last_checked_at": now, "sync_status": status,
		"sync_message": message, "updated_at": now,
	}
	if err := s.db.WithContext(ctx).Model(&model.Application{}).Where("id = ?", application.ID).Updates(updates).Error; err != nil {
		return application, nil, fmt.Errorf("更新应用监听状态失败: %w", err)
	}
	application.LastCheckedAt, application.SyncStatus, application.SyncMessage = &now, status, message
	return application, lastRun, nil
}

func repositoryObservationWatchKey(workflowID, sourceNodeID, event, ref string) string {
	digest := sha256.Sum256([]byte(workflowID + "\x00" + sourceNodeID + "\x00" + event + "\x00" + ref))
	return fmt.Sprintf("%x", digest[:])
}

func observationEventRefKey(workflowID, event, ref string) string {
	return workflowID + "\x00" + event + "\x00" + ref
}

func pipelineCandidateCheckoutRef(candidate workflowPollCandidate) string {
	if candidate.Event == "pr" && normalizePullRequestAction(candidate.Action) == "merged" {
		targetBranch := strings.TrimSpace(strings.TrimPrefix(candidate.TargetBranch, "refs/heads/"))
		if targetBranch != "" {
			return "refs/heads/" + targetBranch
		}
	}
	return candidate.Ref
}

func applicationConfigurationChangedSinceLastCheck(application *model.Application) bool {
	if application.LastCheckedAt == nil {
		return false
	}
	if application.UpdatedAt.After(*application.LastCheckedAt) {
		return true
	}
	for i := range application.Workflows {
		if application.Workflows[i].UpdatedAt.After(*application.LastCheckedAt) {
			return true
		}
	}
	return false
}

func pollChangeMessage(event string) string {
	return map[string]string{
		"push": "定时检查检测到分支变更",
		"pr":   "定时检查检测到 PR 变更",
		"tag":  "定时检查检测到新 Tag",
	}[event]
}

func (s *Service) resolveCommitMessage(ctx context.Context, repositoryID, ref, commitSHA string) string {
	if repositoryID == "" || ref == "" || commitSHA == "" {
		return ""
	}
	message, err := s.repositories.CommitMessage(ctx, repositoryID, ref, commitSHA)
	if err != nil {
		s.logger.Warn("读取流水线提交说明失败", "operation", "pipeline_commit_message", "repository_id", repositoryID, "ref", ref, "commit_sha", commitSHA, "err", err)
		return ""
	}
	return strings.TrimSpace(message)
}

func (s *Service) createObservedRun(
	ctx context.Context,
	application *model.Application,
	workflow *model.ReleaseWorkflow,
	link model.ApplicationRepository,
	source model.WorkflowNode,
	candidate workflowPollCandidate,
	trigger, message string,
	now time.Time,
) (*model.PipelineRun, error) {
	run, err := s.runFromSource(ctx, application, workflow, source, trigger, candidate.Ref, candidate.Commit, "system", message, now)
	if err != nil {
		return nil, err
	}
	setRepositoryEventMetadata(run, candidate.Event, candidate.Ref, candidate.Commit, candidate.SourceBranch, candidate.TargetBranch, candidate.Action)
	run.CommitMessage = s.resolveCommitMessage(ctx, link.RepositoryID, pipelineCandidateCheckoutRef(candidate), candidate.Commit)
	component := pipelineRunRepositoryForLink(run.ID, link, candidate.Ref, candidate.Commit, now)
	created := false
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(run)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		created = true
		return tx.Create(&component).Error
	}); err != nil {
		return nil, fmt.Errorf("记录代码变更失败: %w", err)
	}
	if !created {
		return nil, nil
	}
	run.Repositories = []model.PipelineRunRepository{component}
	if workflow == nil || !workflow.IsActive {
		return run, nil
	}
	advanced, err := s.AdvanceRun(ctx, run.ID, "system", "")
	if err == nil {
		return advanced, nil
	}
	const failureMessage = "检测到代码变化，但自动执行流水线失败"
	_ = s.failCurrentExecution(ctx, run.ID, failureMessage, err)
	return nil, fmt.Errorf("自动执行流水线失败: %w", err)
}

func setRepositoryEventMetadata(run *model.PipelineRun, event, ref, commit, sourceBranch, targetBranch, action string) {
	if run == nil {
		return
	}
	event = strings.TrimSpace(event)
	ref = strings.TrimSpace(ref)
	commit = strings.TrimSpace(commit)
	action = normalizePullRequestAction(action)
	sourceBranch = strings.TrimSpace(sourceBranch)
	targetBranch = strings.TrimSpace(targetBranch)
	if event == "push" && targetBranch == "" {
		targetBranch = strings.TrimPrefix(ref, "refs/heads/")
	}
	run.TriggerAction = action
	run.SourceBranch = sourceBranch
	run.TargetBranch = targetBranch
	key := repositoryEventDedupKey(run.ApplicationID, run.WorkflowID, event, ref, commit, targetBranch, action)
	run.EventDedupKey = &key
}

func repositoryEventDedupKey(applicationID, workflowID, event, ref, commit, targetBranch, action string) string {
	category, identity := event, ref
	switch event {
	case "push":
		category, identity = "branch_commit", strings.TrimPrefix(ref, "refs/heads/")
	case "pr":
		if normalizePullRequestAction(action) == "merged" {
			category, identity = "branch_commit", targetBranch
		} else {
			category = "pr_commit"
		}
	case "tag":
		category = "tag_commit"
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(applicationID), strings.TrimSpace(workflowID), strings.TrimSpace(category), strings.TrimSpace(identity), strings.TrimSpace(commit),
	}, "\x00")))
	return fmt.Sprintf("%x", digest[:])
}

func pipelineRunRepositoryForLink(runID string, link model.ApplicationRepository, ref, commit string, now time.Time) model.PipelineRunRepository {
	return model.PipelineRunRepository{
		ID: uuid.NewString(), PipelineRunID: runID, RepositoryID: link.RepositoryID,
		SortOrder: 0, Ref: ref, CommitSHA: commit,
		Status: model.PipelineRunRepositoryReady, CreatedAt: now, UpdatedAt: now,
	}
}

func (s *Service) HandleRepositoryEvent(ctx context.Context, input repository.WebhookTaskPayload) error {
	var linked []model.ApplicationRepository
	if err := s.db.WithContext(ctx).Where("repository_id = ?", input.RepositoryID).Find(&linked).Error; err != nil {
		return fmt.Errorf("查询 Webhook 关联应用失败: %w", err)
	}
	for i := range linked {
		application, err := s.FindApplication(ctx, linked[i].ApplicationID)
		if err != nil {
			return err
		}
		if !application.IsActive || application.RepositoryID != input.RepositoryID {
			continue
		}
		var applicationRepository *model.ApplicationRepository
		for j := range application.Repositories {
			if application.Repositories[j].RepositoryID == input.RepositoryID {
				applicationRepository = &application.Repositories[j]
				break
			}
		}
		if applicationRepository == nil {
			continue
		}
		event := workflowEventName(input.EventType)
		matchRef := input.Ref
		if event == "pr" {
			if strings.TrimSpace(input.TargetBranch) == "" {
				continue
			}
			matchRef = "refs/heads/" + strings.TrimSpace(input.TargetBranch)
		}
		sources := applicationEventSources(
			application, event, matchRef, input.SourceBranch, input.TargetBranch, input.Action,
		)
		if len(sources) == 0 || input.CommitSHA == "" {
			continue
		}
		now := time.Now().UTC()
		eventAction := ""
		if event == "pr" {
			eventAction = normalizePullRequestAction(input.Action)
		}
		createdRunIDs := make([]string, 0, len(sources))
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			applicationUpdates := map[string]any{
				"last_checked_at": now, "sync_status": model.ApplicationSyncChanged,
				"sync_message": "Webhook 检测到新的代码版本", "updated_at": now,
			}
			if err := tx.Model(&model.Application{}).Where("id = ?", application.ID).Updates(applicationUpdates).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.ApplicationRepository{}).Where("id = ?", applicationRepository.ID).Updates(map[string]any{
				"last_observed_ref": input.Ref, "last_observed_commit": input.CommitSHA,
				"last_checked_at": now, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			for _, binding := range sources {
				workflow, source := binding.Workflow, binding.Source
				var observation model.ApplicationRepositoryObservation
				watchKey := repositoryObservationWatchKey(workflow.ID, source.ID, event, input.Ref)
				err := tx.First(&observation, "application_repository_id = ? AND watch_key = ?", applicationRepository.ID, watchKey).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if err == nil && observation.CommitSHA == input.CommitSHA && observation.Action == eventAction {
					continue
				}
				if errors.Is(err, gorm.ErrRecordNotFound) {
					observation = model.ApplicationRepositoryObservation{
						ID: uuid.NewString(), ApplicationRepositoryID: applicationRepository.ID,
						WatchKey: watchKey, WorkflowID: workflow.ID, SourceNodeID: source.ID, Event: event, CreatedAt: now,
					}
				}
				observation.WatchKey, observation.WorkflowID, observation.SourceNodeID, observation.Event = watchKey, workflow.ID, source.ID, event
				observation.Action = eventAction
				observation.Ref, observation.CommitSHA, observation.LastCheckedAt, observation.UpdatedAt = input.Ref, input.CommitSHA, &now, now
				if err := tx.Save(&observation).Error; err != nil {
					return err
				}
				run, err := s.runFromSourceWithDatabase(ctx, tx, application, workflow, source, input.EventType, input.Ref, input.CommitSHA, "", "Webhook 检测到新的代码版本", now)
				if err != nil {
					return err
				}
				setRepositoryEventMetadata(run, event, input.Ref, input.CommitSHA, input.SourceBranch, input.TargetBranch, input.Action)
				run.CommitMessage = strings.TrimSpace(strings.SplitN(input.Message, "\n", 2)[0])
				result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(run)
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected == 0 {
					continue
				}
				component := pipelineRunRepositoryForLink(run.ID, *applicationRepository, input.Ref, input.CommitSHA, now)
				if err := tx.Create(&component).Error; err != nil {
					return err
				}
				createdRunIDs = append(createdRunIDs, run.ID)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("更新 Webhook 关联应用失败: %w", err)
		}
		for _, runID := range createdRunIDs {
			if _, err := s.AdvanceRun(ctx, runID, "system", ""); err != nil {
				const failureMessage = "Webhook 已创建流水线运行，但自动执行失败"
				_ = s.failCurrentExecution(ctx, runID, failureMessage, err)
				return fmt.Errorf("Webhook 自动执行流水线失败: %w", err)
			}
		}
	}
	return nil
}

func (s *Service) RunWatcher(ctx context.Context, scanInterval time.Duration) {
	scanInterval = repositoryWatcherScanInterval(scanInterval)
	s.scanDueApplications(ctx)
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scanDueApplications(ctx)
		}
	}
}

func validPollIntervalSeconds(value int) bool {
	switch value {
	case 3, 5, 10, 60:
		return true
	default:
		return false
	}
}

func repositoryWatcherScanInterval(configured time.Duration) time.Duration {
	const maximumScanInterval = 3 * time.Second
	if configured <= 0 || configured > maximumScanInterval {
		return maximumScanInterval
	}
	return configured
}

func (s *Service) scanDueApplications(ctx context.Context) {
	var applications []model.Application
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).Limit(200).Find(&applications).Error; err != nil {
		return
	}
	now := time.Now().UTC()
	for i := range applications {
		application := &applications[i]
		if application.LastCheckedAt != nil && now.Sub(*application.LastCheckedAt) < time.Duration(application.PollIntervalSeconds)*time.Second {
			continue
		}
		if _, _, err := s.SyncApplication(ctx, application.ID, "poll"); err != nil && s.logger != nil {
			s.logger.Error("定时检查应用代码失败", "operation", "pipeline_poll", "application_id", application.ID, "err", err)
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func (s *Service) markSyncFailure(ctx context.Context, applicationID string, syncErr error) {
	now := time.Now().UTC()
	message := syncErr.Error()
	if utf8.RuneCountInString(message) > 255 {
		message = string([]rune(message)[:255])
	}
	_ = s.db.WithContext(ctx).Model(&model.Application{}).Where("id = ?", applicationID).Updates(map[string]any{
		"sync_status": model.ApplicationSyncFailed, "sync_message": message,
		"last_checked_at": now, "updated_at": now,
	}).Error
}

func (s *Service) markSyncReadable(ctx context.Context, application *model.Application, now time.Time, message string) error {
	if err := s.db.WithContext(ctx).Model(&model.Application{}).Where("id = ?", application.ID).Updates(map[string]any{
		"sync_status": model.ApplicationSyncSynced, "sync_message": message,
		"last_checked_at": now, "updated_at": now,
	}).Error; err != nil {
		return fmt.Errorf("更新应用检查状态失败: %w", err)
	}
	application.SyncStatus = model.ApplicationSyncSynced
	application.SyncMessage = message
	application.LastCheckedAt = &now
	return nil
}

func pipelineComplete(application *model.Application) bool {
	links := applicationRepositoryLinks(application)
	return pipelineExecutionConfigured(application) && len(links) == 1 && links[0].LastObservedCommit != ""
}

func pipelineExecutionConfigured(application *model.Application) bool {
	return pipelineExecutionConfiguredForWorkflow(application, application.Workflow)
}

func pipelineExecutionConfiguredForWorkflow(application *model.Application, workflow *model.ReleaseWorkflow) bool {
	if !applicationRepositoriesComplete(application) || workflow == nil || !workflow.IsActive {
		return false
	}
	return workflow.SchemaVersion == model.WorkflowSchemaVersion &&
		workflow.Source.Type == model.WorkflowNodeTrigger && workflowTaskCount(workflow.Stages) > 0
}

func pipelineExecutionIncompleteMessage(application *model.Application) string {
	return pipelineExecutionIncompleteMessageForWorkflow(application, application.Workflow)
}

func pipelineExecutionIncompleteMessageForWorkflow(application *model.Application, workflow *model.ReleaseWorkflow) string {
	missing := make([]string, 0, 4)
	if !applicationRepositoriesComplete(application) {
		missing = append(missing, "代码仓库")
	}
	if workflow == nil || !workflow.IsActive {
		missing = append(missing, "已启用的流水线")
	} else if workflowTaskCount(workflow.Stages) == 0 {
		missing = append(missing, "流水线任务")
	}
	if len(missing) == 0 {
		return ErrPipelineIncomplete.Error()
	}
	return "流水线不会执行：缺少" + strings.Join(missing, "、")
}

func pipelineIncompleteMessage(application *model.Application) string {
	missing := make([]string, 0, 4)
	if !applicationRepositoriesComplete(application) {
		missing = append(missing, "代码仓库")
	}
	if application.Workflow == nil || !application.Workflow.IsActive {
		missing = append(missing, "已启用的流水线")
	} else if workflowTaskCount(application.Workflow.Stages) == 0 {
		missing = append(missing, "流水线任务")
	}
	links := applicationRepositoryLinks(application)
	if len(links) != 1 || links[0].LastObservedCommit == "" {
		missing = append(missing, "代码版本")
	}
	if len(missing) == 0 {
		return ErrPipelineIncomplete.Error()
	}
	return "缺少：" + strings.Join(missing, "、")
}

func applicationRepositoriesComplete(application *model.Application) bool {
	links := applicationRepositoryLinks(application)
	return application.RepositoryID != "" && application.Repository.ID != "" && len(links) == 1
}

func validResourceName(name string) bool { return resourceNamePattern.MatchString(name) }

func validRegistryProvider(provider model.RegistryProvider) bool {
	return provider == model.RegistryGeneric || provider == model.RegistryHarbor || provider == model.RegistryDockerHub
}

func validRegistryNamespace(namespace string) bool {
	if namespace == "" {
		return true
	}
	_, err := distributionreference.ParseNamed("registry.invalid/" + namespace)
	return err == nil
}

func matchTag(pattern, tag string) bool {
	if pattern == "" {
		return true
	}
	matched, err := path.Match(pattern, tag)
	return err == nil && matched
}
