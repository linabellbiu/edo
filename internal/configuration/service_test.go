package configuration

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
)

func TestSecretConfigurationIsEncryptedAndNeverReturned(t *testing.T) {
	service := newConfigurationTestService(t)
	secretValue := "postgres://user:password@database/edo"
	created, err := service.Create(context.Background(), "admin", Input{
		Namespace: "payment", Environment: model.EnvironmentProduction,
		Key: "DATABASE_URL", Value: &secretValue, IsSecret: true,
	})
	if err != nil {
		t.Fatalf("创建密钥配置失败: %v", err)
	}
	if created.Value != nil || !created.IsSecret || !created.HasValue {
		t.Fatalf("密钥配置被 API 回显或状态错误: %+v", created)
	}
	var stored model.Configuration
	if err := service.db.First(&stored, "id = ?", created.ID).Error; err != nil {
		t.Fatalf("读取密钥配置失败: %v", err)
	}
	if stored.Value != "" || stored.SecretCiphertext == "" || stored.SecretCiphertext == secretValue {
		t.Fatal("密钥配置未正确加密")
	}
	resolved, err := service.Resolve(context.Background(), "payment", model.EnvironmentProduction)
	if err != nil {
		t.Fatalf("解析密钥配置失败: %v", err)
	}
	if resolved["DATABASE_URL"] != secretValue {
		t.Fatal("解析后的密钥配置内容错误")
	}
}

func TestConfigurationVersionAndEnvironmentOverride(t *testing.T) {
	service := newConfigurationTestService(t)
	globalValue := "30"
	global, err := service.Create(context.Background(), "admin", Input{
		Namespace: "checkout", Environment: model.EnvironmentGlobal,
		Key: "REQUEST_TIMEOUT", Value: &globalValue,
	})
	if err != nil {
		t.Fatalf("创建全局配置失败: %v", err)
	}
	productionValue := "60"
	production, err := service.Create(context.Background(), "admin", Input{
		Namespace: "checkout", Environment: model.EnvironmentProduction,
		Key: "REQUEST_TIMEOUT", Value: &productionValue,
	})
	if err != nil {
		t.Fatalf("创建生产配置失败: %v", err)
	}
	resolved, err := service.Resolve(context.Background(), "checkout", model.EnvironmentProduction)
	if err != nil || resolved["REQUEST_TIMEOUT"] != "60" {
		t.Fatalf("环境配置未覆盖全局配置: values=%v err=%v", resolved, err)
	}
	newValue := "90"
	if _, err := service.Update(context.Background(), production.ID, "operator", UpdateInput{
		Value: &newValue, ExpectedVersion: 99,
	}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("过期配置版本未被拒绝: %v", err)
	}
	updated, err := service.Update(context.Background(), production.ID, "operator", UpdateInput{
		Value: &newValue, ExpectedVersion: production.Version,
	})
	if err != nil || updated.Version != 2 || updated.Value == nil || *updated.Value != "90" {
		t.Fatalf("更新配置失败: configuration=%+v err=%v", updated, err)
	}
	revisions, err := service.Revisions(context.Background(), production.ID, 10)
	if err != nil || len(revisions) != 2 || revisions[0].Version != 2 {
		t.Fatalf("配置修订记录错误: revisions=%+v err=%v", revisions, err)
	}
	if global.Version != 1 {
		t.Fatal("全局配置版本被意外修改")
	}
}

