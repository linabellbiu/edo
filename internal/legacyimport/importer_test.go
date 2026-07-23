package legacyimport

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/model"
	"zrt/internal/secret"
)

func TestImporterMigratesSafeDataAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t, "source")
	destination := openTestDatabase(t, "destination")
	if err := database.Migrate(ctx, destination); err != nil {
		t.Fatalf("迁移目标数据库失败: %v", err)
	}
	createLegacyFixture(t, source)
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	manager, err := secret.New(key)
	if err != nil {
		t.Fatalf("初始化密钥管理器失败: %v", err)
	}

	dryReport, err := New(source, destination, manager, true).Run(ctx)
	if err != nil {
		t.Fatalf("预检旧数据失败: %v", err)
	}
	if dryReport.Users.Planned != 1 || dryReport.Roles.Planned != 1 || dryReport.Configurations.Planned != 1 {
		t.Fatalf("预检统计不正确: %+v", dryReport)
	}
	var userCount int64
	if err := destination.Model(&model.User{}).Count(&userCount).Error; err != nil || userCount != 0 {
		t.Fatalf("预检写入了用户: count=%d err=%v", userCount, err)
	}

	report, err := New(source, destination, manager, false).Run(ctx)
	if err != nil {
		t.Fatalf("迁移旧数据失败: %v", err)
	}
	if report.Users.Created != 1 || report.Roles.Created != 1 || report.UserRoles.Created != 1 ||
		report.Configurations.Created != 1 || report.Repositories.Created != 1 {
		t.Fatalf("迁移统计不正确: %+v", report)
	}
	if report.HostRecordsOmitted != 1 || report.UnsafeSchedulesOmitted != 1 || report.ScriptMonitorsOmitted != 1 {
		t.Fatalf("高风险数据统计不正确: %+v", report)
	}
	var user model.User
	if err := destination.First(&user).Error; err != nil {
		t.Fatalf("读取迁移用户失败: %v", err)
	}
	if user.Username != "admin" || user.IsActive || !user.IsSuperuser {
		t.Fatalf("迁移用户的安全状态不正确: %+v", user)
	}
	var configuration model.Configuration
	if err := destination.First(&configuration).Error; err != nil {
		t.Fatalf("读取迁移配置失败: %v", err)
	}
	if !configuration.IsSecret || configuration.Value != "" || configuration.SecretCiphertext == "legacy-secret" {
		t.Fatalf("迁移配置未加密: %+v", configuration)
	}
	plaintext, err := manager.Decrypt(configuration.SecretCiphertext, []byte("configuration:"+configuration.ID+":value"))
	if err != nil || plaintext != "legacy-secret" {
		t.Fatalf("迁移配置解密结果不正确: value=%q err=%v", plaintext, err)
	}
	var repository model.GitRepository
	if err := destination.First(&repository).Error; err != nil {
		t.Fatalf("读取迁移仓库失败: %v", err)
	}
	if repository.IsActive || repository.Provider != model.GitProviderGitHub {
		t.Fatalf("迁移仓库应等待凭据复核: %+v", repository)
	}

	second, err := New(source, destination, manager, false).Run(ctx)
	if err != nil {
		t.Fatalf("重复执行迁移失败: %v", err)
	}
	if second.Users.Existing != 1 || second.Roles.Existing != 1 || second.Configurations.Existing != 1 || second.Repositories.Existing != 1 {
		t.Fatalf("重复迁移不是幂等的: %+v", second)
	}
}

func TestImporterRequiresEncryptionKeyForConfigurations(t *testing.T) {
	source := openTestDatabase(t, "no-key-source")
	destination := openTestDatabase(t, "no-key-destination")
	if err := database.Migrate(context.Background(), destination); err != nil {
		t.Fatalf("迁移目标数据库失败: %v", err)
	}
	createLegacyFixture(t, source)
	manager, err := secret.New("")
	if err != nil {
		t.Fatalf("初始化空密钥管理器失败: %v", err)
	}
	if _, err := New(source, destination, manager, false).Run(context.Background()); err != ErrSecretsRequired {
		t.Fatalf("未配置加密密钥时应拒绝迁移配置，得到: %v", err)
	}
}

func openTestDatabase(t *testing.T, suffix string) *gorm.DB {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:" + t.Name() + "-" + suffix + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	return db
}

func createLegacyFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, nickname TEXT, password_hash TEXT, is_supper BOOLEAN, is_active BOOLEAN, last_login TEXT, created_at TEXT, deleted_at TEXT)`,
		`CREATE TABLE roles (id INTEGER PRIMARY KEY, name TEXT, desc TEXT, page_perms TEXT, deploy_perms TEXT, group_perms TEXT, created_at TEXT)`,
		`CREATE TABLE user_role_rel (id INTEGER PRIMARY KEY, user_id INTEGER, role_id INTEGER)`,
		`CREATE TABLE environments (id INTEGER PRIMARY KEY, name TEXT, key TEXT, prod BOOLEAN)`,
		`CREATE TABLE apps (id INTEGER PRIMARY KEY, key TEXT)`,
		`CREATE TABLE services (id INTEGER PRIMARY KEY, key TEXT)`,
		`CREATE TABLE configs (id INTEGER PRIMARY KEY, type TEXT, o_id INTEGER, key TEXT, env_id INTEGER, value TEXT, updated_at TEXT, updated_by_id INTEGER)`,
		`CREATE TABLE deploy_extend1 (deploy_id INTEGER PRIMARY KEY, git_repo TEXT)`,
		`CREATE TABLE hosts (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE tasks (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE detections (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE deploys (id INTEGER PRIMARY KEY)`,
		`INSERT INTO users VALUES (1, 'Admin', '旧管理员', 'pbkdf2_sha256$legacy', 1, 1, '2025-01-02 03:04:05', '2024-01-01 00:00:00', NULL)`,
		`INSERT INTO roles VALUES (2, '运维管理员', '旧角色', '{"deploy":{"view":["read"]}}', NULL, NULL, '2024-01-01 00:00:00')`,
		`INSERT INTO user_role_rel VALUES (1, 1, 2)`,
		`INSERT INTO environments VALUES (3, '生产', 'prod', 1)`,
		`INSERT INTO apps VALUES (4, 'checkout-api')`,
		`INSERT INTO services VALUES (5, 'shared')`,
		`INSERT INTO configs VALUES (6, 'app', 4, '_ZRT_DATABASE_URL', 3, 'legacy-secret', '2025-01-01 00:00:00', 1)`,
		`INSERT INTO deploy_extend1 VALUES (7, 'https://github.com/example/project.git')`,
		`INSERT INTO hosts VALUES (1)`,
		`INSERT INTO tasks VALUES (1)`,
		`INSERT INTO detections VALUES (1)`,
		`INSERT INTO deploys VALUES (1)`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("构造旧数据库失败: %v", err)
		}
	}
}
