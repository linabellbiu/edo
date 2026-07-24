package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

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
	ErrRegistryExists           = errors.New("镜像仓库名称已存在")
	ErrInvalidReleasePlan       = errors.New("发布方案配置无效")
	ErrReleasePlanExists        = errors.New("发布方案名称已存在")
	ErrWorkflowTemplateNotFound = errors.New("公共发布计划不存在或未启用")
	ErrPipelineIncomplete       = errors.New("应用尚未绑定完整的构建与发布流程")
)

var resourceNamePattern = regexp.MustCompile(`^[A-Za-z0-9\p{Han}][A-Za-z0-9\p{Han}_. -]{0,127}$`)

type ApplicationInput struct {
	Name                   string
	Description            string
	RepositoryID           string
	Branch                 string
	PollEnabled            bool
	PollIntervalSeconds    int
	WatchPush              bool
	WatchPullRequest       bool
	WatchTags              bool
	TagPattern             string
	BuildPlanID            string
	ImageRegistryID        string
	ReleasePlanID          string
	DeploymentTargetID     string
	WorkflowTemplateID     string
	ReleaseApprovalEnabled bool
	Environments           []EnvironmentInput
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
	ReleasePlanID      string
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

type ReleasePlanInput struct {
	Name           string
	Kind           model.ReleasePlanKind
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
}

func NewService(db *gorm.DB, repositories *repository.Service, secrets *secret.Manager) *Service {
	return &Service{db: db, repositories: repositories, secrets: secrets}
}

func (s *Service) ListApplications(ctx context.Context) ([]model.Application, error) {
	var applications []model.Application
	err := s.db.WithContext(ctx).
		Preload("Repository").Preload("BuildPlan").Preload("ImageRegistry").
		Preload("ReleasePlan").Preload("DeploymentTarget").
		Preload("WorkflowTemplate").
		Preload("Environments", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Preload("Environments.ReleasePlan").Preload("Environments.DeploymentTarget").
		Preload("Workflow").
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
		Preload("ReleasePlan").Preload("DeploymentTarget").
		Preload("WorkflowTemplate").
		Preload("Environments", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Preload("Environments.ReleasePlan").Preload("Environments.DeploymentTarget").
		Preload("Workflow").
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
		ReleasePlanID: input.ReleasePlanID, DeploymentTargetID: input.DeploymentTargetID,
		WorkflowTemplateID:     input.WorkflowTemplateID,
		ReleaseApprovalEnabled: input.ReleaseApprovalEnabled,
		SyncStatus:             model.ApplicationSyncIdle, IsActive: true,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	environments := buildEnvironmentModels(application.ID, input.Environments, now)
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
		"image_registry_id": input.ImageRegistryID, "release_plan_id": input.ReleasePlanID,
		"deployment_target_id":     input.DeploymentTargetID,
		"workflow_template_id":     input.WorkflowTemplateID,
		"release_approval_enabled": input.ReleaseApprovalEnabled, "updated_at": time.Now().UTC(),
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
		updated.ReleaseApprovalEnabled = input.ReleaseApprovalEnabled
		updated.Environments = buildEnvironmentModels(existing.ID, input.Environments, time.Now().UTC())
		replacementWorkflow, err = s.newApplicationWorkflow(ctx, &updated, existing.CreatedBy, time.Now().UTC())
		if err != nil {
			return nil, err
		}
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(existing).Updates(updates).Error; err != nil {
			return err
		}
		if err := saveApplicationEnvironments(tx, existing, input.Environments, time.Now().UTC()); err != nil {
			return err
		}
		if replacementWorkflow != nil {
			if err := tx.Where("application_id = ?", existing.ID).Delete(&model.ReleaseWorkflow{}).Error; err != nil {
				return err
			}
			return tx.Create(replacementWorkflow).Error
		}
		if workflowInputsChanged(existing, input) {
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
	input.ReleasePlanID = strings.TrimSpace(input.ReleasePlanID)
	input.DeploymentTargetID = strings.TrimSpace(input.DeploymentTargetID)
	input.WorkflowTemplateID = strings.TrimSpace(input.WorkflowTemplateID)
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
			TagPattern: input.TagPattern, ReleasePlanID: input.ReleasePlanID,
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
		input.PollIntervalSeconds = 60
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
		{input.ReleasePlanID, &model.ReleasePlan{}},
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
	hasTrigger, hasPull := false, false
	for i := range input.Environments {
		environment := &input.Environments[i]
		environment.Key = strings.ToLower(strings.TrimSpace(environment.Key))
		environment.Name = strings.TrimSpace(environment.Name)
		environment.Branch = strings.TrimSpace(environment.Branch)
		environment.TagPattern = strings.TrimSpace(environment.TagPattern)
		environment.ReleasePlanID = strings.TrimSpace(environment.ReleasePlanID)
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
		if environment.PollEnabled {
			hasPull = true
		}
		for _, check := range []struct {
			id    string
			model any
		}{{environment.ReleasePlanID, &model.ReleasePlan{}}, {environment.DeploymentTargetID, &model.DeploymentTarget{}}} {
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
	if !hasTrigger || (hasPull && (input.PollIntervalSeconds < 15 || input.PollIntervalSeconds > 86400)) {
		return input, ErrInvalidApplication
	}
	primary := input.Environments[0]
	input.Branch, input.PollEnabled = primary.Branch, primary.PollEnabled
	input.WatchPush, input.WatchPullRequest = primary.WatchPush, primary.WatchPullRequest
	input.WatchTags, input.TagPattern = primary.WatchTags, primary.TagPattern
	if input.ReleasePlanID == "" {
		input.ReleasePlanID = primary.ReleasePlanID
	}
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
			ReleasePlanID: inputs[i].ReleasePlanID, DeploymentTargetID: inputs[i].DeploymentTargetID,
			SortOrder: i, CreatedAt: now, UpdatedAt: now,
		})
	}
	return result
}

func environmentModelInputs(environments []model.ApplicationEnvironment) []EnvironmentInput {
	result := make([]EnvironmentInput, 0, len(environments))
	for i := range environments {
		result = append(result, EnvironmentInput{
			Key: environments[i].Key, Name: environments[i].Name, Branch: environments[i].Branch,
			PollEnabled: environments[i].PollEnabled, WatchPush: environments[i].WatchPush,
			WatchPullRequest: environments[i].WatchPullRequest, WatchTags: environments[i].WatchTags,
			TagPattern: environments[i].TagPattern, ReleasePlanID: environments[i].ReleasePlanID,
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
	if existing.WorkflowTemplateID != input.WorkflowTemplateID || existing.ReleaseApprovalEnabled != input.ReleaseApprovalEnabled || len(existing.Environments) != len(input.Environments) {
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
			found.ReleasePlanID != current.ReleasePlanID || found.DeploymentTargetID != current.DeploymentTargetID {
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
	input.Name, input.Endpoint = strings.TrimSpace(input.Name), strings.TrimSpace(input.Endpoint)
	input.Namespace, input.Username = strings.Trim(strings.TrimSpace(input.Namespace), "/"), strings.TrimSpace(input.Username)
	if !validResourceName(input.Name) || !validRegistryProvider(input.Provider) || utf8.RuneCountInString(input.Namespace) > 255 ||
		utf8.RuneCountInString(input.Username) > 255 {
		return nil, ErrInvalidRegistry
	}
	parsed, err := url.Parse(input.Endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && input.AllowInsecureHTTP)) {
		return nil, ErrInvalidRegistry
	}
	credentialCiphertext := ""
	if input.Credential != nil && strings.TrimSpace(*input.Credential) != "" {
		credentialCiphertext, err = s.secrets.Encrypt(strings.TrimSpace(*input.Credential), []byte("image_registry:"+input.Name+":credential"))
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

func (s *Service) ListReleasePlans(ctx context.Context) ([]model.ReleasePlan, error) {
	var plans []model.ReleasePlan
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&plans).Error; err != nil {
		return nil, fmt.Errorf("查询发布方案失败: %w", err)
	}
	return plans, nil
}

func (s *Service) CreateReleasePlan(ctx context.Context, actorID string, input ReleasePlanInput) (*model.ReleasePlan, error) {
	input.Name, input.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
	input.Script, input.HelmChart = strings.TrimSpace(input.Script), strings.TrimSpace(input.HelmChart)
	input.ComposeFile, input.ServiceName = strings.TrimSpace(input.ComposeFile), strings.TrimSpace(input.ServiceName)
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 600
	}
	validKind := (input.Kind == model.ReleasePlanScript && input.Script != "") ||
		(input.Kind == model.ReleasePlanHelm && input.HelmChart != "") ||
		(input.Kind == model.ReleasePlanCompose && input.ComposeFile != "") ||
		(input.Kind == model.ReleasePlanDocker && input.ServiceName != "")
	if !validResourceName(input.Name) || !validKind || input.TimeoutSeconds < 30 || input.TimeoutSeconds > 3600 ||
		len(input.Script) > 256*1024 || len(input.HelmValues) > 512*1024 || utf8.RuneCountInString(input.Description) > 500 {
		return nil, ErrInvalidReleasePlan
	}
	now := time.Now().UTC()
	plan := &model.ReleasePlan{
		ID: uuid.NewString(), Name: input.Name, Kind: input.Kind, Description: input.Description,
		Script: input.Script, HelmChart: input.HelmChart, HelmValues: input.HelmValues,
		ComposeFile: input.ComposeFile, ServiceName: input.ServiceName, TimeoutSeconds: input.TimeoutSeconds,
		IsActive: true, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(plan).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrReleasePlanExists
		}
		return nil, fmt.Errorf("创建发布方案失败: %w", err)
	}
	return plan, nil
}

func (s *Service) ListRuns(ctx context.Context, limit int) ([]model.PipelineRun, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	var runs []model.PipelineRun
	if err := s.db.WithContext(ctx).Preload("Application").Order("created_at DESC").Limit(limit).Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("查询流水线记录失败: %w", err)
	}
	return runs, nil
}

func (s *Service) PrepareRun(ctx context.Context, applicationID, actorID string) (*model.PipelineRun, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	if application.Workflow != nil && application.Workflow.IsActive {
		var source *model.WorkflowNode
		for i := range application.Workflow.Nodes {
			if application.Workflow.Nodes[i].Type == model.WorkflowNodeTrigger {
				source = &application.Workflow.Nodes[i]
				break
			}
		}
		if source == nil || application.LastObservedCommit == "" {
			return s.createBlockedRun(ctx, application, actorID)
		}
		now := time.Now().UTC()
		run, err := newWorkflowRun(
			application, application.Workflow, *source, "manual", application.LastObservedRef,
			application.LastObservedCommit, actorID, "发布计划已启动", now,
		)
		if err != nil {
			return nil, err
		}
		if err := s.db.WithContext(ctx).Create(run).Error; err != nil {
			return nil, fmt.Errorf("创建发布计划运行记录失败: %w", err)
		}
		return run, nil
	}
	status, message := model.PipelineRunReady, "构建与发布配置已就绪"
	if !pipelineComplete(application) {
		status, message = model.PipelineRunBlocked, ErrPipelineIncomplete.Error()
	}
	now := time.Now().UTC()
	run := &model.PipelineRun{
		ID: uuid.NewString(), ApplicationID: application.ID, Trigger: "manual",
		Ref: application.LastObservedRef, CommitSHA: application.LastObservedCommit,
		Status: status, Stage: "configured", Message: message, CreatedBy: actorID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(run).Error; err != nil {
		return nil, fmt.Errorf("创建流水线记录失败: %w", err)
	}
	if status == model.PipelineRunBlocked {
		return run, ErrPipelineIncomplete
	}
	return run, nil
}

func (s *Service) createBlockedRun(ctx context.Context, application *model.Application, actorID string) (*model.PipelineRun, error) {
	now := time.Now().UTC()
	run := &model.PipelineRun{
		ID: uuid.NewString(), ApplicationID: application.ID, Trigger: "manual",
		Ref: application.LastObservedRef, CommitSHA: application.LastObservedCommit,
		Status: model.PipelineRunBlocked, Stage: "configured", Message: ErrPipelineIncomplete.Error(),
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(run).Error; err != nil {
		return nil, fmt.Errorf("创建流水线记录失败: %w", err)
	}
	return run, ErrPipelineIncomplete
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
	refs, err := s.repositories.TestConnection(ctx, application.RepositoryID)
	if err != nil {
		s.markSyncFailure(ctx, application.ID, err)
		return application, nil, err
	}
	pollSources := applicationPollSources(application)
	if len(pollSources) == 0 {
		err = fmt.Errorf("应用没有启用 Pull 监听节点")
		s.markSyncFailure(ctx, application.ID, err)
		return application, nil, err
	}
	environmentByKey := make(map[string]*model.ApplicationEnvironment, len(application.Environments))
	for i := range application.Environments {
		environmentByKey[application.Environments[i].Key] = &application.Environments[i]
	}
	status, message := model.ApplicationSyncSynced, "代码已是最新状态"
	var lastRun *model.PipelineRun
	found := false
	for _, source := range pollSources {
		ref, commit := selectWorkflowRef(source, refs)
		if commit == "" {
			continue
		}
		found = true
		environment := environmentByKey[source.Config.Environment]
		previousCommit := ""
		if environment != nil {
			previousCommit = environment.LastObservedCommit
		}
		changed := previousCommit != "" && previousCommit != commit
		if environment != nil {
			if err := s.db.WithContext(ctx).Model(&model.ApplicationEnvironment{}).Where("id = ?", environment.ID).Updates(map[string]any{
				"last_observed_ref": ref, "last_observed_commit": commit,
				"last_checked_at": now, "updated_at": now,
			}).Error; err != nil {
				return application, nil, fmt.Errorf("更新环境监听状态失败: %w", err)
			}
			environment.LastObservedRef, environment.LastObservedCommit, environment.LastCheckedAt = ref, commit, &now
		}
		application.LastObservedRef, application.LastObservedCommit = ref, commit
		if !changed {
			continue
		}
		status, message = model.ApplicationSyncChanged, "检测到新的代码版本"
		run, createErr := s.runFromSource(application, source, trigger, ref, commit, "", message, now)
		if createErr != nil {
			return application, nil, createErr
		}
		if err := s.db.WithContext(ctx).Create(run).Error; err != nil {
			return application, nil, fmt.Errorf("记录代码变更失败: %w", err)
		}
		lastRun = run
	}
	if !found {
		err = fmt.Errorf("仓库中不存在发布计划监听的分支或标签")
		s.markSyncFailure(ctx, application.ID, err)
		return application, nil, err
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

func (s *Service) HandleRepositoryEvent(ctx context.Context, input repository.WebhookTaskPayload) error {
	var applications []model.Application
	if err := s.db.WithContext(ctx).Where("repository_id = ? AND is_active = ?", input.RepositoryID, true).Find(&applications).Error; err != nil {
		return fmt.Errorf("查询 Webhook 关联应用失败: %w", err)
	}
	for i := range applications {
		application, err := s.FindApplication(ctx, applications[i].ID)
		if err != nil {
			return err
		}
		event := workflowEventName(input.EventType)
		sources := applicationEventSources(application, event, input.Ref)
		if len(sources) == 0 || input.CommitSHA == "" {
			continue
		}
		now := time.Now().UTC()
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.Application{}).Where("id = ?", application.ID).Updates(map[string]any{
				"last_observed_ref": input.Ref, "last_observed_commit": input.CommitSHA,
				"last_checked_at": now, "sync_status": model.ApplicationSyncChanged,
				"sync_message": "Webhook 检测到新的代码版本", "updated_at": now,
			}).Error; err != nil {
				return err
			}
			for _, source := range sources {
				var environment model.ApplicationEnvironment
				err := tx.First(&environment, "application_id = ? AND key = ?", application.ID, source.Config.Environment).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if err == nil && environment.LastObservedCommit == input.CommitSHA {
					continue
				}
				if err == nil {
					if err := tx.Model(&environment).Updates(map[string]any{
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
				if err := tx.Create(run).Error; err != nil {
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
	if scanInterval < 5*time.Second {
		scanInterval = 15 * time.Second
	}
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
		_, _, _ = s.SyncApplication(ctx, application.ID, "poll")
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
	return application.BuildPlanID != "" && application.ImageRegistryID != "" &&
		application.ReleasePlanID != "" && application.DeploymentTargetID != "" &&
		application.LastObservedCommit != ""
}

func validResourceName(name string) bool { return resourceNamePattern.MatchString(name) }

func validRegistryProvider(provider model.RegistryProvider) bool {
	return provider == model.RegistryGeneric || provider == model.RegistryHarbor || provider == model.RegistryDockerHub
}

func matchTag(pattern, tag string) bool {
	if pattern == "" {
		return true
	}
	matched, err := path.Match(pattern, tag)
	return err == nil && matched
}
