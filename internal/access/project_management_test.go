package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

func TestProjectManagementRejectsCurrentRevocation(t *testing.T) {
	for _, action := range []string{"update", "delete"} {
		for _, mutation := range []string{"授权有效", "角色撤权未同步", "个人拒绝未同步", "正常角色撤权", "正常个人拒绝", "移除成员", "停用用户", "用户迁出部门"} {
			t.Run(action+"/"+mutation, func(t *testing.T) {
				db, service, actor, project, roleID, ctx := projectManagementFixture(t, action)
				permission := "project." + action
				var mutationErr error
				switch mutation {
				case "角色撤权未同步":
					mutationErr = db.Where("role_id = ? AND permission = ?", roleID, permission).Delete(&model.RolePermission{}).Error
				case "个人拒绝未同步":
					mutationErr = db.Create(&model.UserPermission{UserID: actor.ID, Permission: permission, Effect: model.PermissionDeny}).Error
				case "正常角色撤权":
					_, mutationErr = service.UpdateRolePermissions(context.Background(), roleID, []string{})
				case "正常个人拒绝":
					mutationErr = service.SetUserPermissions(context.Background(), actor.ID, UserPermissionOverrides{Deny: []string{permission}})
				case "移除成员":
					mutationErr = db.Where("project_id = ? AND user_id = ?", project.ID, actor.ID).Delete(&model.ProjectMember{}).Error
				case "停用用户":
					mutationErr = db.Model(&model.User{}).Where("uuid = ?", actor.ID).Update("is_active", false).Error
				case "用户迁出部门":
					mutationErr = db.Model(&model.User{}).Where("uuid = ?", actor.ID).Update("department_id", uuid.NewString()).Error
				}
				if mutationErr != nil {
					t.Fatal(mutationErr)
				}
				if mutation == "角色撤权未同步" || mutation == "个人拒绝未同步" {
					cached, err := service.HasPermission(projectContext(ctx, actor, project), actor, permission)
					if err != nil || !cached {
						t.Fatalf("未保留真实 Casbin 旧授权基线: allowed=%t err=%v", cached, err)
					}
				}
				var err error
				if action == "update" {
					err = service.UpdateProject(ctx, actor, project.ID, "已修改项目", "", false)
				} else {
					err = service.DeleteProject(ctx, actor, project.ID)
				}
				var stored model.Project
				readErr := db.First(&stored, "uuid = ?", project.ID).Error
				changed := readErr == nil && !stored.IsActive
				if action == "delete" {
					changed = errors.Is(readErr, gorm.ErrRecordNotFound)
				}
				if mutation == "授权有效" {
					if err != nil || !changed {
						t.Fatalf("有效授权不能管理项目: changed=%t err=%v", changed, err)
					}
					return
				}
				denied := errors.Is(err, ErrAccessDelegationDenied) || mutation == "移除成员" && errors.Is(err, ErrProjectNotFound)
				if !denied || changed || readErr != nil || stored.Name != project.Name {
					t.Fatalf("撤权后项目被修改: changed=%t operation_err=%v read_err=%v", changed, err, readErr)
				}
			})
		}
	}
}

func TestProjectManagementCanReactivateWithoutOpeningBusinessAccess(t *testing.T) {
	db, service, actor, project, _, ctx := projectManagementFixture(t, "update")
	if err := db.Model(project).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	scope := projectContext(ctx, actor, project)
	if _, err := FreshProjectPermissions(db, scope, actor.ID, project.ID); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("停用项目仍可访问业务: %v", err)
	}
	if err := service.UpdateProject(ctx, actor, project.ID, project.Name, "", true); err != nil {
		t.Fatalf("合法管理者无法重新启用项目: %v", err)
	}
	if _, err := FreshProjectPermissions(db, scope, actor.ID, project.ID); err != nil {
		t.Fatalf("重新启用后项目不可访问: %v", err)
	}
}

func TestProjectManagementDoesNotTrustStaleSuperuserIdentity(t *testing.T) {
	for _, action := range []string{"update", "delete"} {
		t.Run(action, func(t *testing.T) {
			db, service, actor, project, roleID, ctx := projectManagementFixture(t, action)
			actor.IsSuperuser = true
			if err := db.Where("role_id = ?", roleID).Delete(&model.RolePermission{}).Error; err != nil {
				t.Fatal(err)
			}
			var err error
			if action == "update" {
				err = service.UpdateProject(ctx, actor, project.ID, "越权修改", "", false)
			} else {
				err = service.DeleteProject(ctx, actor, project.ID)
			}
			if !errors.Is(err, ErrAccessDelegationDenied) {
				t.Fatalf("旧超级管理员身份绕过当前授权: %v", err)
			}
		})
	}
}

func projectManagementFixture(t *testing.T, action string) (*gorm.DB, *Service, *model.User, *model.Project, string, context.Context) {
	t.Helper()
	db := testutil.NewDatabase(t)
	now := time.Now().UTC()
	actor := &model.User{ID: uuid.NewString(), Username: "pm-" + uuid.NewString()[:12], DepartmentID: database.DefaultDepartmentID, IsActive: true, CreatedAt: now, UpdatedAt: now}
	role := model.Role{ID: uuid.NewString(), Name: "管理角色-" + uuid.NewString(), DisplayName: "管理权限回归", CreatedAt: now, UpdatedAt: now}
	project := &model.Project{ID: uuid.NewString(), Name: "独立空项目-" + uuid.NewString(), DepartmentID: database.DefaultDepartmentID, IsActive: true, CreatedAt: now, UpdatedAt: now}
	for _, value := range []any{actor, &role, project, &model.RolePermission{RoleID: role.ID, Permission: "project." + action}, &model.ProjectMember{ProjectID: project.ID, UserID: actor.ID, RoleID: role.ID}} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: actor.ID, DepartmentID: actor.DepartmentID})
	return db, service, actor, project, role.ID, ctx
}
