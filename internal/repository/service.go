package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"zrt/internal/credential"
	"zrt/internal/model"
	"zrt/internal/secret"
)

var (
	ErrInvalidRepository       = errors.New("代码仓库配置无效")
	ErrInvalidRepositoryName   = errors.New("仓库名称只能包含中文、英文、数字、空格、点、下划线或短横线")
	ErrInsecureRepository      = errors.New("HTTP 仓库默认不允许，请显式确认不安全连接")
	ErrRepositoryExists        = errors.New("代码仓库名称已存在")
	ErrRepositoryNotFound      = errors.New("代码仓库不存在")
	ErrRepositoryInUse         = errors.New("代码仓库正在被应用使用，不能删除")
	ErrInvalidCredential       = errors.New("代码仓库凭据配置无效")
	ErrKnownHostsRequired      = errors.New("SSH 仓库必须配置可信 known_hosts 文件")
	ErrExternalWebhookDisabled = errors.New("外部 Git Webhook API 未启用")
	ErrWebhookUnavailable      = errors.New("Git Webhook 服务暂不可用")
	ErrWebhookDisabled         = errors.New("代码仓库 Webhook 未启用")
	ErrInvalidSignature        = errors.New("Webhook 签名校验失败")
	ErrUnsupportedEvent        = errors.New("Webhook 事件类型不受支持")
	ErrInvalidTaskPayload      = errors.New("Webhook 任务参数无效")
)

var repositoryNamePattern = regexp.MustCompile(`^[A-Za-z0-9\p{Han}][A-Za-z0-9\p{Han}_. -]{0,127}$`)

type Input struct {
	Name              string
	Provider          model.GitProvider
	CloneURL          string
	DefaultBranch     string
	AuthType          model.GitAuthType
	Username          string
	Credential        *string
	CredentialID      *string
	WebhookEnabled    bool
	RegenerateWebhook bool
	AllowInsecureHTTP bool
	BuildPlanID       string
	ReleasePlanID     string
}

type Service struct {
	db                 *gorm.DB
	secrets            *secret.Manager
	git                refLister
	credentials        *credential.Service
	webhookGate        webhookGate
	defaultMaxAttempts int
}

type refLister interface {
	ListRefs(context.Context, model.GitRepository, string) (RefResult, error)
}

type webhookGate interface {
	ExternalGitWebhookEnabled(context.Context) (bool, error)
}

type Option func(*Service)

func WithWebhookGate(gate webhookGate) Option {
	return func(service *Service) {
		service.webhookGate = gate
	}
}

func NewService(db *gorm.DB, secrets *secret.Manager, credentials *credential.Service, git refLister, defaultMaxAttempts int, options ...Option) *Service {
	service := &Service{db: db, secrets: secrets, credentials: credentials, git: git, defaultMaxAttempts: defaultMaxAttempts}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) List(ctx context.Context) ([]model.GitRepository, error) {
	var repositories []model.GitRepository
	if err := s.db.WithContext(ctx).Preload("BuildPlan").Preload("ReleasePlan").Order("name ASC").Find(&repositories).Error; err != nil {
		return nil, fmt.Errorf("查询代码仓库失败: %w", err)
	}
	return repositories, nil
}

func (s *Service) Find(ctx context.Context, id string) (*model.GitRepository, error) {
	var repository model.GitRepository
	if err := s.db.WithContext(ctx).Preload("BuildPlan").Preload("ReleasePlan").First(&repository, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRepositoryNotFound
		}
		return nil, fmt.Errorf("查询代码仓库失败: %w", err)
	}
	return &repository, nil
}

func (s *Service) ListDeliveries(ctx context.Context, repositoryID string, limit int) ([]model.GitWebhookDelivery, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	var deliveries []model.GitWebhookDelivery
	if err := s.db.WithContext(ctx).Where("repository_id = ?", repositoryID).
		Order("received_at DESC").Limit(limit).Find(&deliveries).Error; err != nil {
		return nil, fmt.Errorf("查询 Webhook 投递记录失败: %w", err)
	}
	return deliveries, nil
}

