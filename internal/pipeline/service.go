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

	"zrt/internal/deployment"
	"zrt/internal/dockerengine"
	"zrt/internal/model"
	"zrt/internal/repository"
	"zrt/internal/secret"
)

var (
	ErrInvalidApplication       = errors.New("应用配置无效")
	ErrApplicationExists        = errors.New("应用名称已存在")
	ErrApplicationNotFound      = errors.New("应用不存在")
	ErrInvalidBuildPlan         = errors.New("构建方案配置无效")
	ErrBuildPlanExists          = errors.New("构建方案名称已存在")
	ErrInvalidRegistry          = errors.New("镜像仓库配置无效")
	ErrInvalidRegistryName      = errors.New("镜像仓库名称格式无效")
	ErrInvalidRegistryProvider  = errors.New("镜像仓库类型无效")
	ErrInvalidRegistryEndpoint  = errors.New("镜像仓库地址格式无效")
	ErrInsecureRegistryEndpoint = errors.New("HTTP 镜像仓库需要显式允许不安全连接")
	ErrInvalidRegistryNamespace = errors.New("镜像仓库命名空间格式无效")
	ErrInvalidRegistryUsername  = errors.New("镜像仓库用户名过长")
	ErrInvalidRegistrySecret    = errors.New("镜像仓库密码或 Token 过长")
	ErrRegistryLoginFailed      = errors.New("镜像仓库登录失败，请检查用户名和密码或 Token")
	ErrRegistryConnectionFailed = errors.New("无法连接镜像仓库，请检查地址和网络")
	ErrRegistryExists           = errors.New("镜像仓库名称已存在")
	ErrInvalidDeploymentPlan    = errors.New("部署方案配置无效")
	ErrDeploymentPlanExists     = errors.New("部署方案名称已存在")
	ErrDeploymentPlanNotFound   = errors.New("部署方案不存在")
	ErrWorkflowTemplateNotFound = errors.New("流水线方案不存在或未启用")
	ErrPipelineIncomplete       = errors.New("应用尚未绑定完整的构建与发布流程")
)

var resourceNamePattern = regexp.MustCompile(`^[A-Za-z0-9\p{Han}][A-Za-z0-9\p{Han}_. -]{0,127}$`)

// 镜像仓库名称是显示标签，也允许管理员直接使用 host/namespace 形式命名。
var registryNamePattern = regexp.MustCompile(`^[A-Za-z0-9\p{Han}][A-Za-z0-9\p{Han}_. /:-]{0,127}$`)

type ApplicationInput struct {
	Name                string
	Description         string
	RepositoryID        string
	Branch              string
	PollEnabled         bool
	PollIntervalSeconds int
	WatchPush           bool
	WatchPullRequest    bool
	WatchTags           bool
	TagPattern          string
	BuildPlanID         string
	ImageRegistryID     string
	ImageRegistrySet    bool
	DeploymentPlanID    string
	DeploymentTargetID  string
	WorkflowTemplateID  string
	Environments        []EnvironmentInput
}

type EnvironmentInput struct {
	Key                string
	Name               string
	Branch             string
	PollEnabled        bool
	WatchPush          bool
	WatchPullRequest   bool
	WatchTags          bool
	TagPattern         string
	DeploymentPlanID   string
	DeploymentTargetID string
	SortOrder          int
}

type BuildPlanInput struct {
	Name           string
	Kind           model.BuildPlanKind
	Description    string
	Script         string
	DockerfilePath string
	ContextPath    string
	ArtifactPath   string
	TimeoutSeconds int
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
	Name           string
	Kind           model.DeploymentPlanKind
	Description    string
	Script         string
	HelmChart      string
	HelmValues     string
	ComposeFile    string
	ServiceName    string
	TimeoutSeconds int
}

type Service struct {
	db           *gorm.DB
	repositories *repository.Service
	secrets      *secret.Manager
	docker       *dockerengine.Service
	deployments  *deployment.Service
	logger       *slog.Logger
}

func NewService(db *gorm.DB, repositories *repository.Service, secrets *secret.Manager) *Service {
	return &Service{db: db, repositories: repositories, secrets: secrets, logger: slog.Default()}
}

func (s *Service) ConfigureExecution(docker *dockerengine.Service, deployments *deployment.Service, logger *slog.Logger) {
	s.docker, s.deployments = docker, deployments
	if logger != nil {
		s.logger = logger
	}
}

