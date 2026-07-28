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
