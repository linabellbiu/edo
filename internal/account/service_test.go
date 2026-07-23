package account

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/auth"
	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/model"
)

func TestEnsureInitialAdmin(t *testing.T) {
	db := openAccountTestDatabase(t, "initial-admin")
	service := NewService(db)

	admin, created, err := service.EnsureInitialAdmin(context.Background())
	if err != nil {
		t.Fatalf("初始化管理员失败: %v", err)
	}
	if !created || admin.Username != "admin" || !admin.IsActive || !admin.IsSuperuser || admin.AuthVersion != 1 {
		t.Fatalf("初始化管理员状态错误: created=%v admin=%+v", created, admin)
	}
	matched, err := auth.ComparePassword("123456", admin.PasswordHash)
	if err != nil || !matched {
		t.Fatalf("初始化管理员密码无效: matched=%v err=%v", matched, err)
	}

	again, created, err := service.EnsureInitialAdmin(context.Background())
	if err != nil || created || again != nil {
		t.Fatalf("重复初始化不应创建账户: created=%v admin=%+v err=%v", created, again, err)
	}
}

func TestEnsureInitialAdminDoesNotModifyExistingUsers(t *testing.T) {
	db := openAccountTestDatabase(t, "existing-user")
	now := time.Now().UTC()
	existing := model.User{
		ID: "existing-user", Username: "operator", Nickname: "运维人员", PasswordHash: "existing-hash",
		IsActive: true, AuthVersion: 7, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("创建已有用户失败: %v", err)
	}

	admin, created, err := NewService(db).EnsureInitialAdmin(context.Background())
	if err != nil || created || admin != nil {
		t.Fatalf("已有账户时不应创建管理员: created=%v admin=%+v err=%v", created, admin, err)
	}
	var count int64
	if err := db.Model(&model.User{}).Where("username = ?", "admin").Count(&count).Error; err != nil {
		t.Fatalf("检查默认管理员失败: %v", err)
	}
	if count != 0 {
		t.Fatal("已有账户的数据库不应补建默认管理员")
	}
	var stored model.User
	if err := db.First(&stored, "id = ?", existing.ID).Error; err != nil {
		t.Fatalf("读取已有用户失败: %v", err)
	}
	if stored.PasswordHash != existing.PasswordHash || stored.AuthVersion != existing.AuthVersion {
		t.Fatalf("已有用户被意外修改: %+v", stored)
	}
}

func openAccountTestDatabase(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := database.Open(context.Background(), config.Database{
		Driver:          "sqlite",
		DSN:             "file:" + name + "?mode=memory&cache=shared",
		MaxOpenConns:    1,
		MaxIdleConns:    1,
		ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	return db
}