func TestExternalGitWebhookSettingDefaultsOffAndUsesOptimisticVersion(t *testing.T) {
	service := newConfigurationTestService(t)

	initial, err := service.GetExternalGitWebhookSettings(context.Background())
	if err != nil || initial.Enabled || initial.Version != 0 {
		t.Fatalf("外部 Git Webhook 默认状态错误: settings=%+v err=%v", initial, err)
	}
	if enabled, err := service.ExternalGitWebhookEnabled(context.Background()); err != nil || enabled {
		t.Fatalf("缺少设置时外部 Git Webhook 未保持关闭: enabled=%v err=%v", enabled, err)
	}

	enabled, err := service.UpdateExternalGitWebhookSettings(context.Background(), "admin", true, 0)
	if err != nil || !enabled.Enabled || enabled.Version != 1 {
		t.Fatalf("启用外部 Git Webhook 失败: settings=%+v err=%v", enabled, err)
	}
	if _, err := service.UpdateExternalGitWebhookSettings(context.Background(), "admin", false, 0); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("过期设置版本未被拒绝: %v", err)
	}
	disabled, err := service.UpdateExternalGitWebhookSettings(context.Background(), "admin", false, enabled.Version)
	if err != nil || disabled.Enabled || disabled.Version != 2 {
		t.Fatalf("关闭外部 Git Webhook 失败: settings=%+v err=%v", disabled, err)
	}
	revisions, err := service.Revisions(context.Background(), externalGitWebhookConfigurationID(t, service), 10)
	if err != nil || len(revisions) != 2 || revisions[0].Version != 2 {
		t.Fatalf("外部 Git Webhook 设置修订记录错误: revisions=%+v err=%v", revisions, err)
	}
}

func TestLoginLockoutSettingDefaultsOffAndUsesOptimisticVersion(t *testing.T) {
	service := newConfigurationTestService(t)

	initial, err := service.GetLoginLockoutSettings(context.Background())
	if err != nil || initial.Enabled || initial.Version != 0 {
		t.Fatalf("登录锁定默认状态错误: settings=%+v err=%v", initial, err)
	}
	if enabled, err := service.LoginLockoutEnabled(context.Background()); err != nil || enabled {
		t.Fatalf("缺少设置时登录锁定未保持关闭: enabled=%v err=%v", enabled, err)
	}

	enabled, err := service.UpdateLoginLockoutSettings(context.Background(), "admin", true, 0)
	if err != nil || !enabled.Enabled || enabled.Version != 1 {
		t.Fatalf("启用登录锁定失败: settings=%+v err=%v", enabled, err)
	}
	if _, err := service.UpdateLoginLockoutSettings(context.Background(), "admin", false, 0); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("过期设置版本未被拒绝: %v", err)
	}
	disabled, err := service.UpdateLoginLockoutSettings(context.Background(), "admin", false, enabled.Version)
	if err != nil || disabled.Enabled || disabled.Version != 2 {
		t.Fatalf("关闭登录锁定失败: settings=%+v err=%v", disabled, err)
	}
	revisions, err := service.Revisions(context.Background(), systemConfigurationID(t, service, loginLockoutSettingKey), 10)
	if err != nil || len(revisions) != 2 || revisions[0].Version != 2 {
		t.Fatalf("登录锁定设置修订记录错误: revisions=%+v err=%v", revisions, err)
	}
}

func TestLogRetentionSettingsDefaultDisabledAndValidateRange(t *testing.T) {
	service := newConfigurationTestService(t)
	settings, err := service.GetLogRetentionSettings(context.Background())
	if err != nil || settings.Enabled || settings.PipelineLogDays != 30 || settings.AuditLogDays != 180 || settings.Version != 0 {
		t.Fatalf("日志保留默认设置错误: settings=%+v err=%v", settings, err)
	}
	if _, err := service.UpdateLogRetentionSettings(context.Background(), "admin", true, 0, 180, 0); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("无效保留天数未被拒绝: %v", err)
	}
	updated, err := service.UpdateLogRetentionSettings(context.Background(), "admin", true, 45, 365, 0)
	if err != nil || !updated.Enabled || updated.PipelineLogDays != 45 || updated.AuditLogDays != 365 || updated.Version != 1 {
		t.Fatalf("保存日志保留设置失败: settings=%+v err=%v", updated, err)
	}
	if _, err := service.UpdateLogRetentionSettings(context.Background(), "admin", false, 30, 180, 0); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("过期日志设置版本未被拒绝: %v", err)
	}
}

