package credential

import (
	"context"
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
	ErrInvalidCredential  = errors.New("Git 令牌信息无效")
	ErrCredentialExists   = errors.New("Git 令牌名称已存在")
	ErrCredentialNotFound = errors.New("Git 令牌不存在")
	ErrCredentialInUse    = errors.New("Git 令牌正在被代码仓库使用，不能删除")
)

var credentialNamePattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_. -]{1,127}$`)

type Input struct {
	Name     string
	Provider model.GitProvider
	AuthType model.GitAuthType
	Username string
	Secret   *string
}

type Service struct {
	db      *gorm.DB
	secrets *secret.Manager
}

func NewService(db *gorm.DB, secrets *secret.Manager) *Service {
	return &Service{db: db, secrets: secrets}
}

func (s *Service) List(ctx context.Context, userID string) ([]model.GitCredential, error) {
	var credentials []model.GitCredential
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Order("name ASC").Find(&credentials).Error; err != nil {
		return nil, fmt.Errorf("查询个人 Git 令牌失败: %w", err)
	}
	return credentials, nil
}

func (s *Service) FindOwned(ctx context.Context, userID, id string) (*model.GitCredential, error) {
	var credential model.GitCredential
	if err := s.db.WithContext(ctx).First(&credential, "id = ? AND user_id = ?", id, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrCredentialNotFound
		}
		return nil, fmt.Errorf("查询个人 Git 令牌失败: %w", err)
	}
	return &credential, nil
}

func (s *Service) Create(ctx context.Context, userID string, input Input) (*model.GitCredential, error) {
	input, plaintext, err := normalizeInput(input, true)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	ciphertext, err := s.secrets.Encrypt(plaintext, credentialAAD(id))
	if err != nil {
		return nil, fmt.Errorf("加密个人 Git 令牌失败: %w", err)
	}
	now := time.Now().UTC()
	credential := &model.GitCredential{
		ID: id, UserID: userID, Name: input.Name, Provider: input.Provider,
		AuthType: input.AuthType, Username: input.Username, SecretCiphertext: ciphertext,
		SecretHint: secretHint(plaintext), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(credential).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrCredentialExists
		}
		return nil, fmt.Errorf("创建个人 Git 令牌失败: %w", err)
	}
	return credential, nil
}

func (s *Service) Update(ctx context.Context, userID, id string, input Input) (*model.GitCredential, error) {
	existing, err := s.FindOwned(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	input, plaintext, err := normalizeInput(input, false)
	if err != nil {
		return nil, err
	}
	if input.Provider != existing.Provider || input.AuthType != existing.AuthType {
		var references int64
		if err := s.db.WithContext(ctx).Model(&model.GitRepository{}).
			Where("credential_id = ? OR api_credential_id = ?", id, id).Count(&references).Error; err != nil {
			return nil, fmt.Errorf("检查 Git 令牌使用状态失败: %w", err)
		}
		if references > 0 {
			return nil, ErrCredentialInUse
		}
	}
	ciphertext := existing.SecretCiphertext
	hint := existing.SecretHint
	if input.Secret != nil {
		ciphertext, err = s.secrets.Encrypt(plaintext, credentialAAD(id))
		if err != nil {
			return nil, fmt.Errorf("加密个人 Git 令牌失败: %w", err)
		}
		hint = secretHint(plaintext)
	} else if input.AuthType != existing.AuthType {
		return nil, ErrInvalidCredential
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"name": input.Name, "provider": input.Provider, "auth_type": input.AuthType,
		"username": input.Username, "secret_ciphertext": ciphertext, "secret_hint": hint, "updated_at": now,
	}
	if err := s.db.WithContext(ctx).Model(existing).Updates(updates).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrCredentialExists
		}
		return nil, fmt.Errorf("更新个人 Git 令牌失败: %w", err)
	}
	existing.Name, existing.Provider, existing.AuthType, existing.Username = input.Name, input.Provider, input.AuthType, input.Username
	existing.SecretCiphertext, existing.SecretHint, existing.UpdatedAt = ciphertext, hint, now
	return existing, nil
}

func (s *Service) Delete(ctx context.Context, userID, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var credential model.GitCredential
		if err := tx.First(&credential, "id = ? AND user_id = ?", id, userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCredentialNotFound
			}
			return fmt.Errorf("查询待删除 Git 令牌失败: %w", err)
		}
		var count int64
		if err := tx.Model(&model.GitRepository{}).
			Where("credential_id = ? OR api_credential_id = ?", id, id).Count(&count).Error; err != nil {
			return fmt.Errorf("检查 Git 令牌使用状态失败: %w", err)
		}
		if count > 0 {
			return ErrCredentialInUse
		}
		if err := tx.Delete(&credential).Error; err != nil {
			return fmt.Errorf("删除个人 Git 令牌失败: %w", err)
		}
		return nil
	})
}

func (s *Service) RevealOwned(ctx context.Context, userID, id string) (string, error) {
	credential, err := s.FindOwned(ctx, userID, id)
	if err != nil {
		return "", err
	}
	return s.decrypt(credential)
}

// Resolve 仅供已通过权限校验的仓库运行时解析引用，不用于面向用户的读取接口。
func (s *Service) Resolve(ctx context.Context, id string) (*model.GitCredential, string, error) {
	var credential model.GitCredential
	if err := s.db.WithContext(ctx).First(&credential, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", ErrCredentialNotFound
		}
		return nil, "", fmt.Errorf("查询 Git 令牌失败: %w", err)
	}
	plaintext, err := s.decrypt(&credential)
	if err != nil {
		return nil, "", err
	}
	return &credential, plaintext, nil
}

func (s *Service) decrypt(credential *model.GitCredential) (string, error) {
	plaintext, err := s.secrets.Decrypt(credential.SecretCiphertext, credentialAAD(credential.ID))
	if err != nil {
		return "", fmt.Errorf("解密个人 Git 令牌失败: %w", err)
	}
	return plaintext, nil
}

func normalizeInput(input Input, requireSecret bool) (Input, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Username = strings.TrimSpace(input.Username)
	if !credentialNamePattern.MatchString(input.Name) || utf8.RuneCountInString(input.Username) > 255 ||
		!validProvider(input.Provider) || (input.AuthType != model.GitAuthToken && input.AuthType != model.GitAuthSSHKey) {
		return Input{}, "", ErrInvalidCredential
	}
	if requireSecret && input.Secret == nil {
		return Input{}, "", ErrInvalidCredential
	}
	plaintext := ""
	if input.Secret != nil {
		plaintext = strings.TrimSpace(*input.Secret)
		if len(plaintext) < 8 || len(plaintext) > 64*1024 ||
			(input.AuthType == model.GitAuthSSHKey && !strings.Contains(plaintext, "PRIVATE KEY")) {
			return Input{}, "", ErrInvalidCredential
		}
	}
	return input, plaintext, nil
}

func validProvider(provider model.GitProvider) bool {
	switch provider {
	case model.GitProviderGeneric, model.GitProviderGitHub, model.GitProviderGitLab, model.GitProviderGitea, model.GitProviderGitee:
		return true
	default:
		return false
	}
}

func secretHint(value string) string {
	runes := []rune(value)
	if len(runes) <= 4 {
		return "••••"
	}
	return "••••" + string(runes[len(runes)-4:])
}

func credentialAAD(id string) []byte { return []byte("git_credential:" + id + ":secret") }
