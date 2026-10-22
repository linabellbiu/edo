package access

import (
	"context"
	"errors"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
)

func TestProjectRolesAndExplicitDeny(t *testing.T) {
	db, s := newRolePolicySyncTestService(t, "project_roles_isolation")
	ctx := context.Background()
	admin := &model.User{ID: "root", DepartmentID: database.DefaultDepartmentID, IsSuperuser: true, IsActive: true}
	a, err := s.CreateProject(ctx, admin, "项目 A", "", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateProject(ctx, admin, "项目 B", "", "")
	if err != nil {
		t.Fatal(err)
	}
	global, err := s.CreateRole(ctx, RoleInput{Name: "global-builder", DisplayName: "平台角色", Permissions: []string{"application.create", "user.update"}})
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser(ctx, "project-user", "项目成员", "correct horse battery staple", []string{global.ID})
	if err != nil {
		t.Fatal(err)
	}
	reader := "bbf0404f-7165-5119-8620-194a3fab6c02"
	if err = s.SetProjectMember(ctx, admin, a.ID, user.ID, reader); err != nil {
		t.Fatal(err)
	}
	if err = s.SetProjectMember(ctx, admin, b.ID, user.ID, ProjectOwnerRoleID); err != nil {
		t.Fatal(err)
	}
	projects, err := s.Projects(ctx, user)
	if err != nil || len(projects) != 2 {
		t.Fatalf("成员只能读取已加入的两个项目: projects=%v err=%v", projects, err)
	}
	for _, project := range projects {
		if project.DepartmentID != user.DepartmentID || project.DepartmentName == "" {
			t.Fatalf("项目列表应提供已授权项目的部门名称: %+v", project)
		}
	}
	scope := func(id string) context.Context {
		return database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: user.ID, DepartmentID: user.DepartmentID, ProjectID: id})
	}
	check := func(project, code string, want bool) {
		t.Helper()
		got, e := s.HasPermission(scope(project), user, code)
		if e != nil || got != want {
			t.Fatalf("project=%s permission=%s got=%v want=%v err=%v", project, code, got, want, e)
		}
	}
	check(a.ID, "application.read", true)
	check(a.ID, "application.create", false)
	check(a.ID, "repository.secret.read", false)
	check(b.ID, "application.create", true)
	check(b.ID, "user.update", true) // 平台管理来自平台角色，不来自项目。
	check(database.DefaultProjectID(user.DepartmentID), "application.create", false)
	if err = s.SetUserPermissions(ctx, user.ID, UserPermissionOverrides{Deny: []string{"application.create"}}); err != nil {
		t.Fatal(err)
	}
	check(b.ID, "application.create", false)
	if err = s.SetProjectMember(ctx, admin, a.ID, user.ID, global.ID); !errors.Is(err, ErrProjectMember) {
		t.Fatalf("项目角色不应包含账号管理: %v", err)
	}
	if err = s.SetProjectMember(ctx, admin, a.ID, user.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveProject(ctx, user, a.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("移除成员后仍能解析项目: %v", err)
	}
	var memberCount int64
	db.Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", a.ID, user.ID).Count(&memberCount)
	if memberCount != 0 {
		t.Fatal("成员关系未删除")
	}
}

func TestProjectDelegationCannotExceedEffectiveActions(t *testing.T) {
	_, s := newRolePolicySyncTestService(t, "project_delegation")
	ctx := context.Background()
	admin := &model.User{ID: "root", IsSuperuser: true, DepartmentID: database.DefaultDepartmentID}
	p, err := s.CreateProject(ctx, admin, "委派测试", "", "")
	if err != nil {
		t.Fatal(err)
	}
	limited, err := s.CreateRole(ctx, RoleInput{Name: "limited", DisplayName: "有限管理员", Permissions: []string{"project.member.update", "application.read"}})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := s.CreateRole(ctx, RoleInput{Name: "broad-read", DisplayName: "全部交付查看", Permissions: []string{"application.read", "workflow.read", "build_plan.read"}})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := s.CreateUser(ctx, "limited-actor", "有限管理员", "correct horse battery staple", nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.CreateUser(ctx, "limited-target", "目标成员", "correct horse battery staple", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetProjectMember(ctx, admin, p.ID, actor.ID, limited.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.SetProjectMember(ctx, actor, p.ID, target.ID, legacy.ID); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("有限查看权限不能委派全部交付权限: %v", err)
	}
	if err = s.SetProjectMember(ctx, actor, p.ID, actor.ID, ProjectOwnerRoleID); !errors.Is(err, ErrSelfAccessUpdate) {
		t.Fatalf("不能修改自己的项目角色: %v", err)
	}
}

func TestProjectRoleCannotGrantPlatformPermissionAfterRoleEdit(t *testing.T) {
	_, s := newRolePolicySyncTestService(t, "project_role_platform_boundary")
	ctx := context.Background()
	admin := &model.User{ID: "root", IsSuperuser: true, DepartmentID: database.DefaultDepartmentID}
	p, err := s.CreateProject(ctx, admin, "平台边界", "", "")
	if err != nil {
		t.Fatal(err)
	}
	role, err := s.CreateRole(ctx, RoleInput{Name: "project-custom", DisplayName: "项目角色", Permissions: []string{"application.read"}})
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser(ctx, "project-only", "项目成员", "correct horse battery staple", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetProjectMember(ctx, admin, p.ID, user.ID, role.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateRolePermissions(ctx, role.ID, []string{"application.read", "user.update"}); err != nil {
		t.Fatal(err)
	}
	scope := database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: user.ID, DepartmentID: user.DepartmentID, ProjectID: p.ID})
	allowed, err := s.HasPermission(scope, user, "user.update")
	if err != nil || allowed {
		t.Fatalf("项目角色编辑后不得越过平台边界: %v %v", allowed, err)
	}
	s.watcher = failingRoleWatcher{}
	if _, err = s.UpdateRolePermissions(ctx, role.ID, []string{}); !errors.Is(err, ErrRolePermissionsSyncDisabled) {
		t.Fatal(err)
	}
	var stored model.User
	if err = s.db.First(&stored, "id = ?", user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.IsActive {
		t.Fatal("策略同步失败未安全停用项目成员")
	}
}
