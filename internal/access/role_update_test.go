package access

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/database"
)

func TestRoleBasicAndPermissionUpdatesAreIndependent(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:access_role_update_test?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer database.Close(db)
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	service, err := NewService(db)
	if err != nil {
		t.Fatalf("初始化权限服务失败: %v", err)
	}

	role, err := service.CreateRole(ctx, RoleInput{
		Name: "independent-role", DisplayName: "原名称", Description: "原说明",
		Permissions: []string{PermissionRoleRead},
	})
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	role, err = service.UpdateRoleBasic(ctx, role.ID, RoleBasicInput{
		Name: "independent-role-renamed", DisplayName: "新名称", Description: "新说明",
	})
	if err != nil {
		t.Fatalf("独立更新角色基本信息失败: %v", err)
	}
	if !slices.Equal(role.Permissions, []string{PermissionRoleRead}) {
		t.Fatalf("更新基本信息覆盖了角色权限: %v", role.Permissions)
	}

	role, err = service.UpdateRolePermissions(ctx, role.ID, []string{PermissionUserRead})
	if err != nil {
		t.Fatalf("独立更新角色权限失败: %v", err)
	}
	if role.Name != "independent-role-renamed" || role.DisplayName != "新名称" || role.Description != "新说明" {
		t.Fatalf("更新权限覆盖了角色基本信息: %+v", role.Role)
	}
	if !slices.Equal(role.Permissions, []string{PermissionUserRead}) {
		t.Fatalf("角色权限未独立更新: %v", role.Permissions)
	}

	role, err = service.UpdateRole(ctx, role.ID, RoleInput{
		Name: "legacy-role-update", DisplayName: "兼容更新", Description: "旧接口仍可用",
		Permissions: []string{PermissionAuditRead},
	})
	if err != nil {
		t.Fatalf("兼容的整包角色更新失败: %v", err)
	}
	if role.Name != "legacy-role-update" || !slices.Equal(role.Permissions, []string{PermissionAuditRead}) {
		t.Fatalf("兼容的整包角色更新结果错误: %+v permissions=%v", role.Role, role.Permissions)
	}
}
