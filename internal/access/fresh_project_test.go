package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

func TestFreshProjectPermissionsBypassesStaleEnforcerAndHonorsDeny(t *testing.T) {
	db, service := newRolePolicySyncTestService(t, "fresh_project_permissions")
	admin := &model.User{ID: "root", DepartmentID: database.DefaultDepartmentID, IsSuperuser: true, IsActive: true}
	project, err := service.CreateProject(context.Background(), admin, "Fresh RBAC", "", "")
	if err != nil {
		t.Fatal(err)
	}
	role, err := service.CreateRole(context.Background(), RoleInput{Name: "fresh-project-reader", DisplayName: "Fresh Reader", Permissions: []string{"kubernetes.workload.read"}})
	if err != nil {
		t.Fatal(err)
	}
	user, err := service.CreateUser(context.Background(), "fresh-project-user", "Fresh", "correct horse battery staple", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetProjectMember(context.Background(), admin, project.ID, user.ID, role.ID); err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: project.ID, DepartmentID: user.DepartmentID, UserID: user.ID})
	before, err := FreshProjectPermissions(db, ctx, user.ID, project.ID)
	if err != nil || !before.Permissions["kubernetes.workload.read"] || before.RoleID != role.ID {
		t.Fatalf("initial permission: %+v %v", before, err)
	}
	if err := db.Where("role_id = ? AND permission = ?", role.ID, "kubernetes.workload.read").Delete(&model.RolePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	stale, err := service.HasPermission(ctx, user, "kubernetes.workload.read")
	if err != nil || !stale {
		t.Fatal("fixture did not retain stale enforcer policy")
	}
	after, err := FreshProjectPermissions(db, ctx, user.ID, project.ID)
	if err != nil || after.Permissions["kubernetes.workload.read"] || after.Digest == before.Digest {
		t.Fatalf("fresh permission reused stale allow: %+v %v", after, err)
	}
	override := model.UserPermission{UserID: user.ID, Permission: "kubernetes.workload.read", Effect: model.PermissionAllow, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&override).Error; err != nil {
		t.Fatal(err)
	}
	allowed, err := FreshProjectPermissions(db, ctx, user.ID, project.ID)
	if err != nil || !allowed.Permissions["kubernetes.workload.read"] {
		t.Fatalf("explicit allow lost: %+v %v", allowed, err)
	}
	if err := db.Model(&override).Update("effect", model.PermissionDeny).Error; err != nil {
		t.Fatal(err)
	}
	denied, err := FreshProjectPermissions(db, ctx, user.ID, project.ID)
	if err != nil || denied.Permissions["kubernetes.workload.read"] || denied.Digest == allowed.Digest {
		t.Fatalf("explicit deny lost: %+v %v", denied, err)
	}
	if err := db.Where("project_id = ? AND user_id = ?", project.ID, user.ID).Delete(&model.ProjectMember{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := FreshProjectPermissions(db, ctx, user.ID, project.ID); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("removed membership accepted: %v", err)
	}
}

func TestFreshProjectPermissionsRejectsInactiveAndWrongProject(t *testing.T) {
	db, service := newRolePolicySyncTestService(t, "fresh_project_inactive")
	admin := &model.User{ID: "root", DepartmentID: database.DefaultDepartmentID, IsSuperuser: true, IsActive: true}
	project, err := service.CreateProject(context.Background(), admin, "Fresh Project Scope", "", "")
	if err != nil {
		t.Fatal(err)
	}
	user, err := service.CreateUser(context.Background(), "fresh-inactive-user", "Fresh", "correct horse battery staple", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetProjectMember(context.Background(), admin, project.ID, user.ID, ProjectOwnerRoleID); err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: project.ID, DepartmentID: user.DepartmentID, UserID: user.ID})
	if _, err := FreshProjectPermissions(db, ctx, user.ID, "different-project"); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("wrong project accepted: %v", err)
	}
	if err := db.Model(user).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := FreshProjectPermissions(db, ctx, user.ID, project.ID); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("inactive user accepted: %v", err)
	}
}

