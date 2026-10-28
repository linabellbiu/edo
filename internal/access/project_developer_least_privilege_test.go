package access

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

const projectDeveloperRoleIDForTest = "bbf0404f-7165-5119-8620-194a3fab6c03"

func projectDeveloperExpectedPermissionsForTest() []string {
	result := []string{
		"application.create", "application.update", "application.execute",
		"repository.create", "repository.update",
		"workflow.create", "workflow.update",
		"build_plan.create", "build_plan.update", "pipeline_run.execute",
	}
	for _, item := range Catalog() {
		if ProjectPermission(item.Code) && item.Action == "read" && !item.Dangerous {
			result = append(result, item.Code)
		}
	}
	return uniqueSorted(result)
}

func TestProjectDeveloperPresetLeastPrivilege(t *testing.T) {
	_, service := newRolePolicySyncTestService(t, "developer_least_privilege")
	if err := service.EnsureProjectRoles(context.Background()); err != nil {
		t.Fatal(err)
	}
	permissions, err := service.rolePermissions(context.Background(), projectDeveloperRoleIDForTest)
	if err != nil {
		t.Fatal(err)
	}
	if want := projectDeveloperExpectedPermissionsForTest(); !slices.Equal(permissions, want) {
		t.Fatalf("默认项目开发角色必须仅包含安全查看和明确列出的开发动作: got=%v want=%v", permissions, want)
	}
	for _, item := range Catalog() {
		if slices.Contains(permissions, item.Code) && !ProjectPermission(item.Code) {
			t.Errorf("默认项目开发角色包含平台权限: %s", item.Code)
		}
	}
}

func TestProjectDeveloperPresetDoesNotInheritNewWriteActions(t *testing.T) {
	for _, code := range []string{
		"environment_policy.update", "deployment.create", "deployment.update", "cluster.create", "cluster.update",
		"delivery_variable.create", "delivery_variable.update", "application_database.create", "application_database.update",
		"repository.secret.read", "host.terminal.open", "terminal.open", "deployment.review", "user.update",
		"application.sensitive.create", "workflow.sensitive.update", "future_resource.create", "future_resource.update",
	} {
		if projectDeveloperWritePermission(code) {
			t.Errorf("新权限或敏感写动作被默认开发角色自动继承: %s", code)
		}
	}
}

