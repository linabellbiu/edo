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

	"zrt/internal/model"
	"zrt/internal/secret"
)

var (
	ErrInvalidRepository  = errors.New("代码仓库配置无效")
	ErrInsecureRepository = errors.New("HTTP 仓库默认不允许，请显式确认不安全连接")
	ErrRepositoryExists   = errors.New("代码仓库名称已存在")
	ErrRepositoryNotFound = errors.New("代码仓库不存在")
	ErrInvalidCredential  = errors.New("代码仓库凭据配置无效")
	ErrKnownHostsRequired = errors.New("SSH 仓库必须配置可信 known_hosts 文件")
	ErrWebhookDisabled    = errors.New("代码仓库 Webhook 未启用")
	ErrInvalidSignature   = errors.New("Webhook 签名校验失败")
	ErrUnsupportedEvent   = errors.New("Webhook 事件类型不受支持")
	ErrInvalidTaskPayload = errors.New("Webhook 任务参数无效")
)

var repositoryNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]{1,127}$`)

type Input struct {
	Name              string
	Provider          model.GitProvider
	CloneURL          string
	DefaultBranch     string
	AuthType          model.GitAuthType
	Username          string
	Credential        *string
	WebhookEnabled    bool
	RegenerateWebhook bool
	AllowInsecureHTTP bool
}

type Service struct {
	db                 *gorm.DB
	secrets            *secret.Manager
	git                *GitClient
	defaultMaxAttempts int
}

func NewService(db *gorm.DB, secrets *secret.Manager, git *GitClient, defaultMaxAttempts int) *Service {
	return &Service{db: db, secrets: secrets, git: git, defaultMaxAttempts: defaultMaxAttempts}
}

func (s *Service) List(ctx context.Context) ([]model.GitRepository, error) {
	var repositories []model.GitRepository
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&repositories).Error; err != nil {
		return nil, fmt.Errorf("查询代码仓库失败: %w", err)
	}
	return repositories, nil
}

func (s *Service) Find(ctx context.Context, id string) (*model.GitRepository, error) {
	var repository model.GitRepository
	if err := s.db.WithContext(ctx).First(&repository, "id = ?", id).Error; err != nil {
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
	normalized, err := s.normalizeInput(id, nil, input)
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	repository := &model.GitRepository{
		ID: id, Name: normalized.Name, Provider: normalized.Provider,
		CloneURL: normalized.CloneURL, DefaultBranch: normalized.DefaultBranch,
		AuthType: normalized.AuthType, Username: normalized.Username,
		CredentialCiphertext:    normalized.credentialCiphertext,
		WebhookSecretCiphertext: normalized.webhookCiphertext,
		WebhookEnabled:          normalized.WebhookEnabled, AllowInsecureHTTP: normalized.AllowInsecureHTTP,
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

func (s *Service) Update(ctx context.Context, id string, input Input) (*model.GitRepository, string, error) {
	existing, err := s.Find(ctx, id)
	if err != nil {
		return nil, "", err
	}
	normalized, err := s.normalizeInput(id, existing, input)
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"name": normalized.Name, "provider": normalized.Provider,
		"clone_url": normalized.CloneURL, "default_branch": normalized.DefaultBranch,
		"auth_type": normalized.AuthType, "username": normalized.Username,
		"credential_ciphertext":     normalized.credentialCiphertext,
		"webhook_secret_ciphertext": normalized.webhookCiphertext,
		"webhook_enabled":           normalized.WebhookEnabled,
		"allow_insecure_http":       normalized.AllowInsecureHTTP, "updated_at": now,
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
	existing.CredentialCiphertext = normalized.credentialCiphertext
	existing.WebhookSecretCiphertext = normalized.webhookCiphertext
	existing.WebhookEnabled = normalized.WebhookEnabled
	existing.AllowInsecureHTTP = normalized.AllowInsecureHTTP
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

func (s *Service) TestConnection(ctx context.Context, id string) (RefResult, error) {
	repository, err := s.Find(ctx, id)
	if err != nil {
		return RefResult{}, err
	}
	if !repository.IsActive {
		return RefResult{}, ErrRepositoryNotFound
	}
	credential := ""
	if repository.CredentialCiphertext != "" {
		credential, err = s.secrets.Decrypt(repository.CredentialCiphertext, credentialAAD(repository.ID))
		if err != nil {
			return RefResult{}, fmt.Errorf("解密代码仓库凭据失败: %w", err)
		}
	}
	return s.git.ListRefs(ctx, *repository, credential)
}

type normalizedInput struct {
	Input
	credentialCiphertext string
	webhookCiphertext    string
	webhookPlaintext     string
}

func (s *Service) normalizeInput(id string, existing *model.GitRepository, input Input) (normalizedInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.DefaultBranch = strings.TrimSpace(input.DefaultBranch)
	input.Username = strings.TrimSpace(input.Username)
	if !repositoryNamePattern.MatchString(input.Name) || utf8.RuneCountInString(input.DefaultBranch) > 255 ||
		utf8.RuneCountInString(input.Username) > 255 || !validProvider(input.Provider) || !validAuthType(input.AuthType) {
		return normalizedInput{}, ErrInvalidRepository
	}
	cloneURL, err := validateCloneURL(input.CloneURL, input.AllowInsecureHTTP)
	if err != nil {
		return normalizedInput{}, err
	}
	input.CloneURL = cloneURL

	credentialCiphertext := ""
	if existing != nil {
		credentialCiphertext = existing.CredentialCiphertext
	}
	if input.AuthType == model.GitAuthNone {
		credentialCiphertext = ""
		input.Username = ""
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
	} else if existing != nil && existing.AuthType != input.AuthType {
		return normalizedInput{}, ErrInvalidCredential
	}
	if input.AuthType != model.GitAuthNone && credentialCiphertext == "" {
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
		Input: input, credentialCiphertext: credentialCiphertext,
		webhookCiphertext: webhookCiphertext, webhookPlaintext: webhookPlaintext,
	}, nil
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