func TestRuntimeLoggingSettingsUseStartupDefaultsAndOptimisticVersion(t *testing.T) {
	service := newConfigurationTestService(t)
	settings, err := service.GetRuntimeLoggingSettings(context.Background(), "warn", true)
	if err != nil || settings.Level != "warn" || !settings.HTTPAccessEnabled || !settings.FileEnabled ||
		settings.FileDirectory != "logs" || settings.MaxFileSizeMB != 100 || settings.CompressAfterDays != 3 || settings.Version != 0 {
		t.Fatalf("运行日志启动默认值错误: settings=%+v err=%v", settings, err)
	}
	if _, err := service.UpdateRuntimeLoggingSettings(
		context.Background(), "admin", "verbose", false, true, "logs", 100, 3, 0,
	); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("无效日志级别未被拒绝: %v", err)
	}
	updated, err := service.UpdateRuntimeLoggingSettings(
		context.Background(), "admin", "error", false, true, "data/runtime-logs", 256, 7, 0,
	)
	if err != nil || updated.Level != "error" || updated.HTTPAccessEnabled || !updated.FileEnabled ||
		updated.FileDirectory != "data/runtime-logs" || updated.MaxFileSizeMB != 256 || updated.CompressAfterDays != 7 || updated.Version != 1 {
		t.Fatalf("保存运行日志设置失败: settings=%+v err=%v", updated, err)
	}
	if _, err := service.UpdateRuntimeLoggingSettings(
		context.Background(), "admin", "info", true, true, "logs", 100, 3, 0,
	); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("过期运行日志设置版本未被拒绝: %v", err)
	}
	persisted, err := service.GetRuntimeLoggingSettings(context.Background(), "debug", true)
	if err != nil || persisted != updated {
		t.Fatalf("运行日志设置未持久化: settings=%+v err=%v", persisted, err)
	}
}

func TestRuntimeLoggingSettingsUpgradeLegacyStoredValueWithFileDefaults(t *testing.T) {
	service := newConfigurationTestService(t)
	now := time.Now().UTC()
	legacy := model.Configuration{
		ID: "legacy-runtime-logging", Namespace: systemNamespace, Environment: model.EnvironmentGlobal,
		Key: runtimeLoggingSettingKey, Value: `{"level":"error","http_access_enabled":false}`,
		Version: 4, IsActive: true, CreatedBy: "admin", UpdatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := service.db.Create(&legacy).Error; err != nil {
		t.Fatalf("create legacy runtime logging settings: %v", err)
	}

	settings, err := service.GetRuntimeLoggingSettings(context.Background(), "info", true, RuntimeLoggingSettings{
		FileEnabled: true, FileDirectory: "custom-logs", MaxFileSizeMB: 256, CompressAfterDays: 5,
	})
	if err != nil {
		t.Fatalf("read legacy runtime logging settings: %v", err)
	}
	if settings.Level != "error" || settings.HTTPAccessEnabled || !settings.FileEnabled ||
		settings.FileDirectory != "custom-logs" || settings.MaxFileSizeMB != 256 ||
		settings.CompressAfterDays != 5 || settings.Version != 4 {
		t.Fatalf("legacy runtime logging settings were not upgraded with file defaults: %+v", settings)
	}
}

func externalGitWebhookConfigurationID(t *testing.T, service *Service) string {
	return systemConfigurationID(t, service, externalGitWebhookSettingKey)
}

func systemConfigurationID(t *testing.T, service *Service, key string) string {
	t.Helper()
	var item model.Configuration
	if err := service.db.Where(
		"namespace = ? AND environment = ? AND key = ?",
		systemNamespace, model.EnvironmentGlobal, key,
	).First(&item).Error; err != nil {
		t.Fatalf("读取系统设置配置失败: %v", err)
	}
	return item.ID
}

func newConfigurationTestService(t *testing.T) *Service {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开配置测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移配置测试数据库失败: %v", err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	secretManager, err := secret.New(key)
	if err != nil {
		t.Fatalf("初始化配置测试密钥失败: %v", err)
	}
	return NewService(db, secretManager)
}