func (s *Service) Create(ctx context.Context, actorID string, input Input) (*model.GitRepository, string, error) {
	id := uuid.NewString()
	normalized, err := s.normalizeInput(ctx, actorID, id, nil, input)
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	repository := &model.GitRepository{
		ID: id, Name: normalized.Name, Provider: normalized.Provider,
		CloneURL: normalized.CloneURL, DefaultBranch: normalized.DefaultBranch,
		AuthType: normalized.AuthType, Username: normalized.Username,
		CredentialID:            normalized.credentialID,
		CredentialCiphertext:    normalized.credentialCiphertext,
		WebhookSecretCiphertext: normalized.webhookCiphertext,
		WebhookEnabled:          normalized.WebhookEnabled, AllowInsecureHTTP: normalized.AllowInsecureHTTP,
		BuildPlanID: normalized.BuildPlanID, ReleasePlanID: normalized.ReleasePlanID,
		IsActive: true, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(repository).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, "", ErrRepositoryExists
		}
		return nil, "", fmt.Errorf("创建代码仓库失败: %w", err)
	}
	return repository, normalized.webhookPlaintext, nil
}

func (s *Service) Update(ctx context.Context, actorID, id string, input Input) (*model.GitRepository, string, error) {
	existing, err := s.Find(ctx, id)
	if err != nil {
		return nil, "", err
	}
	normalized, err := s.normalizeInput(ctx, actorID, id, existing, input)
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"name": normalized.Name, "provider": normalized.Provider,
		"clone_url": normalized.CloneURL, "default_branch": normalized.DefaultBranch,
		"auth_type": normalized.AuthType, "username": normalized.Username,
		"credential_id":             normalized.credentialID,
		"credential_ciphertext":     normalized.credentialCiphertext,
		"webhook_secret_ciphertext": normalized.webhookCiphertext,
		"webhook_enabled":           normalized.WebhookEnabled,
		"allow_insecure_http":       normalized.AllowInsecureHTTP, "updated_at": now,
		"build_plan_id": normalized.BuildPlanID, "release_plan_id": normalized.ReleasePlanID,
	}
	if err := s.db.WithContext(ctx).Model(existing).Updates(updates).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, "", ErrRepositoryExists
		}
		return nil, "", fmt.Errorf("更新代码仓库失败: %w", err)
	}
	existing.Name = normalized.Name
	existing.Provider = normalized.Provider
	existing.CloneURL = normalized.CloneURL
	existing.DefaultBranch = normalized.DefaultBranch
	existing.AuthType = normalized.AuthType
	existing.Username = normalized.Username
	existing.CredentialID = normalized.credentialID
	existing.CredentialCiphertext = normalized.credentialCiphertext
	existing.WebhookSecretCiphertext = normalized.webhookCiphertext
	existing.WebhookEnabled = normalized.WebhookEnabled
	existing.AllowInsecureHTTP = normalized.AllowInsecureHTTP
	existing.BuildPlanID = normalized.BuildPlanID
	existing.ReleasePlanID = normalized.ReleasePlanID
	existing.UpdatedAt = now
	return existing, normalized.webhookPlaintext, nil
}

func (s *Service) SetActive(ctx context.Context, id string, active bool) error {
	result := s.db.WithContext(ctx).Model(&model.GitRepository{}).Where("id = ?", id).
		Updates(map[string]any{"is_active": active, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("修改代码仓库状态失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrRepositoryNotFound
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.GitRepository
		if err := tx.First(&existing, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRepositoryNotFound
			}
			return fmt.Errorf("查询待删除代码仓库失败: %w", err)
		}

		var legacyReferences, references int64
		if err := tx.Model(&model.Application{}).Where("repository_id = ?", id).Count(&legacyReferences).Error; err != nil {
			return fmt.Errorf("检查代码仓库使用状态失败: %w", err)
		}
		if err := tx.Model(&model.ApplicationRepository{}).Where("repository_id = ?", id).Count(&references).Error; err != nil {
			return fmt.Errorf("检查代码仓库关联状态失败: %w", err)
		}
		if legacyReferences > 0 || references > 0 {
			return ErrRepositoryInUse
		}
		if err := tx.Where("repository_id = ?", id).Delete(&model.GitWebhookDelivery{}).Error; err != nil {
			return fmt.Errorf("清理代码仓库 Webhook 投递记录失败: %w", err)
		}
		if err := tx.Delete(&existing).Error; err != nil {
			return fmt.Errorf("删除代码仓库失败: %w", err)
		}
		return nil
	})
}

func (s *Service) TestConnection(ctx context.Context, id string) (RefResult, error) {
	repository, err := s.Find(ctx, id)
	if err != nil {
		return RefResult{}, err
	}
	if !repository.IsActive {
		return RefResult{}, ErrRepositoryNotFound
	}
	credential := ""
	credential, err = s.resolveCredential(ctx, repository)
	if err != nil {
		return RefResult{}, err
	}
	return s.git.ListRefs(ctx, *repository, credential)
}