func TestFreshProjectPermissionsScopesCrossDepartmentIdentityOnly(t *testing.T) {
	db := testutil.NewDatabase(t)
	department := model.Department{ID: "fresh-other-department", Name: "其他部门", IsActive: true}
	project := model.Project{ID: "fresh-other-project", DepartmentID: department.ID, Name: "其他部门项目", IsActive: true}
	admin := model.User{ID: "fresh-cross-admin", Username: "fresh_cross_admin", DepartmentID: database.DefaultDepartmentID, IsActive: true, IsSuperuser: true}
	foreign := model.User{ID: "fresh-cross-member", Username: "fresh_cross_member", DepartmentID: database.DefaultDepartmentID, IsActive: true}
	member := model.User{ID: "fresh-local-member", Username: "fresh_local_member", DepartmentID: department.ID, IsActive: true}
	role := model.Role{ID: "fresh-executor-role", Name: "fresh_executor_role", DisplayName: "项目执行员"}
	for _, item := range []any{
		&department, &project, &admin, &foreign, &member, &role,
		&model.RolePermission{RoleID: role.ID, Permission: "pipeline_run.execute"},
		&model.ProjectMember{ProjectID: project.ID, UserID: foreign.ID, RoleID: role.ID},
		&model.ProjectMember{ProjectID: project.ID, UserID: member.ID, RoleID: role.ID},
	} {
		if err := db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	type traceKey struct{}
	base, cancel := context.WithTimeout(context.WithValue(context.Background(), traceKey{}, "identity-trace"), time.Minute)
	defer cancel()
	deadline, _ := base.Deadline()
	ctx := database.WithDepartmentScope(base, database.DepartmentScope{ProjectID: project.ID, DepartmentID: department.ID})
	queries := map[string]int{}
	if err := db.Callback().Query().Before("gorm:query").Register("test:fresh_identity_scope", func(tx *gorm.DB) {
		current := tx.Statement.Context
		if current.Value(traceKey{}) != "identity-trace" {
			t.Error("授权查询丢失了原请求上下文")
		}
		if actual, ok := current.Deadline(); !ok || !actual.Equal(deadline) {
			t.Error("授权查询丢失了原请求超时")
		}
		scope, _ := database.DepartmentScopeFromContext(current)
		queries[tx.Statement.Table]++
		if tx.Statement.Table == "users" {
			if !scope.AllDepartments || scope.ProjectID != "" || scope.DepartmentID != "" {
				t.Error("跨部门身份查询仍受到业务项目范围过滤")
			}
		} else if scope.ProjectID != project.ID || scope.DepartmentID != department.ID || scope.AllDepartments {
			t.Errorf("身份读取范围扩大到了业务授权查询: table=%s", tx.Statement.Table)
		}
	}); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []model.User{admin, member} {
		authorization, err := FreshProjectPermissions(db, ctx, actor.ID, project.ID)
		if err != nil || !authorization.Permissions["pipeline_run.execute"] {
			t.Fatalf("有效操作者无法在冻结项目执行: actor=%s err=%v", actor.ID, err)
		}
	}
	for _, table := range []string{"users", "projects", "project_members", "role_permissions", "user_permissions"} {
		if queries[table] == 0 {
			t.Fatalf("没有覆盖授权查询的冻结范围: table=%s", table)
		}
	}
	if _, err := FreshProjectPermissions(db, ctx, foreign.ID, project.ID); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("普通跨部门用户借助成员记录获得项目权限: %v", err)
	}
	if _, err := FreshProjectPermissions(db, ctx, admin.ID, "different-project"); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("超级管理员身份读取放宽了目标项目范围: %v", err)
	}
	if err := db.Model(&model.Project{}).Where("uuid = ?", project.ID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := FreshProjectPermissions(db, ctx, admin.ID, project.ID); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("超级管理员可以执行已失效项目: %v", err)
	}
}
