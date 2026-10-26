package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
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
