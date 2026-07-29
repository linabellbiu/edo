package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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

const (
	systemNamespace              = "zrt"
	externalGitWebhookSettingKey = "EXTERNAL_GIT_WEBHOOK_ENABLED"
	loginLockoutSettingKey       = "LOGIN_LOCKOUT_ENABLED"
	runtimeLoggingSettingKey     = "RUNTIME_LOGGING_SETTINGS"
	logRetentionSettingKey       = "LOG_RETENTION_SETTINGS"
	defaultPipelineLogDays       = 30
	defaultAuditLogDays          = 180
)

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

type ExternalGitWebhookSettings struct {
	Enabled bool `json:"enabled"`
	Version int  `json:"version"`
}

type LoginLockoutSettings struct {
	Enabled bool `json:"enabled"`
	Version int  `json:"version"`
}

type LogRetentionSettings struct {
	Enabled         bool `json:"enabled"`
	PipelineLogDays int  `json:"pipeline_log_days"`
	AuditLogDays    int  `json:"audit_log_days"`
	Version         int  `json:"version"`
}

type RuntimeLoggingSettings struct {
	Level             string `json:"level"`
	HTTPAccessEnabled bool   `json:"http_access_enabled"`
	Version           int    `json:"version"`
}

type runtimeLoggingValue struct {
	Level             string `json:"level"`
	HTTPAccessEnabled bool   `json:"http_access_enabled"`
}

type logRetentionValue struct {
	Enabled         bool `json:"enabled"`
	PipelineLogDays int  `json:"pipeline_log_days"`
	AuditLogDays    int  `json:"audit_log_days"`
}

type systemBooleanSettings struct {
	Enabled bool
	Version int
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

// ExternalGitWebhookEnabled 实现仓库 Webhook 的全局安全开关。配置缺失时保持关闭，
// 避免新安装或升级后在管理员未确认公网入口前直接暴露接收端点。
func (s *Service) ExternalGitWebhookEnabled(ctx context.Context) (bool, error) {
	settings, err := s.GetExternalGitWebhookSettings(ctx)
	if err != nil {
		return false, err
	}
	return settings.Enabled, nil
}

func (s *Service) GetExternalGitWebhookSettings(ctx context.Context) (ExternalGitWebhookSettings, error) {
	settings, err := s.getSystemBooleanSettings(ctx, externalGitWebhookSettingKey, "外部 Git Webhook")
	return ExternalGitWebhookSettings(settings), err
}

func (s *Service) UpdateExternalGitWebhookSettings(
	ctx context.Context,
	actorID string,
	enabled bool,
	expectedVersion int,
) (ExternalGitWebhookSettings, error) {
	settings, err := s.updateSystemBooleanSettings(ctx, actorID, externalGitWebhookSettingKey, "外部 Git Webhook", enabled, expectedVersion)
	return ExternalGitWebhookSettings(settings), err
}

// LoginLockoutEnabled 供登录入口读取动态开关；配置缺失时默认关闭。
func (s *Service) LoginLockoutEnabled(ctx context.Context) (bool, error) {
	settings, err := s.GetLoginLockoutSettings(ctx)
	if err != nil {
		return false, err
	}
	return settings.Enabled, nil
}

func (s *Service) GetLoginLockoutSettings(ctx context.Context) (LoginLockoutSettings, error) {
	settings, err := s.getSystemBooleanSettings(ctx, loginLockoutSettingKey, "登录锁定")
	return LoginLockoutSettings(settings), err
}

func (s *Service) UpdateLoginLockoutSettings(
	ctx context.Context,
	actorID string,
	enabled bool,
	expectedVersion int,
) (LoginLockoutSettings, error) {
	settings, err := s.updateSystemBooleanSettings(ctx, actorID, loginLockoutSettingKey, "登录锁定", enabled, expectedVersion)
	return LoginLockoutSettings(settings), err
}

// GetLogRetentionSettings 在尚未配置时返回安全的建议值，但默认不启用自动删除。
// 这避免升级后在管理员未确认保留周期前清理历史记录。
func (s *Service) GetLogRetentionSettings(ctx context.Context) (LogRetentionSettings, error) {
	defaults := LogRetentionSettings{PipelineLogDays: defaultPipelineLogDays, AuditLogDays: defaultAuditLogDays}
	var item model.Configuration
	err := s.db.WithContext(ctx).Where(
		"namespace = ? AND environment = ? AND key = ?",
		systemNamespace, model.EnvironmentGlobal, logRetentionSettingKey,
	).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return defaults, nil
	}
	if err != nil {
		return LogRetentionSettings{}, fmt.Errorf("读取日志保留设置失败: %w", err)
	}
	if item.IsSecret || item.SecretCiphertext != "" || !item.IsActive {
		return LogRetentionSettings{}, ErrInvalidConfiguration
	}
	var value logRetentionValue
	if err := json.Unmarshal([]byte(item.Value), &value); err != nil || !validLogRetention(value) {
		return LogRetentionSettings{}, ErrInvalidConfiguration
	}
	return LogRetentionSettings{
		Enabled: value.Enabled, PipelineLogDays: value.PipelineLogDays,
		AuditLogDays: value.AuditLogDays, Version: item.Version,
	}, nil
}