func (s *Service) ListApplications(ctx context.Context) ([]model.Application, error) {
	var applications []model.Application
	err := s.db.WithContext(ctx).
		Preload("Repository").Preload("BuildPlan").Preload("ImageRegistry").
		Preload("DeploymentPlan").Preload("DeploymentTarget").
		Preload("WorkflowTemplate").
		Preload("Environments", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Preload("Environments.DeploymentPlan").Preload("Environments.DeploymentTarget").
		Preload("Workflow").
		Preload("Repositories", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Preload("Repositories.Observations").
		Preload("Repositories.Repository").
		Order("name ASC").Find(&applications).Error
	if err != nil {
		return nil, fmt.Errorf("查询应用失败: %w", err)
	}
	return applications, nil
}

func (s *Service) FindApplication(ctx context.Context, id string) (*model.Application, error) {
	var application model.Application
	err := s.db.WithContext(ctx).
		Preload("Repository").Preload("BuildPlan").Preload("ImageRegistry").
		Preload("DeploymentPlan").Preload("DeploymentTarget").
		Preload("WorkflowTemplate").
		Preload("Environments", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Preload("Environments.DeploymentPlan").Preload("Environments.DeploymentTarget").
		Preload("Workflow").
		Preload("Repositories", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Preload("Repositories.Observations").
		Preload("Repositories.Repository").
		First(&application, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrApplicationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询应用失败: %w", err)
	}
	return &application, nil
}

func (s *Service) CreateApplication(ctx context.Context, actorID string, input ApplicationInput) (*model.Application, error) {
	input, err := s.normalizeApplication(ctx, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	application := &model.Application{
		ID: uuid.NewString(), Name: input.Name, Description: input.Description,
		RepositoryID: input.RepositoryID, Branch: input.Branch,
		PollEnabled: input.PollEnabled, PollIntervalSeconds: input.PollIntervalSeconds,
		WatchPush: input.WatchPush, WatchPullRequest: input.WatchPullRequest,
		WatchTags: input.WatchTags, TagPattern: input.TagPattern,
		BuildPlanID: input.BuildPlanID, ImageRegistryID: input.ImageRegistryID,
		DeploymentPlanID: input.DeploymentPlanID, DeploymentTargetID: input.DeploymentTargetID,
		WorkflowTemplateID: input.WorkflowTemplateID,
		SyncStatus:         model.ApplicationSyncIdle, IsActive: true,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	environments := buildEnvironmentModels(application.ID, input.Environments, now)
	repositoryLink := buildApplicationRepositoryModel(application.ID, input.RepositoryID, now)
	application.Environments = environments
	workflow, err := s.newApplicationWorkflow(ctx, application, actorID, now)
	if err != nil {
		return nil, err
	}
	application.Environments = nil
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(application).Error; err != nil {
			return err
		}
		if err := tx.Create(&environments).Error; err != nil {
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
	// 旧版更新请求不会回传应用级方案。保留已有绑定，避免只改说明时意外清空可执行配置。
	if strings.TrimSpace(input.BuildPlanID) == "" {
		input.BuildPlanID = existing.BuildPlanID
	}
	if !input.ImageRegistrySet && strings.TrimSpace(input.ImageRegistryID) == "" {
		input.ImageRegistryID = existing.ImageRegistryID
	}
	if strings.TrimSpace(input.DeploymentPlanID) == "" {
		input.DeploymentPlanID = existing.DeploymentPlanID
	}
	if strings.TrimSpace(input.DeploymentTargetID) == "" {
		input.DeploymentTargetID = existing.DeploymentTargetID
	}
	requestedTemplateID := strings.TrimSpace(input.WorkflowTemplateID)
	reuseTemplateSnapshot := existing.WorkflowTemplateID != "" && (requestedTemplateID == "" || requestedTemplateID == existing.WorkflowTemplateID)
	if reuseTemplateSnapshot {
		input.WorkflowTemplateID = ""
		input.Environments = environmentModelInputs(existing.Environments)
	}
	input, err = s.normalizeApplication(ctx, input)
	if err != nil {
		return nil, err
	}
	if reuseTemplateSnapshot {
		input.WorkflowTemplateID = existing.WorkflowTemplateID
	}
	updates := map[string]any{
		"name": input.Name, "description": input.Description, "repository_id": input.RepositoryID,
		"branch": input.Branch, "poll_enabled": input.PollEnabled,
		"poll_interval_seconds": input.PollIntervalSeconds, "watch_push": input.WatchPush,
		"watch_pull_request": input.WatchPullRequest, "watch_tags": input.WatchTags,
		"tag_pattern": input.TagPattern, "build_plan_id": input.BuildPlanID,
		"image_registry_id": input.ImageRegistryID, "release_plan_id": input.DeploymentPlanID,
		"deployment_target_id": input.DeploymentTargetID,
		"workflow_template_id": input.WorkflowTemplateID,
		"updated_at":           time.Now().UTC(),
	}
	if existing.RepositoryID != input.RepositoryID || existing.Branch != input.Branch {
		updates["last_observed_ref"] = ""
		updates["last_observed_commit"] = ""
		updates["sync_status"] = model.ApplicationSyncIdle
		updates["sync_message"] = ""
		updates["last_checked_at"] = nil
	}
	var replacementWorkflow *model.ReleaseWorkflow
	if existing.WorkflowTemplateID != input.WorkflowTemplateID && input.WorkflowTemplateID != "" {
		updated := *existing
		updated.Name = input.Name
		updated.WorkflowTemplateID = input.WorkflowTemplateID
		updated.Environments = buildEnvironmentModels(existing.ID, input.Environments, time.Now().UTC())
		replacementWorkflow, err = s.newApplicationWorkflow(ctx, &updated, existing.CreatedBy, time.Now().UTC())
		if err != nil {
			return nil, err
		}
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Application{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
			return err
		}
		if err := saveApplicationEnvironments(tx, existing, input.Environments, time.Now().UTC()); err != nil {
			return err
		}
		if err := saveApplicationRepository(tx, existing, input.RepositoryID, time.Now().UTC()); err != nil {
			return err
		}
		if replacementWorkflow != nil {
			if err := tx.Where("application_id = ?", existing.ID).Delete(&model.ReleaseWorkflow{}).Error; err != nil {
				return err
			}
			return tx.Create(replacementWorkflow).Error
		}
		if !reuseTemplateSnapshot && workflowInputsChanged(existing, input) {
			return tx.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", existing.ID).
				Updates(map[string]any{"is_active": false, "updated_at": time.Now().UTC()}).Error
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
	input.Branch = strings.TrimSpace(input.Branch)
	input.TagPattern = strings.TrimSpace(input.TagPattern)
	input.BuildPlanID = strings.TrimSpace(input.BuildPlanID)
	input.ImageRegistryID = strings.TrimSpace(input.ImageRegistryID)
	input.DeploymentPlanID = strings.TrimSpace(input.DeploymentPlanID)
	input.DeploymentTargetID = strings.TrimSpace(input.DeploymentTargetID)
	input.WorkflowTemplateID = strings.TrimSpace(input.WorkflowTemplateID)
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
		input.Environments = workflowTemplateEnvironmentInputs(template.Nodes)
	}
	legacyInput := len(input.Environments) == 0
	if legacyInput {
		input.Environments = []EnvironmentInput{{
			Key: "dev", Name: "开发环境", Branch: input.Branch,
			PollEnabled: input.PollEnabled, WatchPush: input.WatchPush,
			WatchPullRequest: input.WatchPullRequest, WatchTags: input.WatchTags,
			TagPattern: input.TagPattern, DeploymentPlanID: input.DeploymentPlanID,
			DeploymentTargetID: input.DeploymentTargetID,
		}}
	}
	if !validResourceName(input.Name) || utf8.RuneCountInString(input.Description) > 500 || input.RepositoryID == "" ||
		utf8.RuneCountInString(input.Branch) > 255 || utf8.RuneCountInString(input.TagPattern) > 255 {
		return input, ErrInvalidApplication
	}
	if legacyInput && !input.PollEnabled && !input.WatchPush && !input.WatchPullRequest && !input.WatchTags {
		return input, ErrInvalidApplication
	}
	if input.PollIntervalSeconds == 0 {
		input.PollIntervalSeconds = 3
	}
	if input.WatchTags && input.TagPattern != "" {
		if _, err := path.Match(input.TagPattern, "v1.0.0"); err != nil {
			return input, ErrInvalidApplication
		}
	}
	repo, err := s.repositories.Find(ctx, input.RepositoryID)
	if err != nil || !repo.IsActive {
		return input, ErrInvalidApplication
	}
	if input.Branch == "" {
		input.Branch = repo.DefaultBranch
		if input.Branch == "" {
			input.Branch = "main"
		}
	}
	checks := []struct {
		id    string
		model any
	}{
		{input.BuildPlanID, &model.BuildPlan{}},
		{input.ImageRegistryID, &model.ImageRegistry{}},
		{input.DeploymentPlanID, &model.DeploymentPlan{}},
		{input.DeploymentTargetID, &model.DeploymentTarget{}},
	}
	for _, check := range checks {
		if check.id == "" {
			continue
		}
		var count int64
		if err := s.db.WithContext(ctx).Model(check.model).Where("id = ? AND is_active = ?", check.id, true).Count(&count).Error; err != nil || count != 1 {
			return input, ErrInvalidApplication
		}
	}
	seenEnvironments := make(map[string]struct{}, len(input.Environments))
	hasTrigger, hasPollableSource := false, false
	for i := range input.Environments {
		environment := &input.Environments[i]
		environment.Key = strings.ToLower(strings.TrimSpace(environment.Key))
		environment.Name = strings.TrimSpace(environment.Name)
		environment.Branch = strings.TrimSpace(environment.Branch)
		environment.TagPattern = strings.TrimSpace(environment.TagPattern)
		// 部署方案属于应用。环境表继续保留旧字段用于无损迁移，但不能形成第二套配置来源。
		environment.DeploymentPlanID = input.DeploymentPlanID
		environment.DeploymentTargetID = strings.TrimSpace(environment.DeploymentTargetID)
		if _, exists := seenEnvironments[environment.Key]; exists || !validEnvironmentKey(environment.Key) {
			return input, ErrInvalidApplication
		}
		seenEnvironments[environment.Key] = struct{}{}
		if environment.Name == "" {
			environment.Name = environmentName(environment.Key)
		}
		if environment.Branch == "" {
			environment.Branch = defaultEnvironmentBranch(environment.Key, input.Branch)
		}
		if utf8.RuneCountInString(environment.Name) > 64 || utf8.RuneCountInString(environment.Branch) > 255 ||
			utf8.RuneCountInString(environment.TagPattern) > 255 {
			return input, ErrInvalidApplication
		}
		if environment.WatchTags && environment.TagPattern != "" {
			if _, err := path.Match(environment.TagPattern, "v1.0.0"); err != nil {
				return input, ErrInvalidApplication
			}
		}
		if environment.PollEnabled || environment.WatchPush || environment.WatchPullRequest || environment.WatchTags {
			hasTrigger = true
		}
		if environment.PollEnabled || environment.WatchPush || environment.WatchPullRequest || environment.WatchTags {
			hasPollableSource = true
		}
		for _, check := range []struct {
			id    string
			model any
		}{{environment.DeploymentPlanID, &model.DeploymentPlan{}}, {environment.DeploymentTargetID, &model.DeploymentTarget{}}} {
			if check.id == "" {
				continue
			}
			var count int64
			if err := s.db.WithContext(ctx).Model(check.model).Where("id = ? AND is_active = ?", check.id, true).Count(&count).Error; err != nil || count != 1 {
				return input, ErrInvalidApplication
			}
		}
	}
	if len(input.Environments) > 4 {
		return input, ErrInvalidApplication
	}
	if !hasTrigger || (hasPollableSource && !validPollIntervalSeconds(input.PollIntervalSeconds)) {
		return input, ErrInvalidApplication
	}
	primary := input.Environments[0]
	input.Branch, input.PollEnabled = primary.Branch, primary.PollEnabled
	input.WatchPush, input.WatchPullRequest = primary.WatchPush, primary.WatchPullRequest
	input.WatchTags, input.TagPattern = primary.WatchTags, primary.TagPattern
	if input.DeploymentTargetID == "" {
		input.DeploymentTargetID = primary.DeploymentTargetID
	}
	return input, nil
}

func buildEnvironmentModels(applicationID string, inputs []EnvironmentInput, now time.Time) []model.ApplicationEnvironment {
	result := make([]model.ApplicationEnvironment, 0, len(inputs))
	for i := range inputs {
		result = append(result, model.ApplicationEnvironment{
			ID: uuid.NewString(), ApplicationID: applicationID, Key: inputs[i].Key,
			Name: inputs[i].Name, Branch: inputs[i].Branch, PollEnabled: inputs[i].PollEnabled,
			WatchPush: inputs[i].WatchPush, WatchPullRequest: inputs[i].WatchPullRequest,
			WatchTags: inputs[i].WatchTags, TagPattern: inputs[i].TagPattern,
			DeploymentPlanID: inputs[i].DeploymentPlanID, DeploymentTargetID: inputs[i].DeploymentTargetID,
			SortOrder: i, CreatedAt: now, UpdatedAt: now,
		})
	}
	return result
}

func buildApplicationRepositoryModel(applicationID, repositoryID string, now time.Time) model.ApplicationRepository {
	return model.ApplicationRepository{
		ID: uuid.NewString(), ApplicationID: applicationID, RepositoryID: repositoryID,
		SortOrder: 0, CreatedAt: now, UpdatedAt: now,
	}
}

func saveApplicationRepository(tx *gorm.DB, application *model.Application, repositoryID string, now time.Time) error {
	for i := range application.Repositories {
		if application.Repositories[i].RepositoryID == repositoryID {
			return tx.Model(&model.ApplicationRepository{}).Where("id = ?", application.Repositories[i].ID).
				Updates(map[string]any{"sort_order": 0, "updated_at": now}).Error
		}
	}
	repository := buildApplicationRepositoryModel(application.ID, repositoryID, now)
	return tx.Create(&repository).Error
}

func environmentModelInputs(environments []model.ApplicationEnvironment) []EnvironmentInput {
	result := make([]EnvironmentInput, 0, len(environments))
	for i := range environments {
		result = append(result, EnvironmentInput{
			Key: environments[i].Key, Name: environments[i].Name, Branch: environments[i].Branch,
			PollEnabled: environments[i].PollEnabled, WatchPush: environments[i].WatchPush,
			WatchPullRequest: environments[i].WatchPullRequest, WatchTags: environments[i].WatchTags,
			TagPattern: environments[i].TagPattern, DeploymentPlanID: environments[i].DeploymentPlanID,
			DeploymentTargetID: environments[i].DeploymentTargetID, SortOrder: environments[i].SortOrder,
		})
	}
	return result
}

func saveApplicationEnvironments(tx *gorm.DB, application *model.Application, inputs []EnvironmentInput, now time.Time) error {
	existingByKey := make(map[string]model.ApplicationEnvironment, len(application.Environments))
	for i := range application.Environments {
		existingByKey[application.Environments[i].Key] = application.Environments[i]
	}
	environments := buildEnvironmentModels(application.ID, inputs, now)
	keys := make([]string, 0, len(environments))
	for i := range environments {
		keys = append(keys, environments[i].Key)
		if existing, ok := existingByKey[environments[i].Key]; ok {
			environments[i].ID = existing.ID
			environments[i].CreatedAt = existing.CreatedAt
			if existing.Branch == environments[i].Branch && existing.WatchTags == environments[i].WatchTags && existing.TagPattern == environments[i].TagPattern {
				environments[i].LastObservedRef = existing.LastObservedRef
				environments[i].LastObservedCommit = existing.LastObservedCommit
				environments[i].LastCheckedAt = existing.LastCheckedAt
			}
		}
		if err := tx.Save(&environments[i]).Error; err != nil {
			return err
		}
	}
	return tx.Where("application_id = ? AND key NOT IN ?", application.ID, keys).
		Delete(&model.ApplicationEnvironment{}).Error
}

func validEnvironmentKey(value string) bool {
	return value == "dev" || value == "test" || value == "pre" || value == "prod"
}

func environmentName(value string) string {
	return map[string]string{"dev": "开发环境", "test": "测试环境", "pre": "预发布环境", "prod": "生产环境"}[value]
}

func defaultEnvironmentBranch(key, fallback string) string {
	if fallback == "" {
		fallback = "main"
	}
	return map[string]string{"dev": "dev", "test": "test", "pre": "main", "prod": "release"}[key]
}

func workflowInputsChanged(existing *model.Application, input ApplicationInput) bool {
	if existing.WorkflowTemplateID != input.WorkflowTemplateID || len(existing.Environments) != len(input.Environments) {
		return true
	}
	for i := range input.Environments {
		current := input.Environments[i]
		var found *model.ApplicationEnvironment
		for j := range existing.Environments {
			if existing.Environments[j].Key == current.Key {
				found = &existing.Environments[j]
				break
			}
		}
		if found == nil || found.Branch != current.Branch || found.PollEnabled != current.PollEnabled ||
			found.WatchPush != current.WatchPush || found.WatchPullRequest != current.WatchPullRequest ||
			found.WatchTags != current.WatchTags || found.TagPattern != current.TagPattern ||
			found.DeploymentPlanID != current.DeploymentPlanID || found.DeploymentTargetID != current.DeploymentTargetID {
			return true
		}
	}
	return false
}

func (s *Service) ListBuildPlans(ctx context.Context) ([]model.BuildPlan, error) {
	var plans []model.BuildPlan
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&plans).Error; err != nil {
		return nil, fmt.Errorf("查询构建方案失败: %w", err)
	}
	return plans, nil
}

func (s *Service) CreateBuildPlan(ctx context.Context, actorID string, input BuildPlanInput) (*model.BuildPlan, error) {
	input.Name, input.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
	input.Script, input.DockerfilePath = strings.TrimSpace(input.Script), strings.TrimSpace(input.DockerfilePath)
	input.ContextPath, input.ArtifactPath = strings.TrimSpace(input.ContextPath), strings.TrimSpace(input.ArtifactPath)
	if input.ContextPath == "" {
		input.ContextPath = "."
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 1800
	}
	validKind := (input.Kind == model.BuildPlanScript && input.Script != "") ||
		(input.Kind == model.BuildPlanDockerfile && input.DockerfilePath != "")
	if !validResourceName(input.Name) || !validKind || input.TimeoutSeconds < 30 || input.TimeoutSeconds > 7200 ||
		len(input.Script) > 256*1024 || utf8.RuneCountInString(input.Description) > 500 {
		return nil, ErrInvalidBuildPlan
	}
	now := time.Now().UTC()
	plan := &model.BuildPlan{
		ID: uuid.NewString(), Name: input.Name, Kind: input.Kind, Description: input.Description,
		Script: input.Script, DockerfilePath: input.DockerfilePath, ContextPath: input.ContextPath,
		ArtifactPath: input.ArtifactPath, TimeoutSeconds: input.TimeoutSeconds,
		IsActive: true, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(plan).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrBuildPlanExists
		}
		return nil, fmt.Errorf("创建构建方案失败: %w", err)
	}
	return plan, nil
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
		regclient.WithUserAgent("zrt"),
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

func (s *Service) ListDeploymentPlans(ctx context.Context) ([]model.DeploymentPlan, error) {
	var plans []model.DeploymentPlan
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&plans).Error; err != nil {
		return nil, fmt.Errorf("查询部署方案失败: %w", err)
	}
	return plans, nil
}

func normalizeDeploymentPlanInput(input DeploymentPlanInput) (DeploymentPlanInput, error) {
	input.Name, input.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
	input.Script, input.HelmChart = strings.TrimSpace(input.Script), strings.TrimSpace(input.HelmChart)
	input.ComposeFile, input.ServiceName = strings.TrimSpace(input.ComposeFile), strings.TrimSpace(input.ServiceName)
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 600
	}
	validKind := (input.Kind == model.DeploymentPlanScript && input.Script != "") ||
		(input.Kind == model.DeploymentPlanHelm && input.HelmChart != "") ||
		(input.Kind == model.DeploymentPlanCompose && input.ComposeFile != "") ||
		(input.Kind == model.DeploymentPlanDocker && input.ServiceName != "")
	if !validResourceName(input.Name) || !validKind || input.TimeoutSeconds < 30 || input.TimeoutSeconds > 3600 ||
		len(input.Script) > 256*1024 || len(input.HelmValues) > 512*1024 || utf8.RuneCountInString(input.Description) > 500 {
		return input, ErrInvalidDeploymentPlan
	}
	switch input.Kind {
	case model.DeploymentPlanScript:
		input.HelmChart, input.HelmValues, input.ComposeFile, input.ServiceName = "", "", "", ""
	case model.DeploymentPlanHelm:
		input.Script, input.ComposeFile, input.ServiceName = "", "", ""
	case model.DeploymentPlanCompose:
		input.Script, input.HelmChart, input.HelmValues, input.ServiceName = "", "", "", ""
	case model.DeploymentPlanDocker:
		input.Script, input.HelmChart, input.HelmValues, input.ComposeFile = "", "", "", ""
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
		Script: input.Script, HelmChart: input.HelmChart, HelmValues: input.HelmValues,
		ComposeFile: input.ComposeFile, ServiceName: input.ServiceName, TimeoutSeconds: input.TimeoutSeconds,
		IsActive: true, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(plan).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrDeploymentPlanExists
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
	if err := s.db.WithContext(ctx).First(&plan, "id = ?", strings.TrimSpace(id)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDeploymentPlanNotFound
		}
		return nil, fmt.Errorf("查询部署方案失败: %w", err)
	}
	plan.Name, plan.Kind, plan.Description = input.Name, input.Kind, input.Description
	plan.Script, plan.HelmChart, plan.HelmValues = input.Script, input.HelmChart, input.HelmValues
	plan.ComposeFile, plan.ServiceName = input.ComposeFile, input.ServiceName
	plan.TimeoutSeconds, plan.UpdatedAt = input.TimeoutSeconds, time.Now().UTC()
	if err := s.db.WithContext(ctx).Save(&plan).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrDeploymentPlanExists
		}
		return nil, fmt.Errorf("更新部署方案失败: %w", err)
	}
	return &plan, nil
}

func (s *Service) ListRuns(ctx context.Context, limit int) ([]model.PipelineRun, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	var runs []model.PipelineRun
	if err := s.db.WithContext(ctx).
		Preload("Application").Preload("Application.WorkflowTemplate").
		Preload("Repositories", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Preload("Repositories.Repository").Preload("Repositories.BuildPlan").Preload("Repositories.DeploymentPlan").
		Order("created_at DESC").Limit(limit).Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("查询流水线运行失败: %w", err)
	}
	for i := range runs {
		if runs[i].WorkflowSnapshot == "" {
			continue
		}
		var snapshot workflowSnapshot
		if json.Unmarshal([]byte(runs[i].WorkflowSnapshot), &snapshot) == nil {
			snapshot.ApprovalEnabled = workflowHasApprovalNode(snapshot.Nodes)
			runs[i].ApprovalRequired = snapshot.ApprovalEnabled
			for _, node := range snapshot.Nodes {
				if node.ID == runs[i].CurrentNodeID {
					runs[i].CurrentNodeName = node.Name
					break
				}
			}
		}
	}
	return runs, nil
}

// BackfillCommitMessages 为升级前创建的运行补齐提交标题。失败的仓库留待下次启动重试，
// 不影响 HTTP 服务和流水线消费者启动。
func (s *Service) BackfillCommitMessages(ctx context.Context, limit int) error {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	var runs []model.PipelineRun
	if err := s.db.WithContext(ctx).
		Preload("Repositories", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Where("commit_message = ? AND commit_sha <> ?", "", "").
		Order("created_at DESC").Limit(limit).Find(&runs).Error; err != nil {
		return fmt.Errorf("查询待补全提交说明的流水线运行失败: %w", err)
	}
	for i := range runs {
		if len(runs[i].Repositories) == 0 {
			continue
		}
		message, err := s.repositories.HistoricalCommitMessage(ctx, runs[i].Repositories[0].RepositoryID, runs[i].Ref, runs[i].CommitSHA)
		if err != nil {
			s.logger.Warn("读取历史流水线提交说明失败", "operation", "pipeline_commit_backfill", "repository_id", runs[i].Repositories[0].RepositoryID, "ref", runs[i].Ref, "commit_sha", runs[i].CommitSHA, "err", err)
			continue
		}
		message = strings.TrimSpace(message)
		if message == "" {
			continue
		}
		result := s.db.WithContext(ctx).Model(&model.PipelineRun{}).
			Where("id = ? AND commit_message = ?", runs[i].ID, "").
			Update("commit_message", message)
		if result.Error != nil {
			return fmt.Errorf("补全流水线提交说明失败: %w", result.Error)
		}
	}
	return nil
}

func (s *Service) PrepareRun(ctx context.Context, applicationID, actorID string) (*model.PipelineRun, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	if !pipelineExecutionConfigured(application) {
		return s.createBlockedRun(ctx, application, actorID, pipelineExecutionIncompleteMessage(application))
	}
	if application.Workflow != nil && application.Workflow.IsActive {
		var source *model.WorkflowNode
		manualSource := false
		for i := range application.Workflow.Nodes {
			if application.Workflow.Nodes[i].Type == model.WorkflowNodeManualRelease {
				source = &application.Workflow.Nodes[i]
				manualSource = true
				break
			}
			if source == nil && application.Workflow.Nodes[i].Type == model.WorkflowNodeTrigger {
				source = &application.Workflow.Nodes[i]
			}
		}
		if source == nil {
			return s.createBlockedRun(ctx, application, actorID, "应用流水线缺少代码触发或手动发布节点，请修正并重新启用流水线")
		}
		if manualSource {
			return s.createManualSelectionRun(ctx, application, actorID)
		}
		if application.LastObservedCommit == "" {
			return s.createBlockedRun(ctx, application, actorID, "尚未获取代码版本，请检查仓库并确认触发节点能匹配远端分支或 Tag")
		}
		now := time.Now().UTC()
		run, err := newWorkflowRun(
			application, application.Workflow, *source, "manual", application.LastObservedRef,
			application.LastObservedCommit, actorID, "流水线运行已启动", now,
		)
		if err != nil {
			return nil, err
		}
		links := applicationRepositoryLinks(application)
		run.CommitMessage = s.resolveCommitMessage(ctx, links[0].RepositoryID, run.Ref, run.CommitSHA)
		component := pipelineRunRepositoryForLink(application, run.ID, links[0], run.Ref, run.CommitSHA, now)
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(run).Error; err != nil {
				return err
			}
			return tx.Create(&component).Error
		}); err != nil {
			return nil, fmt.Errorf("创建流水线运行失败: %w", err)
		}
		run.Repositories = []model.PipelineRunRepository{component}
		return run, nil
	}
	status, message := model.PipelineRunReady, "构建与发布配置已就绪"
	if !pipelineComplete(application) {
		status, message = model.PipelineRunBlocked, pipelineIncompleteMessage(application)
	}
	now := time.Now().UTC()
	run := &model.PipelineRun{
		ID: uuid.NewString(), ApplicationID: application.ID, Trigger: "manual",
		Ref: application.LastObservedRef, CommitSHA: application.LastObservedCommit,
		Status: status, Stage: "configured", Message: message, CreatedBy: actorID,
		CreatedAt: now, UpdatedAt: now,
	}
	links := applicationRepositoryLinks(application)
	if len(links) > 0 {
		run.CommitMessage = s.resolveCommitMessage(ctx, links[0].RepositoryID, run.Ref, run.CommitSHA)
	}
	components := pipelineRunRepositories(application, run.ID, application.LastObservedRef, application.LastObservedCommit, now)
	for i := range components {
		if components[i].CommitSHA == "" {
			components[i].Status = model.PipelineRunRepositoryPending
		}
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
	if status == model.PipelineRunBlocked {
		return run, ErrPipelineIncomplete
	}
	return run, nil
}

func (s *Service) createBlockedRun(ctx context.Context, application *model.Application, actorID, message string) (*model.PipelineRun, error) {
	run, err := s.createPendingRun(ctx, application, actorID, message)
	if err != nil {
		return nil, err
	}
	return run, ErrPipelineIncomplete
}

func (s *Service) createManualSelectionRun(ctx context.Context, application *model.Application, actorID string) (*model.PipelineRun, error) {
	return s.createPendingRun(ctx, application, actorID, "请选择每个代码仓库要发布的 Commit")
}

func (s *Service) createPendingRun(ctx context.Context, application *model.Application, actorID, message string) (*model.PipelineRun, error) {
	now := time.Now().UTC()
	run := &model.PipelineRun{
		ID: uuid.NewString(), ApplicationID: application.ID, Trigger: "manual",
		Ref: "", CommitSHA: "",
		Status: model.PipelineRunBlocked, Stage: "configured", Message: message,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	components := pipelineRunRepositories(application, run.ID, "", "", now)
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
	links := applicationRepositoryLinks(application)
	if len(links) == 0 {
		return application, nil, ErrInvalidApplication
	}
	environmentByKey := make(map[string]*model.ApplicationEnvironment, len(application.Environments))
	for i := range application.Environments {
		environmentByKey[application.Environments[i].Key] = &application.Environments[i]
	}
	status, message := model.ApplicationSyncSynced, "代码已是最新状态"
	var lastRun *model.PipelineRun
	found := false
	configurationChanged := applicationConfigurationChangedSinceLastCheck(application)
	includePullRequests := false
	for i := range pollSources {
		if containsEvent(pollSources[i].Config.Events, "pr") {
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
		if len(pollSources) == 0 {
			continue
		}
		linkHadBaseline := link.LastCheckedAt != nil || application.LastCheckedAt != nil
		observedByWatchKey := make(map[string]model.ApplicationRepositoryObservation, len(link.Observations))
		observedByEventRef := make(map[string]model.ApplicationRepositoryObservation, len(link.Observations))
		legacyObservations := make([]model.ApplicationRepositoryObservation, 0)
		for i := range link.Observations {
			observation := link.Observations[i]
			if strings.HasPrefix(observation.WatchKey, "legacy:") || observation.WatchKey == "" {
				legacyObservations = append(legacyObservations, observation)
				continue
			}
			observedByWatchKey[observation.WatchKey] = observation
			observedByEventRef[observationEventRefKey(observation.Event, observation.Ref)] = observation
		}
		seenWatchKeys := make([]string, 0)
		sourceNodeIDs := make([]string, 0, len(pollSources))
		for _, source := range pollSources {
			sourceNodeIDs = append(sourceNodeIDs, source.ID)
			for _, candidate := range workflowPollCandidates(source, refs) {
				if candidate.Commit == "" {
					continue
				}
				found = true
				watchKey := repositoryObservationWatchKey(source.ID, candidate.Event, candidate.Ref)
				seenWatchKeys = append(seenWatchKeys, watchKey)
				observation, exists := observedByWatchKey[watchKey]
				if !exists {
					observation, exists = takeLegacyObservation(&legacyObservations, source.Config.Environment, candidate.Ref)
				}
				if !exists {
					if _, sameRefExists := observedByEventRef[observationEventRefKey(candidate.Event, candidate.Ref)]; sameRefExists {
						// 新增、复制或切换触发节点时，已有 Ref 只建立该节点自己的基线，
						// 不能因为节点 ID 变化而重复发布同一个 Commit。
						observation = model.ApplicationRepositoryObservation{
							ID: uuid.NewString(), ApplicationRepositoryID: link.ID, CreatedAt: now,
						}
						exists = true
					}
				}
				changed := exists && observation.CommitSHA != "" && observation.CommitSHA != candidate.Commit
				if !exists && linkHadBaseline && !configurationChanged {
					// 完成过基线后出现的新分支、Tag 或 PR 都是新的远端事件。
					changed = true
				}
				if !exists {
					observation = model.ApplicationRepositoryObservation{
						ID: uuid.NewString(), ApplicationRepositoryID: link.ID, CreatedAt: now,
					}
				}
				observation.WatchKey, observation.SourceNodeID = watchKey, source.ID
				observation.Event, observation.Environment = candidate.Event, source.Config.Environment
				observation.Ref, observation.CommitSHA = candidate.Ref, candidate.Commit
				observation.LastCheckedAt, observation.UpdatedAt = &now, now
				if err := s.db.WithContext(ctx).Save(&observation).Error; err != nil {
					return application, nil, fmt.Errorf("更新仓库监听状态失败: %w", err)
				}
				observedByWatchKey[watchKey] = observation
				observedByEventRef[observationEventRefKey(candidate.Event, candidate.Ref)] = observation
				link.LastObservedRef, link.LastObservedCommit, link.LastCheckedAt = candidate.Ref, candidate.Commit, &now
				if err := s.db.WithContext(ctx).Model(&model.ApplicationRepository{}).Where("id = ?", link.ID).Updates(map[string]any{
					"last_observed_ref": candidate.Ref, "last_observed_commit": candidate.Commit, "last_checked_at": now, "updated_at": now,
				}).Error; err != nil {
					return application, nil, fmt.Errorf("更新应用仓库监听状态失败: %w", err)
				}
				if linkIndex == 0 {
					application.LastObservedRef, application.LastObservedCommit = candidate.Ref, candidate.Commit
					if environment := environmentByKey[source.Config.Environment]; environment != nil {
						if err := s.db.WithContext(ctx).Model(environment).Updates(map[string]any{
							"last_observed_ref": candidate.Ref, "last_observed_commit": candidate.Commit, "last_checked_at": now, "updated_at": now,
						}).Error; err != nil {
							return application, nil, fmt.Errorf("更新环境监听状态失败: %w", err)
						}
					}
				}
				if !changed {
					continue
				}
				status, message = model.ApplicationSyncChanged, pollChangeMessage(candidate.Event)
				run, createErr := s.createObservedRun(ctx, application, *link, source, polledRunTrigger(candidate.Event), candidate.Ref, candidate.Commit, message, now)
				if createErr != nil {
					return application, nil, createErr
				}
				lastRun = run
			}
		}
		stale := s.db.WithContext(ctx).Where("application_repository_id = ? AND source_node_id IN ?", link.ID, sourceNodeIDs)
		if len(seenWatchKeys) > 0 {
			stale = stale.Where("watch_key NOT IN ?", seenWatchKeys)
		}
		if err := stale.Delete(&model.ApplicationRepositoryObservation{}).Error; err != nil {
			return application, nil, fmt.Errorf("清理失效仓库监听状态失败: %w", err)
		}
	}
	if len(pollSources) == 0 {
		message = "所有仓库均可读取；当前流水线没有可定时检查的分支、PR 或 Tag 节点"
		if err := s.markSyncReadable(ctx, application, now, message); err != nil {
			return application, nil, err
		}
		return application, nil, nil
	}
	if !found {
		message = "所有仓库均可读取；未找到流水线配置的分支、PR 或 Tag"
		if err := s.markSyncReadable(ctx, application, now, message); err != nil {
			return application, nil, err
		}
		return application, nil, nil
	}
	updates := map[string]any{
		"last_observed_ref":    application.LastObservedRef,
		"last_observed_commit": application.LastObservedCommit,
		"last_checked_at":      now, "sync_status": status,
		"sync_message": message, "updated_at": now,
	}
	if err := s.db.WithContext(ctx).Model(&model.Application{}).Where("id = ?", application.ID).Updates(updates).Error; err != nil {
		return application, nil, fmt.Errorf("更新应用监听状态失败: %w", err)
	}
	application.LastCheckedAt, application.SyncStatus, application.SyncMessage = &now, status, message
	return application, lastRun, nil
}

func repositoryObservationWatchKey(sourceNodeID, event, ref string) string {
	digest := sha256.Sum256([]byte(sourceNodeID + "\x00" + event + "\x00" + ref))
	return fmt.Sprintf("%x", digest[:])
}

func observationEventRefKey(event, ref string) string {
	return event + "\x00" + ref
}

func applicationConfigurationChangedSinceLastCheck(application *model.Application) bool {
	if application.LastCheckedAt == nil {
		return false
	}
	if application.UpdatedAt.After(*application.LastCheckedAt) {
		return true
	}
	return application.Workflow != nil && application.Workflow.UpdatedAt.After(*application.LastCheckedAt)
}

func takeLegacyObservation(observations *[]model.ApplicationRepositoryObservation, environment, ref string) (model.ApplicationRepositoryObservation, bool) {
	for i := range *observations {
		observation := (*observations)[i]
		if observation.ID != "" && observation.Environment == environment && observation.Ref == ref {
			(*observations)[i].ID = ""
			return observation, true
		}
	}
	return model.ApplicationRepositoryObservation{}, false
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

func (s *Service) createObservedRun(ctx context.Context, application *model.Application, link model.ApplicationRepository, source model.WorkflowNode, trigger, ref, commit, message string, now time.Time) (*model.PipelineRun, error) {
	run, err := s.runFromSource(application, source, trigger, ref, commit, "system", message, now)
	if err != nil {
		return nil, err
	}
	run.CommitMessage = s.resolveCommitMessage(ctx, link.RepositoryID, ref, commit)
	component := pipelineRunRepositoryForLink(application, run.ID, link, ref, commit, now)
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		return tx.Create(&component).Error
	}); err != nil {
		return nil, fmt.Errorf("记录代码变更失败: %w", err)
	}
	run.Repositories = []model.PipelineRunRepository{component}
	if application.Workflow == nil || !application.Workflow.IsActive {
		return run, nil
	}
	advanced, err := s.AdvanceRun(ctx, run.ID, "system", "")
	if err == nil {
		return advanced, nil
	}
	const failureMessage = "检测到代码变化，但自动执行流水线失败"
	_ = s.failExecution(ctx, run.ID, failureMessage, err)
	return nil, fmt.Errorf("自动执行流水线失败: %w", err)
}

func pipelineRunRepositoryForLink(application *model.Application, runID string, link model.ApplicationRepository, ref, commit string, now time.Time) model.PipelineRunRepository {
	return model.PipelineRunRepository{
		ID: uuid.NewString(), PipelineRunID: runID, RepositoryID: link.RepositoryID,
		SortOrder: link.SortOrder, Ref: ref, CommitSHA: commit, BuildPlanID: application.BuildPlanID,
		ImageRegistryID: application.ImageRegistryID, DeploymentPlanID: application.DeploymentPlanID,
		Status:    model.PipelineRunRepositoryReady,
		CreatedAt: now, UpdatedAt: now,
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
		sources := applicationEventSources(application, event, input.Ref)
		if len(sources) == 0 || input.CommitSHA == "" {
			continue
		}
		now := time.Now().UTC()
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			applicationUpdates := map[string]any{
				"last_checked_at": now, "sync_status": model.ApplicationSyncChanged,
				"sync_message": "Webhook 检测到新的代码版本", "updated_at": now,
			}
			if applicationRepository.SortOrder == 0 {
				applicationUpdates["last_observed_ref"] = input.Ref
				applicationUpdates["last_observed_commit"] = input.CommitSHA
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
			for _, source := range sources {
				var observation model.ApplicationRepositoryObservation
				watchKey := repositoryObservationWatchKey(source.ID, event, input.Ref)
				err := tx.First(&observation, "application_repository_id = ? AND watch_key = ?", applicationRepository.ID, watchKey).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if err == nil && observation.CommitSHA == input.CommitSHA {
					continue
				}
				if errors.Is(err, gorm.ErrRecordNotFound) {
					observation = model.ApplicationRepositoryObservation{
						ID: uuid.NewString(), ApplicationRepositoryID: applicationRepository.ID,
						WatchKey: watchKey, SourceNodeID: source.ID, Event: event,
						Environment: source.Config.Environment, CreatedAt: now,
					}
				}
				observation.WatchKey, observation.SourceNodeID, observation.Event = watchKey, source.ID, event
				observation.Ref, observation.CommitSHA, observation.LastCheckedAt, observation.UpdatedAt = input.Ref, input.CommitSHA, &now, now
				if err := tx.Save(&observation).Error; err != nil {
					return err
				}
				if applicationRepository.SortOrder == 0 {
					if err := tx.Model(&model.ApplicationEnvironment{}).Where("application_id = ? AND key = ?", application.ID, source.Config.Environment).Updates(map[string]any{
						"last_observed_ref": input.Ref, "last_observed_commit": input.CommitSHA,
						"last_checked_at": now, "updated_at": now,
					}).Error; err != nil {
						return err
					}
				}
				run, err := s.runFromSource(application, source, input.EventType, input.Ref, input.CommitSHA, "", "Webhook 检测到新的代码版本", now)
				if err != nil {
					return err
				}
				run.CommitMessage = strings.TrimSpace(strings.SplitN(input.Message, "\n", 2)[0])
				if err := tx.Create(run).Error; err != nil {
					return err
				}
				component := pipelineRunRepositoryForLink(application, run.ID, *applicationRepository, input.Ref, input.CommitSHA, now)
				if err := tx.Create(&component).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("更新 Webhook 关联应用失败: %w", err)
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

func selectObservedRef(application *model.Application, refs repository.RefResult) (string, string) {
	if application.WatchTags && application.TagPattern != "" {
		for i := len(refs.Tags) - 1; i >= 0; i-- {
			if matchTag(application.TagPattern, refs.Tags[i].Name) {
				return "refs/tags/" + refs.Tags[i].Name, refs.Tags[i].SHA
			}
		}
	}
	for _, branch := range refs.Branches {
		if branch.Name == application.Branch {
			return "refs/heads/" + branch.Name, branch.SHA
		}
	}
	return "", ""
}

func pipelineComplete(application *model.Application) bool {
	return pipelineExecutionConfigured(application) && application.LastObservedCommit != ""
}

func pipelineExecutionConfigured(application *model.Application) bool {
	return applicationRepositoriesComplete(application) && application.BuildPlanID != "" &&
		application.DeploymentPlanID != ""
}

func pipelineExecutionIncompleteMessage(application *model.Application) string {
	missing := make([]string, 0, 4)
	if !applicationRepositoriesComplete(application) {
		missing = append(missing, "代码仓库")
	}
	if application.BuildPlanID == "" {
		missing = append(missing, "构建方案")
	}
	if application.DeploymentPlanID == "" {
		missing = append(missing, "部署方案")
	}
	if len(missing) == 0 {
		return ErrPipelineIncomplete.Error()
	}
	return "流水线不会执行：缺少" + strings.Join(missing, "、")
}

func pipelineIncompleteMessage(application *model.Application) string {
	missing := make([]string, 0, 4)
	if application.BuildPlanID == "" {
		missing = append(missing, "构建方案")
	}
	if !applicationRepositoriesComplete(application) {
		missing = append(missing, "代码仓库")
	}
	if application.DeploymentPlanID == "" {
		missing = append(missing, "部署方案")
	}
	if application.LastObservedCommit == "" {
		missing = append(missing, "代码版本")
	}
	if len(missing) == 0 {
		return ErrPipelineIncomplete.Error()
	}
	return "缺少：" + strings.Join(missing, "、")
}

func applicationRepositoriesComplete(application *model.Application) bool {
	return application.RepositoryID != "" && application.Repository.ID != ""
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
