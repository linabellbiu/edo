package access

import (
	"context"
	"slices"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
)

func TestK3sUninstallPermissionRequiresExplicitProjectGrant(t *testing.T) {
	if !IsKnown(PermissionK3sUninstall) || !ProjectPermission(PermissionK3sUninstall) {
		t.Fatal("K3s 卸载必须登记为独立项目权限")
	}
	found := false
	for _, item := range Catalog() {
		if item.Code == PermissionK3sUninstall {
			found = true
			if !item.Dangerous || item.Action != "uninstall" || item.Scope != "project" {
				t.Fatalf("K3s 卸载的权限元数据不安全: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("角色配置目录缺少 K3s 卸载权限")
	}
	for legacy, permissions := range LegacyPermissionExpansions {
		if slices.Contains(permissions, PermissionK3sUninstall) {
			t.Fatalf("旧聚合权限不得自动获得卸载: %s", legacy)
		}
	}
	_, service := newRolePolicySyncTestService(t, "k3s_uninstall_presets")
	ctx := context.Background()
	if err := service.EnsureProjectRoles(ctx); err != nil {
		t.Fatal(err)
	}
	for _, roleID := range []string{ProjectOwnerRoleID, "bbf0404f-7165-5119-8620-194a3fab6c02", projectDeveloperRoleID} {
		permissions, err := service.rolePermissions(ctx, roleID)
		if err != nil || slices.Contains(permissions, PermissionK3sUninstall) {
			t.Fatalf("内置角色模板不得自动获得卸载: role=%s err=%v", roleID, err)
		}
	}
	permissions, err := service.rolePermissions(ctx, ProjectOwnerRoleID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateRolePermissions(ctx, ProjectOwnerRoleID, append(permissions, PermissionK3sUninstall)); err != nil {
		t.Fatal(err)
	}
	if err := service.EnsureProjectRoles(ctx); err != nil {
		t.Fatal(err)
	}
	permissions, err = service.rolePermissions(ctx, ProjectOwnerRoleID)
	if err != nil || !slices.Contains(permissions, PermissionK3sUninstall) {
		t.Fatalf("重新初始化不得撤销管理员明确授予的卸载权限: %v", err)
	}
}

func TestK3sUninstallProjectIsolationAndExplicitDeny(t *testing.T) {
	db, service := newRolePolicySyncTestService(t, "k3s_uninstall_scope")
	ctx := context.Background()
	admin := &model.User{ID: "root", DepartmentID: database.DefaultDepartmentID, IsSuperuser: true, IsActive: true}
	a, err := service.CreateProject(ctx, admin, "卸载授权项目", "", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.CreateProject(ctx, admin, "未授权项目", "", "")
	if err != nil {
		t.Fatal(err)
	}
	role, err := service.CreateRole(ctx, RoleInput{Name: "k3s-uninstall-only", DisplayName: "仅 K3s 卸载", Permissions: []string{PermissionClusterRead, PermissionK3sUninstall}})
	if err != nil {
		t.Fatal(err)
	}
	user, err := service.CreateUser(ctx, "k3s-uninstall-member", "卸载成员", "correct horse battery staple", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetProjectMember(ctx, admin, a.ID, user.ID, role.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.SetProjectMember(ctx, admin, b.ID, user.ID, ProjectOwnerRoleID); err != nil {
		t.Fatal(err)
	}
	scope := func(projectID string) context.Context {
		return database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: user.ID, DepartmentID: user.DepartmentID, ProjectID: projectID})
	}
	check := func(projectID, permission string, want bool) {
		t.Helper()
		allowed, err := service.HasPermission(scope(projectID), user, permission)
		if err != nil || allowed != want {
			t.Fatalf("项目卸载授权不符: project=%s permission=%s allowed=%t want=%t err=%v", projectID, permission, allowed, want, err)
		}
	}
	check(a.ID, PermissionK3sUninstall, true)
	check(a.ID, PermissionClusterCreate, false)
	check(a.ID, "kubernetes.node.create", false)
	check(b.ID, PermissionK3sUninstall, false)
	check(database.DefaultProjectID(user.DepartmentID), PermissionK3sUninstall, false)
	if err := service.SetUserPermissions(ctx, user.ID, UserPermissionOverrides{Deny: []string{PermissionK3sUninstall}}); err != nil {
		t.Fatal(err)
	}
	check(a.ID, PermissionK3sUninstall, false)
	fresh, err := FreshProjectPermissions(db, scope(a.ID), user.ID, a.ID)
	if err != nil || fresh.Permissions[PermissionK3sUninstall] {
		t.Fatalf("实时权限复核不得忽略个人拒绝: %v", err)
	}
}
