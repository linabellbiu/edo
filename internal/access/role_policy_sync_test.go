package access

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/account"
	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
)

func TestRolePermissionSyncFailureDisablesMembersAcrossDepartments(t *testing.T) {
	ctx := context.Background()
	db, service := newRolePolicySyncTestService(t, "role_policy_sync_disable")
	role, err := service.CreateRole(ctx, RoleInput{
		Name: "sync-role", DisplayName: "同步失败角色", Permissions: []string{PermissionRoleRead},
	})
	if err != nil {
		t.Fatalf("创建同步失败测试角色失败: %v", err)
	}
	fullRole, err := service.CreateRole(ctx, RoleInput{
		Name: "sync-full-role", DisplayName: "整包同步失败角色", Permissions: []string{PermissionRoleRead},
	})
	if err != nil {
		t.Fatalf("创建整包同步失败测试角色失败: %v", err)
	}
	emptyRole, err := service.CreateRole(ctx, RoleInput{
		Name: "sync-empty-role", DisplayName: "待删除空角色", Permissions: []string{},
	})
	if err != nil {
		t.Fatalf("创建待删除空角色失败: %v", err)
	}
	memberA, err := service.CreateUserInDepartment(ctx, "syncmembera", "甲部门成员", "correct horse battery staple", "sync-department-a", []string{role.ID})
	if err != nil {
		t.Fatalf("创建甲部门成员失败: %v", err)
	}
	memberB, err := service.CreateUserInDepartment(ctx, "syncmemberb", "乙部门成员", "correct horse battery staple", "sync-department-b", []string{role.ID})
	if err != nil {
		t.Fatalf("创建乙部门成员失败: %v", err)
	}
	fullMember, err := service.CreateUserInDepartment(ctx, "syncmemberc", "整包更新成员", "correct horse battery staple", "sync-department-a", []string{fullRole.ID})
	if err != nil {
		t.Fatalf("创建整包更新成员失败: %v", err)
	}
	admin, err := account.NewService(db).CreateAdmin(ctx, "syncadmin", "同步管理员", "correct horse battery staple")
	if err != nil {
		t.Fatalf("创建超级管理员失败: %v", err)
	}
	if err := db.Create(&model.UserRole{UserID: admin.ID, RoleID: role.ID, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("创建超级管理员角色关联失败: %v", err)
	}
	service.watcher = failingRoleWatcher{}

	scopedA := database.WithDepartmentScope(ctx, database.DepartmentScope{
		UserID: "sync-operator", DepartmentID: "sync-department-a",
	})
	updated, err := service.UpdateRolePermissions(scopedA, role.ID, []string{PermissionUserRead})
	if !errors.Is(err, ErrRolePermissionsSyncDisabled) || updated == nil {
		t.Fatalf("独立权限同步失败未返回安全停用语义: role=%+v err=%v", updated, err)
	}
	for _, user := range []*model.User{memberA, memberB} {
		var stored model.User
		if queryErr := db.First(&stored, "id = ?", user.ID).Error; queryErr != nil {
			t.Fatalf("查询安全停用成员失败: %v", queryErr)
		}
		if stored.IsActive || stored.AuthVersion != user.AuthVersion+1 {
			t.Fatalf("关联成员未被安全停用: user=%s active=%t auth_version=%d want=%d", stored.ID, stored.IsActive, stored.AuthVersion, user.AuthVersion+1)
		}
	}
	var storedAdmin model.User
	if err := db.First(&storedAdmin, "id = ?", admin.ID).Error; err != nil || !storedAdmin.IsActive || storedAdmin.AuthVersion != admin.AuthVersion {
		t.Fatalf("超级管理员不应因角色同步失败被停用: admin=%+v err=%v", storedAdmin, err)
	}
	permissions, err := service.rolePermissions(ctx, role.ID)
	if err != nil || len(permissions) != 1 || permissions[0] != PermissionUserRead {
		t.Fatalf("同步失败后独立权限修改未提交: permissions=%v err=%v", permissions, err)
	}

	updated, err = service.UpdateRole(scopedA, fullRole.ID, RoleInput{
		Name: "sync-full-role-renamed", DisplayName: "已提交整包更新", Permissions: []string{PermissionUserUpdate},
	})
	if !errors.Is(err, ErrRolePermissionsSyncDisabled) || updated == nil || updated.Name != "sync-full-role-renamed" {
		t.Fatalf("整包更新同步失败未返回安全停用语义: role=%+v err=%v", updated, err)
	}
	var storedFullMember model.User
	if err := db.First(&storedFullMember, "id = ?", fullMember.ID).Error; err != nil || storedFullMember.IsActive {
		t.Fatalf("整包更新关联成员未被安全停用: member=%+v err=%v", storedFullMember, err)
	}

	created, err := service.CreateRole(ctx, RoleInput{
		Name: "sync-created-role", DisplayName: "已创建待同步角色", Permissions: []string{PermissionRoleRead},
	})
	if !errors.Is(err, ErrRoleCreatedSyncPending) || created == nil {
		t.Fatalf("创建角色同步失败未返回已提交警告: role=%+v err=%v", created, err)
	}
	var createdCount int64
	if err := db.Model(&model.Role{}).Where("id = ?", created.ID).Count(&createdCount).Error; err != nil || createdCount != 1 {
		t.Fatalf("同步失败后角色创建结果未保留: count=%d err=%v", createdCount, err)
	}
	if err := service.DeleteRole(ctx, emptyRole.ID); !errors.Is(err, ErrRoleDeletedSyncPending) {
		t.Fatalf("删除角色同步失败未返回已提交警告: %v", err)
	}
	var deletedCount int64
	if err := db.Model(&model.Role{}).Where("id = ?", emptyRole.ID).Count(&deletedCount).Error; err != nil || deletedCount != 0 {
		t.Fatalf("同步失败后角色删除结果未保留: count=%d err=%v", deletedCount, err)
	}
}

func TestRolePermissionSyncFailureReportsUnsafeDisable(t *testing.T) {
	ctx := context.Background()
	db, service := newRolePolicySyncTestService(t, "role_policy_sync_unsafe")
	role, err := service.CreateRole(ctx, RoleInput{
		Name: "unsafe-sync-role", DisplayName: "无法停用角色", Permissions: []string{PermissionRoleRead},
	})
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	member, err := service.CreateUserInDepartment(ctx, "unsafemember", "无法停用成员", "correct horse battery staple", "unsafe-department", []string{role.ID})
	if err != nil {
		t.Fatalf("创建角色成员失败: %v", err)
	}
	callbackName := "test:fail_role_member_disable"
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.User{}).TableName() {
			tx.AddError(errors.New("测试安全停用失败"))
		}
	}); err != nil {
		t.Fatalf("注册测试更新失败回调失败: %v", err)
	}
	defer db.Callback().Update().Remove(callbackName)
	service.watcher = failingRoleWatcher{}

	updated, err := service.UpdateRolePermissions(ctx, role.ID, []string{PermissionUserRead})
	if !errors.Is(err, ErrRolePermissionsSyncUnsafe) || updated == nil {
		t.Fatalf("无法安全停用时未返回明确错误: role=%+v err=%v", updated, err)
	}
	var stored model.User
	if err := db.First(&stored, "id = ?", member.ID).Error; err != nil || !stored.IsActive {
		t.Fatalf("安全停用失败测试的成员状态异常: member=%+v err=%v", stored, err)
	}
	permissions, err := service.rolePermissions(ctx, role.ID)
	if err != nil || len(permissions) != 1 || permissions[0] != PermissionUserRead {
		t.Fatalf("无法安全停用时角色权限提交状态错误: permissions=%v err=%v", permissions, err)
	}
}

func newRolePolicySyncTestService(t *testing.T, databaseName string) (*gorm.DB, *Service) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:" + databaseName + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	service, err := NewService(db)
	if err != nil {
		t.Fatalf("初始化权限服务失败: %v", err)
	}
	return db, service
}

type failingRoleWatcher struct{}

func (failingRoleWatcher) SetUpdateCallback(func(string)) error { return nil }
func (failingRoleWatcher) Update() error                        { return errors.New("测试权限策略同步失败") }
func (failingRoleWatcher) Close()                               {}
