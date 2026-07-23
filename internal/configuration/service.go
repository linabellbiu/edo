package configuration

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
	ErrInvalidConfiguration  = errors.New("配置参数无效")
	ErrConfigurationExists   = errors.New("配置项已存在")
	ErrConfigurationNotFound = errors.New("配置项不存在")
	ErrVersionConflict       = errors.New("配置已被其他用户修改，请刷新后重试")
)

var namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
var keyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

type Input struct {
	Namespace   string
	Environment model.EnvironmentType
	Key         string
	Value       *string
	IsSecret    bool
}

type UpdateInput struct {
	Value           *string
	IsSecret        bool
	ExpectedVersion int
}

type View struct {
	ID          string                `json:"id"`
	Namespace   string                `json:"namespace"`
	Environment model.EnvironmentType `json:"environment"`
	Key         string                `json:"key"`
	Value       *string               `json:"value,omitempty"`
	IsSecret    bool                  `json:"is_secret"`
	HasValue    bool                  `json:"has_value"`
	Version     int                   `json:"version"`
	IsActive    bool                  `json:"is_active"`
	CreatedBy   string                `json:"created_by"`
	UpdatedBy   string                `json:"updated_by"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
}

type RevisionView struct {
	ID          string                `json:"id"`
	Version     int                   `json:"version"`
	Namespace   string                `json:"namespace"`
	Environment model.EnvironmentType `json:"environment"`
	Key         string                `json:"key"`
	IsSecret    bool                  `json:"is_secret"`
	HasValue    bool                  `json:"has_value"`
	IsActive    bool                  `json:"is_active"`
	ChangedBy   string                `json:"changed_by"`
	CreatedAt   time.Time             `json:"created_at"`
}

type Service struct {
	db      *gorm.DB
	secrets *secret.Manager
}

func NewService(db *gorm.DB, secrets *secret.Manager) *Service {
	return &Service{db: db, secrets: secrets}
}

func (s *Service) List(ctx context.Context, namespace string, environment model.EnvironmentType) ([]View, error) {
	query := s.db.WithContext(ctx).Order("namespace ASC, environment ASC, key ASC")
	if namespace != "" {
		query = query.Where("namespace = ?", strings.TrimSpace(namespace))
	}
	if environment != "" {
		query = query.Where("environment = ?", environment)
	}
	var items []model.Configuration
	if err := query.Find(&items).Error; err != nil {
		return nil, fmt.Errorf("查询配置失败: %w", err)
	}
	views := make([]View, 0, len(items))
	for index := range items {
		views = append(views, toView(&items[index]))
	}
	return views, nil
}

func (s *Service) Create(ctx context.Context, actorID string, input Input) (*View, error) {
	normalized, err := normalizeInput(input)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	value, ciphertext, err := s.protectValue(id, normalized.IsSecret, *normalized.Value)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	item := &model.Configuration{
		ID: id, Namespace: normalized.Namespace, Environment: normalized.Environment,
		Key: normalized.Key, Value: value, SecretCiphertext: ciphertext,
		IsSecret: normalized.IsSecret, Version: 1, IsActive: true,
		CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(item).Error; err != nil {
			return err
		}
		return tx.Create(revisionFrom(item, actorID)).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrConfigurationExists
		}
		return nil, fmt.Errorf("创建配置失败: %w", err)
	}
	view := toView(item)
	return &view, nil
}

func (s *Service) Update(ctx context.Context, id, actorID string, input UpdateInput) (*View, error) {
	if input.ExpectedVersion < 1 || input.Value == nil {
		return nil, ErrInvalidConfiguration
	}
	valueText := *input.Value
	if utf8.RuneCountInString(valueText) > 65536 {
		return nil, ErrInvalidConfiguration
	}
	var updated model.Configuration
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Configuration
		if err := tx.First(&current, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConfigurationNotFound
			}
			return err
		}
		if current.Version != input.ExpectedVersion {
			return ErrVersionConflict
		}
		value, ciphertext, err := s.protectValue(current.ID, input.IsSecret, valueText)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		nextVersion := current.Version + 1
		result := tx.Model(&model.Configuration{}).
			Where("id = ? AND version = ?", current.ID, current.Version).
			Updates(map[string]any{
				"value": value, "secret_ciphertext": ciphertext, "is_secret": input.IsSecret,
				"version": nextVersion, "updated_by": actorID, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrVersionConflict
		}
		current.Value, current.SecretCiphertext, current.IsSecret = value, ciphertext, input.IsSecret
		current.Version, current.UpdatedBy, current.UpdatedAt = nextVersion, actorID, now
		if err := tx.Create(revisionFrom(&current, actorID)).Error; err != nil {
			return err
		}
		updated = current
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrConfigurationNotFound) || errors.Is(err, ErrVersionConflict) || errors.Is(err, ErrInvalidConfiguration) || errors.Is(err, secret.ErrUnavailable) {
			return nil, err
		}
		return nil, fmt.Errorf("更新配置失败: %w", err)
	}
	view := toView(&updated)
	return &view, nil
}

func (s *Service) SetActive(ctx context.Context, id, actorID string, active bool, expectedVersion int) (*View, error) {
	if expectedVersion < 1 {
		return nil, ErrInvalidConfiguration
	}
	var updated model.Configuration
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&updated, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConfigurationNotFound
			}
			return err
		}
		if updated.Version != expectedVersion {
			return ErrVersionConflict
		}
		now := time.Now().UTC()
		result := tx.Model(&model.Configuration{}).Where("id = ? AND version = ?", id, expectedVersion).
			Updates(map[string]any{"is_active": active, "version": expectedVersion + 1, "updated_by": actorID, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrVersionConflict
		}
		updated.IsActive, updated.Version, updated.UpdatedBy, updated.UpdatedAt = active, expectedVersion+1, actorID, now
		return tx.Create(revisionFrom(&updated, actorID)).Error
	})
	if err != nil {
		if errors.Is(err, ErrConfigurationNotFound) || errors.Is(err, ErrVersionConflict) {
			return nil, err
		}
		return nil, fmt.Errorf("修改配置状态失败: %w", err)
	}
	view := toView(&updated)
	return &view, nil
}

func (s *Service) Revisions(ctx context.Context, id string, limit int) ([]RevisionView, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.Configuration{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return nil, fmt.Errorf("查询配置失败: %w", err)
	}
	if count == 0 {
		return nil, ErrConfigurationNotFound
	}
	var revisions []model.ConfigurationRevision
	if err := s.db.WithContext(ctx).Where("configuration_id = ?", id).Order("version DESC").Limit(limit).Find(&revisions).Error; err != nil {
		return nil, fmt.Errorf("查询配置修订失败: %w", err)
	}
	result := make([]RevisionView, 0, len(revisions))
	for _, revision := range revisions {
		result = append(result, RevisionView{
			ID: revision.ID, Version: revision.Version, Namespace: revision.Namespace,
			Environment: revision.Environment, Key: revision.Key, IsSecret: revision.IsSecret,
			HasValue: revision.Value != "" || revision.SecretCiphertext != "", IsActive: revision.IsActive,
			ChangedBy: revision.ChangedBy, CreatedAt: revision.CreatedAt,
		})
	}
	return result, nil
}

func (s *Service) Resolve(ctx context.Context, namespace string, environment model.EnvironmentType) (map[string]string, error) {
	if !namespacePattern.MatchString(namespace) || !validEnvironment(environment) {
		return nil, ErrInvalidConfiguration
	}
	var items []model.Configuration
	if err := s.db.WithContext(ctx).Where(
		"namespace = ? AND environment IN ? AND is_active = ?", namespace,
		[]model.EnvironmentType{model.EnvironmentGlobal, environment}, true,
	).Order("key ASC").Find(&items).Error; err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	result := make(map[string]string, len(items))
	for _, currentEnvironment := range []model.EnvironmentType{model.EnvironmentGlobal, environment} {
		for _, item := range items {
			if item.Environment != currentEnvironment {
				continue
			}
			value := item.Value
			if item.IsSecret {
				decrypted, err := s.secrets.Decrypt(item.SecretCiphertext, configurationAAD(item.ID))
				if err != nil {
					return nil, fmt.Errorf("解密配置 %s 失败: %w", item.ID, err)
				}
				value = decrypted
			}
			result[item.Key] = value
		}
	}
	return result, nil
}

func normalizeInput(input Input) (Input, error) {
	input.Namespace = strings.TrimSpace(input.Namespace)
	input.Key = strings.TrimSpace(input.Key)
	if !namespacePattern.MatchString(input.Namespace) || !keyPattern.MatchString(input.Key) ||
		!validEnvironment(input.Environment) || input.Value == nil || utf8.RuneCountInString(*input.Value) > 65536 {
		return Input{}, ErrInvalidConfiguration
	}
	return input, nil
}

func validEnvironment(value model.EnvironmentType) bool {
	return value == model.EnvironmentGlobal || value == model.EnvironmentDevelopment ||
		value == model.EnvironmentStaging || value == model.EnvironmentProduction
}

func (s *Service) protectValue(id string, isSecret bool, value string) (string, string, error) {
	if !isSecret {
		return value, "", nil
	}
	ciphertext, err := s.secrets.Encrypt(value, configurationAAD(id))
	if err != nil {
		return "", "", err
	}
	return "", ciphertext, nil
}

func toView(item *model.Configuration) View {
	view := View{
		ID: item.ID, Namespace: item.Namespace, Environment: item.Environment, Key: item.Key,
		IsSecret: item.IsSecret, HasValue: item.Value != "" || item.SecretCiphertext != "",
		Version: item.Version, IsActive: item.IsActive, CreatedBy: item.CreatedBy,
		UpdatedBy: item.UpdatedBy, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
	if !item.IsSecret {
		value := item.Value
		view.Value = &value
	}
	return view
}

func revisionFrom(item *model.Configuration, actorID string) *model.ConfigurationRevision {
	return &model.ConfigurationRevision{
		ID: uuid.NewString(), ConfigurationID: item.ID, Version: item.Version,
		Namespace: item.Namespace, Environment: item.Environment, Key: item.Key,
		Value: item.Value, SecretCiphertext: item.SecretCiphertext, IsSecret: item.IsSecret,
		IsActive: item.IsActive, ChangedBy: actorID, CreatedAt: item.UpdatedAt,
	}
}

func configurationAAD(id string) []byte { return []byte("configuration:" + id + ":value") }