func projectDeveloperRowsForTest(t *testing.T, db *gorm.DB, roleID string) []model.RolePermission {
	t.Helper()
	var rows []model.RolePermission
	if err := db.Where("role_id = ?", roleID).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestProjectDeveloperPresetReconcilesLegacyPermissionsAndPreservesAssociations(t *testing.T) {
	db, service := newRolePolicySyncTestService(t, "developer_reconcile_existing")
	ctx := context.Background()
	admin := &model.User{ID: "root", DepartmentID: database.DefaultDepartmentID, IsSuperuser: true, IsActive: true}
	project, err := service.CreateProject(ctx, admin, "开发角色升级验证", "", "")
	if err != nil {
		t.Fatal(err)
	}
	developer, err := service.CreateUser(ctx, "preset-developer", "默认开发成员", "correct horse battery staple", []string{projectDeveloperRoleIDForTest})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetProjectMember(ctx, admin, project.ID, developer.ID, projectDeveloperRoleIDForTest); err != nil {
		t.Fatal(err)
	}
	custom, err := service.CreateRole(ctx, RoleInput{Name: "developer-custom-protection", DisplayName: "独立审批策略维护", Permissions: []string{"project.read", "environment_policy.read", "environment_policy.update"}})
	if err != nil {
		t.Fatal(err)
	}
	customUser, err := service.CreateUser(ctx, "custom-developer", "自定义策略维护员", "correct horse battery staple", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetProjectMember(ctx, admin, project.ID, customUser.ID, custom.ID); err != nil {
		t.Fatal(err)
	}
	// 模拟已有实例中的旧模板，并额外证明不会保留模板中的敏感及平台权限。
	legacy := append(projectDeveloperExpectedPermissionsForTest(), "environment_policy.update", "deployment.create", "deployment.update", "cluster.create", "cluster.update", "delivery_variable.update", "application_database.update", "repository.secret.read", "host.terminal.open", "deployment.review", "user.update")
	if _, err := service.UpdateRolePermissions(ctx, projectDeveloperRoleIDForTest, legacy); err != nil {
		t.Fatal(err)
	}
	var originalRole model.Role
	if err := db.First(&originalRole, "uuid = ?", projectDeveloperRoleIDForTest).Error; err != nil {
		t.Fatal(err)
	}
	var originalMembers []model.ProjectMember
	if err := db.Order("id ASC").Find(&originalMembers).Error; err != nil {
		t.Fatal(err)
	}
	var originalPlatformRoles []model.UserRole
	if err := db.Order("id ASC").Find(&originalPlatformRoles).Error; err != nil {
		t.Fatal(err)
	}
	originalCustomPermissions := projectDeveloperRowsForTest(t, db, custom.ID)
	originalOwnerPermissions := projectDeveloperRowsForTest(t, db, ProjectOwnerRoleID)
	originalViewerPermissions := projectDeveloperRowsForTest(t, db, "bbf0404f-7165-5119-8620-194a3fab6c02")
	scope := database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: project.ID, DepartmentID: developer.DepartmentID, UserID: developer.ID})
	if allowed, err := service.HasPermission(scope, developer, "environment_policy.update"); err != nil || !allowed {
		t.Fatalf("旧模板复现前提未满足: allowed=%v err=%v", allowed, err)
	}
	if err := service.EnsureProjectRoles(ctx); err != nil {
		t.Fatal(err)
	}
	permissions, err := service.rolePermissions(ctx, projectDeveloperRoleIDForTest)
	if err != nil || !slices.Equal(permissions, projectDeveloperExpectedPermissionsForTest()) {
		t.Fatalf("重新初始化没有收敛旧默认开发权限: permissions=%v err=%v", permissions, err)
	}
	var currentRole model.Role
	if err := db.First(&currentRole, "uuid = ?", projectDeveloperRoleIDForTest).Error; err != nil || !reflect.DeepEqual(originalRole, currentRole) {
		t.Fatalf("收敛权限改变了默认角色 UUID、内部主键或元数据: err=%v", err)
	}
	var currentMembers []model.ProjectMember
	if err := db.Order("id ASC").Find(&currentMembers).Error; err != nil || !reflect.DeepEqual(originalMembers, currentMembers) {
		t.Fatalf("收敛权限改变了合法项目成员关联: err=%v", err)
	}
	var currentPlatformRoles []model.UserRole
	if err := db.Order("id ASC").Find(&currentPlatformRoles).Error; err != nil || !reflect.DeepEqual(originalPlatformRoles, currentPlatformRoles) {
		t.Fatalf("收敛权限改变了已有角色关联: err=%v", err)
	}
	for _, other := range []struct {
		id          string
		permissions []model.RolePermission
	}{
		{custom.ID, originalCustomPermissions},
		{ProjectOwnerRoleID, originalOwnerPermissions},
		{"bbf0404f-7165-5119-8620-194a3fab6c02", originalViewerPermissions},
	} {
		if after := projectDeveloperRowsForTest(t, db, other.id); !reflect.DeepEqual(other.permissions, after) {
			t.Errorf("收敛开发权限改动了其他角色: role_id=%s", other.id)
		}
	}
	for code, want := range map[string]bool{"application.create": true, "application.update": true, "pipeline_run.execute": true, "environment_policy.update": false, "deployment.create": false, "cluster.update": false, "delivery_variable.update": false, "host.terminal.open": false, "deployment.review": false, "user.update": false} {
		allowed, err := service.HasPermission(scope, developer, code)
		if err != nil || allowed != want {
			t.Errorf("初始化后的 Casbin 权限未收敛: permission=%s allowed=%v want=%v err=%v", code, allowed, want, err)
		}
	}
	fresh, err := FreshProjectPermissions(db, scope, developer.ID, project.ID)
	if err != nil || fresh.Permissions["environment_policy.update"] || !fresh.Permissions["application.create"] {
		t.Fatalf("初始化后的实时项目权限错误: result=%+v err=%v", fresh, err)
	}
	customScope := database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: project.ID, DepartmentID: customUser.DepartmentID, UserID: customUser.ID})
	if allowed, err := service.HasPermission(customScope, customUser, "environment_policy.update"); err != nil || !allowed {
		t.Fatalf("自定义维护角色的显式授权丢失: allowed=%v err=%v", allowed, err)
	}
	// 稳定模板再次初始化时不重建权限行，避免每次创建项目都改变内部行号。
	stable := projectDeveloperRowsForTest(t, db, projectDeveloperRoleIDForTest)
	if err := service.EnsureProjectRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if after := projectDeveloperRowsForTest(t, db, projectDeveloperRoleIDForTest); !reflect.DeepEqual(stable, after) {
		t.Fatal("稳定默认开发角色重复初始化不幂等")
	}
}

func TestProjectDeveloperPresetReconcileFailureRollsBack(t *testing.T) {
	db, service := newRolePolicySyncTestService(t, "developer_reconcile_rollback")
	ctx := context.Background()
	if err := service.EnsureProjectRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateRolePermissions(ctx, projectDeveloperRoleIDForTest, []string{"project.read", "environment_policy.update"}); err != nil {
		t.Fatal(err)
	}
	before := projectDeveloperRowsForTest(t, db, projectDeveloperRoleIDForTest)
	if err := db.Callback().Create().Before("gorm:create").Register("test:developer-reconcile-fail", func(tx *gorm.DB) {
		if tx.Statement.Table == "role_permissions" {
			tx.AddError(context.DeadlineExceeded)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove("test:developer-reconcile-fail") })
	if err := service.EnsureProjectRoles(ctx); err == nil {
		t.Fatal("默认角色权限写入失败被忽略")
	}
	if after := projectDeveloperRowsForTest(t, db, projectDeveloperRoleIDForTest); !reflect.DeepEqual(before, after) {
		t.Fatal("权限收敛事务失败后留下部分权限或清空角色")
	}
}
