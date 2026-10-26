package access

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"

	"edo/internal/database"
	"edo/internal/model"
)

func TestRoleGrantReferencesPreventDeletionAcrossProjects(t *testing.T) {
	db, service := newRolePolicySyncTestService(t, "role_grant_references")
	role, err := service.CreateRole(context.Background(), RoleInput{Name: "unassigned-cluster-role", DisplayName: "无成员的集群角色", Permissions: []string{"kubernetes.workload.read"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	departments := []model.Department{{ID: uuid.NewString(), Name: "Grant Department A", IsActive: true, CreatedAt: now, UpdatedAt: now}, {ID: uuid.NewString(), Name: "Grant Department B", IsActive: true, CreatedAt: now, UpdatedAt: now}}
	for _, department := range departments {
		if err := db.Create(&department).Error; err != nil {
			t.Fatal(err)
		}
	}
	projects := []model.Project{{ID: uuid.NewString(), DepartmentID: departments[0].ID, Name: "Grant Project A", IsActive: true, CreatedAt: now, UpdatedAt: now}, {ID: uuid.NewString(), DepartmentID: departments[1].ID, Name: "Grant Project B", IsActive: true, CreatedAt: now, UpdatedAt: now}}
	for _, project := range projects {
		if err := db.Create(&project).Error; err != nil {
			t.Fatal(err)
		}
	}
	grant := model.KubernetesRoleGrant{ID: uuid.NewString(), ClusterID: uuid.NewString(), ProjectID: projects[1].ID, DepartmentID: departments[1].ID, RoleID: role.ID, Namespace: "test", Permissions: datatypes.JSON(`["kubernetes.workload.read"]`), CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: "project-a-role-admin", ProjectID: projects[0].ID, DepartmentID: departments[0].ID})
	assertUsage := func(want bool) {
		t.Helper()
		roles, err := service.ListRoles(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range roles {
			if item.ID == role.ID {
				if item.InUse != want || item.VisibleMemberCount != 0 || item.VisibleProjectMemberCount != 0 {
					t.Fatalf("global reference or private member visibility incorrect: %+v", item)
				}
				return
			}
		}
		t.Fatal("global role template disappeared")
	}
	assertUsage(true)
	if err := service.DeleteRole(ctx, role.ID); !errors.Is(err, ErrRoleInUse) {
		t.Fatalf("cross-project grant did not protect role: %v", err)
	} else if strings.Contains(err.Error(), projects[1].ID) || strings.Contains(err.Error(), departments[1].ID) {
		t.Fatal("in-use error disclosed another project's identity")
	}
	updated, err := service.UpdateRoleBasic(ctx, role.ID, RoleBasicInput{Name: role.Name, DisplayName: "更新后的集群角色", Description: "只修改元数据"})
	if err != nil || updated.DisplayName != "更新后的集群角色" || len(updated.Permissions) != 1 {
		t.Fatalf("metadata update incorrectly blocked by grant reference: %+v %v", updated, err)
	}
	var count int64
	if err := db.Model(&model.RolePermission{}).Where("role_id = ?", role.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("blocked delete changed role permissions: count=%d err=%v", count, err)
	}
	if err := db.Delete(&grant).Error; err != nil {
		t.Fatal(err)
	}
	assertUsage(false)
	if err := service.DeleteRole(ctx, role.ID); err != nil {
		t.Fatalf("unreferenced role could not be deleted: %v", err)
	}
	if err := db.Model(&model.Role{}).Where("uuid = ?", role.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("unreferenced role remained: count=%d err=%v", count, err)
	}
}