// GetRuntimeLoggingSettings 使用启动配置作为首次运行的默认值；一旦管理员保存，
// 后续启动以数据库中的设置为准。
func (s *Service) GetRuntimeLoggingSettings(
	ctx context.Context,
	defaultLevel string,
	defaultHTTPAccess bool,
) (RuntimeLoggingSettings, error) {
	defaultLevel = normalizeRuntimeLogLevel(defaultLevel)
	if !validRuntimeLogging(runtimeLoggingValue{Level: defaultLevel}) {
		defaultLevel = "info"
	}
	defaults := RuntimeLoggingSettings{Level: defaultLevel, HTTPAccessEnabled: defaultHTTPAccess}
	var item model.Configuration
	err := s.db.WithContext(ctx).Where(
		"namespace = ? AND environment = ? AND key = ?",
		systemNamespace, model.EnvironmentGlobal, runtimeLoggingSettingKey,
	).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return defaults, nil
	}
	if err != nil {
		return RuntimeLoggingSettings{}, fmt.Errorf("读取运行日志设置失败: %w", err)
	}
	if item.IsSecret || item.SecretCiphertext != "" || !item.IsActive {
		return RuntimeLoggingSettings{}, ErrInvalidConfiguration
	}
	var value runtimeLoggingValue
	if err := json.Unmarshal([]byte(item.Value), &value); err != nil || !validRuntimeLogging(value) {
		return RuntimeLoggingSettings{}, ErrInvalidConfiguration
	}
	return RuntimeLoggingSettings{
		Level: value.Level, HTTPAccessEnabled: value.HTTPAccessEnabled, Version: item.Version,
	}, nil
}

