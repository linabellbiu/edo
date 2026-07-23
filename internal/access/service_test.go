package access

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"zrt/internal/account"
	"zrt/internal/config"
	"zrt/internal/database"
)

func TestRolePermissionAndAtomicUserCreation(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:access_test?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer database.Close(db)
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}

	service := NewService(db)
	role, err := service.CreateRole(ctx, RoleInput{
		Name: "auditor", DisplayName: "审计员", Permissions: []string{PermissionAuditRead},
	})
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	user, err := service.CreateUser(ctx, "reviewer", "审核用户", "correct horse battery staple", []string{role.ID})
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	allowed, err := service.HasPermission(ctx, user, PermissionAuditRead)
	if err != nil || !allowed {
		t.Fatalf("用户权限未生效: allowed=%v err=%v", allowed, err)
	}
	allowed, err = service.HasPermission(ctx, user, PermissionRoleManage)
	if err != nil || allowed {
		t.Fatalf("用户获得了未分配权限: allowed=%v err=%v", allowed, err)
	}

	_, err = service.CreateUser(ctx, "orphan", "不应创建", "correct horse battery staple", []string{"missing-role"})
	if !errors.Is(err, ErrInvalidUserRoles) {
		t.Fatalf("无效角色未被拒绝: %v", err)
	}
	if _, err := account.NewService(db).FindByUsername(ctx, "orphan"); err == nil {
		t.Fatal("事务失败后仍留下用户记录")
	}
}
