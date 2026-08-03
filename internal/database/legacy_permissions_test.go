package database

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/model"
)

func TestLegacyManagePermissionsExpandAndDisappear(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:legacy_permission_expansion?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer Close(db)
	if err := db.AutoMigrate(&model.RolePermission{}, &model.UserPermission{}); err != nil {
		t.Fatalf("创建旧权限表失败: %v", err)
	}
	now := time.Now().UTC()
	if err := db.Create(&model.RolePermission{
		RoleID: "role-a", Permission: "repository.manage", CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("写入旧角色权限失败: %v", err)
	}
	if err := db.Create(&model.UserPermission{
		UserID: "user-a", Permission: "delivery.manage", Effect: model.PermissionDeny,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("写入旧用户权限失败: %v", err)
	}
	legacyWithoutDelete := map[string][]string{
		"identity.manage":     {"identity.create", "identity.update"},
		"config.manage":       {"config.create", "config.execute", "config.update"},
		"notification.manage": {"notification.create", "notification.execute", "notification.update"},
		"monitor.manage":      {"monitor.create", "monitor.execute", "monitor.update"},
		"scheduler.manage":    {"scheduler.create", "scheduler.update"},
	}
	for permission := range legacyWithoutDelete {
		if err := db.Create(&model.RolePermission{
			RoleID: "role-" + strings.ReplaceAll(permission, ".", "-"), Permission: permission, CreatedAt: now,
		}).Error; err != nil {
			t.Fatalf("写入待审计旧角色权限失败: permission=%s err=%v", permission, err)
		}
	}

	if err := migrateLegacyPermissions(db); err != nil {
		t.Fatalf("迁移旧聚合权限失败: %v", err)
	}

	var oldCount int64
	if err := db.Model(&model.RolePermission{}).Where("permission = ?", "repository.manage").Count(&oldCount).Error; err != nil {
		t.Fatalf("检查旧角色权限失败: %v", err)
	}
	if oldCount != 0 {
		t.Fatalf("旧角色权限迁移后仍存在: %d", oldCount)
	}
	if err := db.Model(&model.UserPermission{}).Where("permission = ?", "delivery.manage").Count(&oldCount).Error; err != nil {
		t.Fatalf("检查旧用户权限失败: %v", err)
	}
	if oldCount != 0 {
		t.Fatalf("旧用户权限迁移后仍存在: %d", oldCount)
	}

	var rolePermissions []model.RolePermission
	if err := db.Where("role_id = ?", "role-a").Order("permission ASC").Find(&rolePermissions).Error; err != nil {
		t.Fatalf("读取迁移后的角色权限失败: %v", err)
	}
	wantRole := map[string]bool{
		"repository.create":  false,
		"repository.update":  false,
		"repository.delete":  false,
		"repository.execute": false,
	}
	for _, permission := range rolePermissions {
		if _, ok := wantRole[permission.Permission]; ok {
			wantRole[permission.Permission] = true
		}
	}
	for permission, found := range wantRole {
		if !found {
			t.Fatalf("旧 repository.manage 未迁移为 %s: %+v", permission, rolePermissions)
		}
	}

	var userPermissions []model.UserPermission
	if err := db.Where("user_id = ?", "user-a").Order("permission ASC").Find(&userPermissions).Error; err != nil {
		t.Fatalf("读取迁移后的用户权限失败: %v", err)
	}
	wantUser := map[string]bool{
		"delivery.create": false,
		"delivery.update": false,
		"delivery.delete": false,
	}
	for _, permission := range userPermissions {
		if permission.Effect != model.PermissionDeny {
			t.Fatalf("旧用户拒绝权限迁移后改变了效果: %+v", permission)
		}
		if _, ok := wantUser[permission.Permission]; ok {
			wantUser[permission.Permission] = true
		}
	}
	for permission, found := range wantUser {
		if !found {
			t.Fatalf("旧 delivery.manage 未迁移为 %s: %+v", permission, userPermissions)
		}
	}

	for legacy, want := range legacyWithoutDelete {
		roleID := "role-" + strings.ReplaceAll(legacy, ".", "-")
		var permissions []string
		if err := db.Model(&model.RolePermission{}).Where("role_id = ?", roleID).
			Order("permission ASC").Pluck("permission", &permissions).Error; err != nil {
			t.Fatalf("读取旧权限真实能力展开结果失败: permission=%s err=%v", legacy, err)
		}
		if len(permissions) != len(want) {
			t.Fatalf("旧权限展开出了不存在的 API 能力: permission=%s got=%v want=%v", legacy, permissions, want)
		}
		for index := range want {
			if permissions[index] != want[index] {
				t.Fatalf("旧权限展开结果错误: permission=%s got=%v want=%v", legacy, permissions, want)
			}
		}
	}
}