func (s *Service) UpdateRuntimeLoggingSettings(
	ctx context.Context,
	actorID, level string,
	httpAccessEnabled bool,
	expectedVersion int,
) (RuntimeLoggingSettings, error) {
	value := runtimeLoggingValue{
		Level: normalizeRuntimeLogLevel(level), HTTPAccessEnabled: httpAccessEnabled,
	}
	if expectedVersion < 0 || !validRuntimeLogging(value) {
		return RuntimeLoggingSettings{}, ErrInvalidConfiguration
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return RuntimeLoggingSettings{}, fmt.Errorf("序列化运行日志设置失败: %w", err)
	}
	var updated model.Configuration
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Configuration
		err := tx.Where(
			"namespace = ? AND environment = ? AND key = ?",
			systemNamespace, model.EnvironmentGlobal, runtimeLoggingSettingKey,
		).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if expectedVersion != 0 {
				return ErrVersionConflict
			}
			now := time.Now().UTC()
			current = model.Configuration{
				ID: uuid.NewString(), Namespace: systemNamespace, Environment: model.EnvironmentGlobal,
				Key: runtimeLoggingSettingKey, Value: string(encoded), Version: 1, IsActive: true,
				CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&current).Error; err != nil {
				if errors.Is(err, gorm.ErrDuplicatedKey) {
					return ErrVersionConflict
				}
				return err
			}
			if err := tx.Create(revisionFrom(&current, actorID)).Error; err != nil {
				return err
			}
			updated = current
			return nil
		}
		if err != nil {
			return err
		}
		if current.Version != expectedVersion {
			return ErrVersionConflict
		}
		now := time.Now().UTC()
		nextVersion := current.Version + 1
		result := tx.Model(&model.Configuration{}).Where("id = ? AND version = ?", current.ID, current.Version).
			Updates(map[string]any{
				"value": string(encoded), "secret_ciphertext": "", "is_secret": false,
				"is_active": true, "version": nextVersion, "updated_by": actorID, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrVersionConflict
		}
		current.Value, current.SecretCiphertext, current.IsSecret, current.IsActive = string(encoded), "", false, true
		current.Version, current.UpdatedBy, current.UpdatedAt = nextVersion, actorID, now
		if err := tx.Create(revisionFrom(&current, actorID)).Error; err != nil {
			return err
		}
		updated = current
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrVersionConflict) || errors.Is(err, ErrInvalidConfiguration) {
			return RuntimeLoggingSettings{}, err
		}
		return RuntimeLoggingSettings{}, fmt.Errorf("更新运行日志设置失败: %w", err)
	}
	return RuntimeLoggingSettings{
		Level: value.Level, HTTPAccessEnabled: value.HTTPAccessEnabled, Version: updated.Version,
	}, nil
}

func validRuntimeLogging(value runtimeLoggingValue) bool {
	return value.Level == "debug" || value.Level == "info" || value.Level == "warn" || value.Level == "error"
}

func normalizeRuntimeLogLevel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "warning" {
		return "warn"
	}
	return value
}

func (s *Service) UpdateLogRetentionSettings(
	ctx context.Context,
	actorID string,
	enabled bool,
	pipelineLogDays, auditLogDays, expectedVersion int,
) (LogRetentionSettings, error) {
	value := logRetentionValue{Enabled: enabled, PipelineLogDays: pipelineLogDays, AuditLogDays: auditLogDays}
	if expectedVersion < 0 || !validLogRetention(value) {
		return LogRetentionSettings{}, ErrInvalidConfiguration
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return LogRetentionSettings{}, fmt.Errorf("序列化日志保留设置失败: %w", err)
	}
	var updated model.Configuration
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Configuration
		err := tx.Where(
			"namespace = ? AND environment = ? AND key = ?",
			systemNamespace, model.EnvironmentGlobal, logRetentionSettingKey,
		).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if expectedVersion != 0 {
				return ErrVersionConflict
			}
			now := time.Now().UTC()
			current = model.Configuration{
				ID: uuid.NewString(), Namespace: systemNamespace, Environment: model.EnvironmentGlobal,
				Key: logRetentionSettingKey, Value: string(encoded), Version: 1, IsActive: true,
				CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&current).Error; err != nil {
				if errors.Is(err, gorm.ErrDuplicatedKey) {
					return ErrVersionConflict
				}
				return err
			}
			if err := tx.Create(revisionFrom(&current, actorID)).Error; err != nil {
				return err
			}
			updated = current
			return nil
		}
		if err != nil {
			return err
		}
		if current.Version != expectedVersion {
			return ErrVersionConflict
		}
		now := time.Now().UTC()
		nextVersion := current.Version + 1
		result := tx.Model(&model.Configuration{}).Where("id = ? AND version = ?", current.ID, current.Version).
			Updates(map[string]any{
				"value": string(encoded), "secret_ciphertext": "", "is_secret": false,
				"is_active": true, "version": nextVersion, "updated_by": actorID, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrVersionConflict
		}
		current.Value, current.SecretCiphertext, current.IsSecret, current.IsActive = string(encoded), "", false, true
		current.Version, current.UpdatedBy, current.UpdatedAt = nextVersion, actorID, now
		if err := tx.Create(revisionFrom(&current, actorID)).Error; err != nil {
			return err
		}
		updated = current
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrVersionConflict) || errors.Is(err, ErrInvalidConfiguration) {
			return LogRetentionSettings{}, err
		}
		return LogRetentionSettings{}, fmt.Errorf("更新日志保留设置失败: %w", err)
	}
	return LogRetentionSettings{
		Enabled: enabled, PipelineLogDays: pipelineLogDays,
		AuditLogDays: auditLogDays, Version: updated.Version,
	}, nil
}