// TestInput 使用尚未保存的仓库配置执行只读远端查询，不会持久化地址或凭据。
func (s *Service) TestInput(ctx context.Context, actorID string, input Input) (RefResult, error) {
	if err := normalizeRepositoryFields(&input); err != nil {
		return RefResult{}, err
	}
	cloneURL, err := validateCloneURL(input.CloneURL, input.AllowInsecureHTTP)
	if err != nil {
		return RefResult{}, err
	}

	credentialValue := ""
	switch input.AuthType {
	case model.GitAuthNone:
		input.Username = ""
	case model.GitAuthToken, model.GitAuthSSHKey:
		if input.CredentialID != nil {
			selectedID := strings.TrimSpace(*input.CredentialID)
			if selectedID == "" || input.Credential != nil || s.credentials == nil {
				return RefResult{}, ErrInvalidCredential
			}
			selected, err := s.credentials.FindOwned(ctx, actorID, selectedID)
			if err != nil || selected.AuthType != input.AuthType || !credentialProviderCompatible(selected.Provider, input.Provider) {
				return RefResult{}, ErrInvalidCredential
			}
			credentialValue, err = s.credentials.RevealOwned(ctx, actorID, selectedID)
			if err != nil {
				return RefResult{}, fmt.Errorf("读取代码仓库测试凭据失败: %w", err)
			}
			if input.Username == "" {
				input.Username = selected.Username
			}
		} else {
			if input.Credential == nil {
				return RefResult{}, ErrInvalidCredential
			}
			credentialValue = strings.TrimSpace(*input.Credential)
			if credentialValue == "" || len(credentialValue) > 64*1024 ||
				(input.AuthType == model.GitAuthSSHKey && !strings.Contains(credentialValue, "PRIVATE KEY")) {
				return RefResult{}, ErrInvalidCredential
			}
		}
	default:
		return RefResult{}, ErrInvalidCredential
	}

	return s.git.ListRefs(ctx, model.GitRepository{
		Provider: input.Provider, CloneURL: cloneURL, AuthType: input.AuthType, Username: input.Username,
	}, credentialValue)
}

func (s *Service) RevealWebhookSecret(ctx context.Context, id string) (string, error) {
	repository, err := s.Find(ctx, id)
	if err != nil {
		return "", err
	}
	if !repository.WebhookEnabled || repository.WebhookSecretCiphertext == "" {
		return "", ErrWebhookDisabled
	}
	plaintext, err := s.secrets.Decrypt(repository.WebhookSecretCiphertext, webhookAAD(repository.ID))
	if err != nil {
		return "", fmt.Errorf("解密 Webhook 密钥失败: %w", err)
	}
	return plaintext, nil
}

func (s *Service) CredentialIDForUser(ctx context.Context, userID string, repository *model.GitRepository) *string {
	if repository.CredentialID == nil || *repository.CredentialID == "" || s.credentials == nil {
		return nil
	}
	if _, err := s.credentials.FindOwned(ctx, userID, *repository.CredentialID); err != nil {
		return nil
	}
	id := *repository.CredentialID
	return &id
}

func (s *Service) resolveCredential(ctx context.Context, repository *model.GitRepository) (string, error) {
	if repository.CredentialID != nil && *repository.CredentialID != "" {
		_, plaintext, err := s.credentials.Resolve(ctx, *repository.CredentialID)
		if err != nil {
			return "", fmt.Errorf("读取代码仓库引用令牌失败: %w", err)
		}
		return plaintext, nil
	}
	if repository.CredentialCiphertext == "" {
		return "", nil
	}
	plaintext, err := s.secrets.Decrypt(repository.CredentialCiphertext, credentialAAD(repository.ID))
	if err != nil {
		return "", fmt.Errorf("解密代码仓库凭据失败: %w", err)
	}
	return plaintext, nil
}

type normalizedInput struct {
	Input
	credentialID         *string
	credentialCiphertext string
	webhookCiphertext    string
	webhookPlaintext     string
}

