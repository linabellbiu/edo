package database

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/model"
)

func TestCopyDatabaseSnapshotCopiesAllRowsWithoutSchemaHistory(t *testing.T) {
	ctx := context.Background()
	source := openTransferTestDatabase(t, "transfer-source")
	target := openTransferTestDatabase(t, "transfer-target")
	now := time.Now().UTC()
	user := model.User{
		ID: "user-1", Username: "operator", Nickname: "运维", PasswordHash: "hash",
		IsActive: true, AuthVersion: 3, CreatedAt: now, UpdatedAt: now,
	}
	if err := source.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	audit := model.AuditLog{
		ID: "audit-1", ActorUserID: &user.ID, Action: "test", ResourceType: "database",
		Result: model.AuditSucceeded, Metadata: datatypes.JSON(`{"ok":true}`), CreatedAt: now,
	}
	if err := source.Create(&audit).Error; err != nil {
		t.Fatal(err)
	}
	environment := model.Environment{
		ID: "environment-1", Name: "开发环境", Description: "主机归属",
		Level: model.EnvironmentDevelopment, IsActive: true, CreatedBy: user.ID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := source.Create(&environment).Error; err != nil {
		t.Fatal(err)
	}
	host := model.Host{
		ID: "host-1", Name: "开发主机", Mode: model.HostModeSSH,
		Address: "host.example.com", SSHPort: 22, SSHUsername: "deploy",
		SSHAuthType: model.SSHAuthPassword, SSHCredentialCiphertext: "host-ciphertext",
		SSHHostKeyFingerprint: "SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IsActive:              true, CreatedBy: user.ID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := source.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	membership := model.EnvironmentHost{
		EnvironmentID: environment.ID, HostID: host.ID, CreatedAt: now,
	}
	if err := source.Create(&membership).Error; err != nil {
		t.Fatal(err)
	}
	endpoint := model.DockerEndpoint{
		ID: "docker-1", HostID: host.ID, Name: "开发 Docker",
		Host: "ssh://deploy@host.example.com:22", SSHCredentialCiphertext: "endpoint-ciphertext",
		SSHHostKeyFingerprint: host.SSHHostKeyFingerprint,
		IsActive:              true, CreatedBy: user.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := source.Create(&endpoint).Error; err != nil {
		t.Fatal(err)
	}
	capability := model.HostCapability{
		HostID: host.ID, Kind: model.HostCapabilityDocker, RuntimeID: endpoint.ID,
		Status: model.HostCapabilityReady, Version: "27.0.0",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := source.Create(&capability).Error; err != nil {
		t.Fatal(err)
	}

	if err := copyDatabaseSnapshot(ctx, source, target, "sqlite", func(int, int, int64) {}); err != nil {
		t.Fatalf("复制数据快照失败: %v", err)
	}
	var copiedUser model.User
	if err := target.First(&copiedUser, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("目标库未找到用户: %v", err)
	}
	if copiedUser.AuthVersion != user.AuthVersion || copiedUser.Username != user.Username {
		t.Fatalf("用户数据不一致: %+v", copiedUser)
	}
	var copiedHost model.Host
	if err := target.First(&copiedHost, "id = ?", host.ID).Error; err != nil {
		t.Fatalf("目标库未找到主机: %v", err)
	}
	if copiedHost.SSHCredentialCiphertext != host.SSHCredentialCiphertext {
		t.Fatalf("主机加密凭据复制不一致: %+v", copiedHost)
	}
	var copiedMembership model.EnvironmentHost
	if err := target.First(&copiedMembership,
		"environment_id = ? AND host_id = ?", environment.ID, host.ID,
	).Error; err != nil {
		t.Fatalf("目标库未找到环境主机关联: %v", err)
	}
	var copiedEndpoint model.DockerEndpoint
	if err := target.First(&copiedEndpoint, "id = ?", endpoint.ID).Error; err != nil {
		t.Fatalf("目标库未找到 Docker 连接: %v", err)
	}
	var copiedCapability model.HostCapability
	if err := target.First(&copiedCapability, "host_id = ? AND kind = ?", host.ID, model.HostCapabilityDocker).Error; err != nil {
		t.Fatalf("目标库未找到主机能力: %v", err)
	}
	if copiedEndpoint.HostID != host.ID || copiedCapability.RuntimeID != endpoint.ID {
		t.Fatalf("主机与运行时关系复制不一致: endpoint=%+v capability=%+v", copiedEndpoint, copiedCapability)
	}
	var sourceMigrations, targetMigrations int64
	_ = source.Model(&model.SchemaMigration{}).Count(&sourceMigrations).Error
	_ = target.Model(&model.SchemaMigration{}).Count(&targetMigrations).Error
	if sourceMigrations != targetMigrations {
		t.Fatalf("目标库迁移版本数量异常: source=%d target=%d", sourceMigrations, targetMigrations)
	}
}

func TestEnsureEmptyTransferTargetAllowsInitializedEmptySchema(t *testing.T) {
	db := openTransferTestDatabase(t, "transfer-empty")
	if err := ensureEmptyTransferTarget(db); err != nil {
		t.Fatalf("已初始化的空库应允许迁移: %v", err)
	}
	now := time.Now().UTC()
	if err := db.Create(&model.User{ID: "user", Username: "user", Nickname: "user", PasswordHash: "hash", IsActive: true, AuthVersion: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureEmptyTransferTarget(db); !errors.Is(err, ErrTargetNotEmpty) {
		t.Fatalf("非空目标库未被拒绝: %v", err)
	}
}

func openTransferTestDatabase(t *testing.T, name string) *gorm.DB {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:" + name + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close(db) })
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}