func validLogRetention(value logRetentionValue) bool {
	return value.PipelineLogDays >= 1 && value.PipelineLogDays <= 3650 &&
		value.AuditLogDays >= 1 && value.AuditLogDays <= 3650
}

func (s *Service) getSystemBooleanSettings(ctx context.Context, key, label string) (systemBooleanSettings, error) {
	var item model.Configuration
	err := s.db.WithContext(ctx).Where(
		"namespace = ? AND environment = ? AND key = ?",
		systemNamespace, model.EnvironmentGlobal, key,
	).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return systemBooleanSettings{}, nil
	}
	if err != nil {
		return systemBooleanSettings{}, fmt.Errorf("读取%s设置失败: %w", label, err)
	}
	if !item.IsActive {
		return systemBooleanSettings{Version: item.Version}, nil
	}
	if item.IsSecret || item.SecretCiphertext != "" {
		return systemBooleanSettings{}, ErrInvalidConfiguration
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(item.Value))
	if err != nil {
		return systemBooleanSettings{}, ErrInvalidConfiguration
	}
	return systemBooleanSettings{Enabled: enabled, Version: item.Version}, nil
}

func (s *Service) updateSystemBooleanSettings(
	ctx context.Context,
	actorID, key, label string,
	enabled bool,
	expectedVersion int,
) (systemBooleanSettings, error) {
	if expectedVersion < 0 {
		return systemBooleanSettings{}, ErrInvalidConfiguration
	}
	value := strconv.FormatBool(enabled)
	var updated model.Configuration
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Configuration
		err := tx.Where(
			"namespace = ? AND environment = ? AND key = ?",
			systemNamespace, model.EnvironmentGlobal, key,
		).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if expectedVersion != 0 {
				return ErrVersionConflict
			}
			now := time.Now().UTC()
			current = model.Configuration{
				ID: uuid.NewString(), Namespace: systemNamespace, Environment: model.EnvironmentGlobal,
				Key: key, Value: value, Version: 1, IsActive: true,
				CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&current).Error; err != nil {
				if errors.Is(err, gorm.ErrDuplicatedKey) {
					return ErrVersionConflict
				}
				return err
			}
			if err := tx.Create(revisionFrom(&current, actorID)).Error; err != nil {
				return err
			}
			updated = current
			return nil
		}
		if err != nil {
			return err
		}
		if current.Version != expectedVersion {
			return ErrVersionConflict
		}
		now := time.Now().UTC()
		nextVersion := current.Version + 1
		result := tx.Model(&model.Configuration{}).
			Where("id = ? AND version = ?", current.ID, current.Version).
			Updates(map[string]any{
				"value": value, "secret_ciphertext": "", "is_secret": false, "is_active": true,
				"version": nextVersion, "updated_by": actorID, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrVersionConflict
		}
		current.Value, current.SecretCiphertext, current.IsSecret, current.IsActive = value, "", false, true
		current.Version, current.UpdatedBy, current.UpdatedAt = nextVersion, actorID, now
		if err := tx.Create(revisionFrom(&current, actorID)).Error; err != nil {
			return err
		}
		updated = current
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrVersionConflict) || errors.Is(err, ErrInvalidConfiguration) {
			return systemBooleanSettings{}, err
		}
		return systemBooleanSettings{}, fmt.Errorf("更新%s设置失败: %w", label, err)
	}
	return systemBooleanSettings{Enabled: enabled, Version: updated.Version}, nil
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