func (s *Service) normalizeInput(ctx context.Context, actorID, id string, existing *model.GitRepository, input Input) (normalizedInput, error) {
	if err := normalizeRepositoryFields(&input); err != nil {
		return normalizedInput{}, err
	}
	cloneURL, err := validateCloneURL(input.CloneURL, input.AllowInsecureHTTP)
	if err != nil {
		return normalizedInput{}, err
	}
	input.CloneURL = cloneURL
	for _, resource := range []struct {
		id    string
		model any
	}{{input.BuildPlanID, &model.BuildPlan{}}, {input.ReleasePlanID, &model.ReleasePlan{}}} {
		if resource.id == "" {
			continue
		}
		var count int64
		if err := s.db.WithContext(ctx).Model(resource.model).Where("id = ? AND is_active = ?", resource.id, true).Count(&count).Error; err != nil || count != 1 {
			return normalizedInput{}, ErrInvalidRepository
		}
	}

	var credentialID *string
	credentialCiphertext := ""
	if existing != nil {
		credentialID = existing.CredentialID
		credentialCiphertext = existing.CredentialCiphertext
	}
	if input.AuthType == model.GitAuthNone {
		credentialID = nil
		credentialCiphertext = ""
		input.Username = ""
	} else if input.CredentialID != nil {
		selectedID := strings.TrimSpace(*input.CredentialID)
		if selectedID == "" || input.Credential != nil || s.credentials == nil {
			return normalizedInput{}, ErrInvalidCredential
		}
		selected, err := s.credentials.FindOwned(ctx, actorID, selectedID)
		if err != nil || selected.AuthType != input.AuthType || !credentialProviderCompatible(selected.Provider, input.Provider) {
			return normalizedInput{}, ErrInvalidCredential
		}
		credentialID = &selected.ID
		credentialCiphertext = ""
		if input.Username == "" {
			input.Username = selected.Username
		}
	} else if input.Credential != nil {
		credential := strings.TrimSpace(*input.Credential)
		if credential == "" || len(credential) > 64*1024 {
			return normalizedInput{}, ErrInvalidCredential
		}
		if input.AuthType == model.GitAuthSSHKey && !strings.Contains(credential, "PRIVATE KEY") {
			return normalizedInput{}, ErrInvalidCredential
		}
		credentialCiphertext, err = s.secrets.Encrypt(credential, credentialAAD(id))
		if err != nil {
			return normalizedInput{}, fmt.Errorf("加密代码仓库凭据失败: %w", err)
		}
		credentialID = nil
	} else if existing != nil && existing.AuthType != input.AuthType {
		return normalizedInput{}, ErrInvalidCredential
	}
	if input.AuthType != model.GitAuthNone && credentialCiphertext == "" && credentialID == nil {
		return normalizedInput{}, ErrInvalidCredential
	}

	webhookCiphertext := ""
	if existing != nil {
		webhookCiphertext = existing.WebhookSecretCiphertext
	}
	webhookPlaintext := ""
	shouldGenerate := input.WebhookEnabled && (webhookCiphertext == "" || input.RegenerateWebhook)
	if shouldGenerate {
		webhookPlaintext, err = randomSecret()
		if err != nil {
			return normalizedInput{}, err
		}
		webhookCiphertext, err = s.secrets.Encrypt(webhookPlaintext, webhookAAD(id))
		if err != nil {
			return normalizedInput{}, fmt.Errorf("加密 Webhook 密钥失败: %w", err)
		}
	}
	return normalizedInput{
		Input: input, credentialID: credentialID, credentialCiphertext: credentialCiphertext,
		webhookCiphertext: webhookCiphertext, webhookPlaintext: webhookPlaintext,
	}, nil
}

func normalizeRepositoryFields(input *Input) error {
	input.Name = strings.TrimSpace(input.Name)
	input.DefaultBranch = strings.TrimSpace(input.DefaultBranch)
	input.Username = strings.TrimSpace(input.Username)
	input.BuildPlanID = strings.TrimSpace(input.BuildPlanID)
	input.ReleasePlanID = strings.TrimSpace(input.ReleasePlanID)
	if !repositoryNamePattern.MatchString(input.Name) {
		return ErrInvalidRepositoryName
	}
	if utf8.RuneCountInString(input.DefaultBranch) > 255 || utf8.RuneCountInString(input.Username) > 255 ||
		!validProvider(input.Provider) || !validAuthType(input.AuthType) {
		return ErrInvalidRepository
	}
	return nil
}

func credentialProviderCompatible(saved, repository model.GitProvider) bool {
	return saved == model.GitProviderGeneric || repository == model.GitProviderGeneric || saved == repository
}

func validProvider(provider model.GitProvider) bool {
	switch provider {
	case model.GitProviderGeneric, model.GitProviderGitHub, model.GitProviderGitLab, model.GitProviderGitea, model.GitProviderGitee:
		return true
	default:
		return false
	}
}

func validAuthType(authType model.GitAuthType) bool {
	return authType == model.GitAuthNone || authType == model.GitAuthToken || authType == model.GitAuthSSHKey
}

func credentialAAD(id string) []byte { return []byte("git_repository:" + id + ":credential") }
func webhookAAD(id string) []byte    { return []byte("git_repository:" + id + ":webhook") }

func randomSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("生成 Webhook 密钥失败: %w", err)
	}
	return hex.EncodeToString(value), nil
}
