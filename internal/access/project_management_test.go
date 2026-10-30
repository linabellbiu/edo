package access

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

func TestProjectMemberMustKeepLastOwner(t *testing.T) {
	for _, replacement := range []string{"", "limited"} {
		t.Run("替换为"+replacement, func(t *testing.T) {
			db, service, users, project, role := projectMemberOwnerFixture(t)
			roleID := ""
			if replacement == "limited" {
				roleID = role.ID
			}
			if err := service.SetProjectMember(context.Background(), users[2], project.ID, users[0].ID, roleID); !errors.Is(err, ErrProjectLastOwner) {
				t.Fatalf("受限成员管理员移除了最后一个项目管理员: %v", err)
			}
			var member model.ProjectMember
			if err := db.Where("project_id = ? AND user_id = ?", project.ID, users[0].ID).First(&member).Error; err != nil || member.RoleID != ProjectOwnerRoleID {
				t.Fatalf("被拒绝的修改仍改变了最后管理员: role=%s err=%v", member.RoleID, err)
			}
		})
	}
}

func TestProjectMemberLastOwnerIgnoresUnavailableOwners(t *testing.T) {
	for _, mutation := range []string{"停用", "迁出部门", "个人拒绝"} {
		t.Run(mutation, func(t *testing.T) {
			db, service, users, project, _ := projectMemberOwnerFixture(t)
			if err := service.SetProjectMember(context.Background(), users[3], project.ID, users[1].ID, ProjectOwnerRoleID); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mutation {
			case "停用":
				err = db.Model(&model.User{}).Where("uuid = ?", users[1].ID).Update("is_active", false).Error
			case "迁出部门":
				err = db.Model(&model.User{}).Where("uuid = ?", users[1].ID).Update("department_id", uuid.NewString()).Error
			case "个人拒绝":
				err = service.SetUserPermissions(context.Background(), users[1].ID, UserPermissionOverrides{Deny: []string{"project.member.update"}})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := service.SetProjectMember(context.Background(), users[2], project.ID, users[0].ID, ""); !errors.Is(err, ErrProjectLastOwner) {
				t.Fatalf("不可管理成员的账号被当成了可保留的项目管理员: %v", err)
			}
		})
	}
}

func TestProjectMemberOwnerTransferAndSuperuserRecovery(t *testing.T) {
	db, service, users, project, _ := projectMemberOwnerFixture(t)
	var before model.Project
	if err := db.First(&before, "uuid = ?", project.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.SetProjectMember(context.Background(), users[0], project.ID, users[1].ID, ProjectOwnerRoleID); err != nil {
		t.Fatalf("不能先增加新管理员: %v", err)
	}
	if err := service.SetProjectMember(context.Background(), users[1], project.ID, users[0].ID, ""); err != nil {
		t.Fatalf("保留新管理员后不能移除旧管理员: %v", err)
	}
	if err := service.SetProjectMember(context.Background(), users[3], project.ID, users[1].ID, ""); err != nil {
		t.Fatalf("超级管理员恢复入口被阻断: %v", err)
	}
	if err := service.SetProjectMember(context.Background(), users[3], project.ID, users[0].ID, ProjectOwnerRoleID); err != nil {
		t.Fatalf("超级管理员不能恢复本轮管理员: %v", err)
	}
	var stored model.Project
	if err := db.First(&stored, "uuid = ?", project.ID).Error; err != nil || !stored.UpdatedAt.Equal(before.UpdatedAt) || stored.Name != before.Name || stored.Description != before.Description {
		t.Fatalf("成员锁改变了项目元数据: stored=%+v err=%v", stored, err)
	}
}

func TestProjectMemberConcurrentOwnerRemovalKeepsRecovery(t *testing.T) {
	db, service, users, project, role := projectMemberOwnerFixture(t)
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(4)
	pool.SetMaxIdleConns(4)
	other, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	for iteration := range 5 {
		t.Run(fmt.Sprintf("两实例并发_%d", iteration), func(t *testing.T) {
			for _, user := range users[:2] {
				if err := service.SetProjectMember(context.Background(), users[3], project.ID, user.ID, ProjectOwnerRoleID); err != nil {
					t.Fatal(err)
				}
			}
			if err := service.SetProjectMember(context.Background(), users[3], project.ID, users[4].ID, role.ID); err != nil {
				t.Fatal(err)
			}
			if err := other.syncPolicy("test_owner_removal"); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var tasks sync.WaitGroup
			for i, instance := range []*Service{service, other} {
				tasks.Add(1)
				go func(i int, instance *Service) {
					defer tasks.Done()
					<-start
					actor := users[2]
					if i == 1 {
						actor = users[4]
					}
					results <- instance.SetProjectMember(context.Background(), actor, project.ID, users[i].ID, "")
				}(i, instance)
			}
			close(start)
			tasks.Wait()
			close(results)
			successes := 0
			for err := range results {
				if err == nil {
					successes++
				} else if !errors.Is(err, ErrProjectLastOwner) {
					t.Errorf("并发冲突没有返回管理员保护错误: %v", err)
				}
			}
			var count int64
			if err := db.Model(&model.ProjectMember{}).Where("project_id = ? AND role_id = ?", project.ID, ProjectOwnerRoleID).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if successes != 1 || count != 1 {
				t.Fatalf("并发移除未保留唯一管理员: successes=%d owners=%d", successes, count)
			}
		})
	}
}

func TestProjectMemberRechecksAuthorizationAfterProjectLock(t *testing.T) {
	for _, mutation := range []string{"移除成员但保留个人授权", "删除角色权限", "个人拒绝", "账号停用", "迁出部门"} {
		t.Run(mutation, func(t *testing.T) {
			db, service, users, project, role := projectMemberOwnerFixture(t)
			if err := service.SetUserPermissions(context.Background(), users[2].ID, UserPermissionOverrides{Allow: []string{"project.member.update"}}); err != nil {
				t.Fatal(err)
			}
			// 删角色权限的情况不能再由个人 allow 获权。
			if mutation == "删除角色权限" {
				if err := service.SetUserPermissions(context.Background(), users[2].ID, UserPermissionOverrides{}); err != nil {
					t.Fatal(err)
				}
			}
			var injected atomic.Bool
			callback := "test:project_member_lock_revoke"
			if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table != "projects" || injected.Swap(true) {
					return
				}
				var err error
				switch mutation {
				case "移除成员但保留个人授权":
					err = tx.Exec("DELETE FROM project_members WHERE project_id = ? AND user_id = ?", project.ID, users[2].ID).Error
				case "删除角色权限":
					err = tx.Exec("DELETE FROM role_permissions WHERE role_id = ?", role.ID).Error
				case "个人拒绝":
					err = tx.Exec("DELETE FROM user_permissions WHERE user_id = ?", users[2].ID).Error
					if err == nil {
						err = tx.Session(&gorm.Session{NewDB: true}).Create(&model.UserPermission{UserID: users[2].ID, Permission: "project.member.update", Effect: model.PermissionDeny}).Error
					}
				case "账号停用":
					err = tx.Exec("UPDATE users SET is_active = ? WHERE uuid = ?", false, users[2].ID).Error
				case "迁出部门":
					err = tx.Exec("UPDATE users SET department_id = ? WHERE uuid = ?", uuid.NewString(), users[2].ID).Error
				}
				if err != nil {
					tx.AddError(err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
			err := service.SetProjectMember(context.Background(), users[2], project.ID, users[0].ID, "")
			if !injected.Load() || !errors.Is(err, ErrAccessDelegationDenied) {
				t.Fatalf("加锁后没有复核最新成员和授权: injected=%v err=%v", injected.Load(), err)
			}
			var member model.ProjectMember
			if err := db.Where("project_id = ? AND user_id = ?", project.ID, users[0].ID).First(&member).Error; err != nil || member.RoleID != ProjectOwnerRoleID {
				t.Fatalf("被拒绝的请求改变了目标管理员: role=%s err=%v", member.RoleID, err)
			}
		})
	}
}

func TestProjectMemberConcurrentOwnersCannotRemoveEachOther(t *testing.T) {
	db, service, users, project, _ := projectMemberOwnerFixture(t)
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(4)
	pool.SetMaxIdleConns(4)
	other, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	for iteration := range 5 {
		t.Run(fmt.Sprintf("相互移除_%d", iteration), func(t *testing.T) {
			for _, user := range users[:2] {
				if err := service.SetProjectMember(context.Background(), users[3], project.ID, user.ID, ProjectOwnerRoleID); err != nil {
					t.Fatal(err)
				}
			}
			if err := other.syncPolicy("test_owner_mutual_removal"); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for i, instance := range []*Service{service, other} {
				go func(i int, instance *Service) {
					<-start
					results <- instance.SetProjectMember(context.Background(), users[i], project.ID, users[1-i].ID, "")
				}(i, instance)
			}
			close(start)
			successes := 0
			for range 2 {
				err := <-results
				if err == nil {
					successes++
				} else if !errors.Is(err, ErrAccessDelegationDenied) && !errors.Is(err, ErrProjectNotFound) && !errors.Is(err, ErrProjectLastOwner) {
					t.Errorf("并发撤权出现非业务错误: %v", err)
				}
			}
			var count int64
			if err := db.Model(&model.ProjectMember{}).Where("project_id = ? AND role_id = ?", project.ID, ProjectOwnerRoleID).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if successes != 1 || count != 1 {
				t.Fatalf("已被移除的管理员仍完成了旧请求: successes=%d owners=%d", successes, count)
			}
		})
	}
}

func projectMemberOwnerFixture(t *testing.T) (*gorm.DB, *Service, []*model.User, *model.Project, *model.Role) {
	t.Helper()
	db := testutil.NewDatabase(t)
	users := make([]*model.User, 5)
	for i := range users {
		users[i] = &model.User{ID: uuid.NewString(), Username: fmt.Sprintf("pm-owner-%d-%s", i, uuid.NewString()[:8]), DepartmentID: database.DefaultDepartmentID, IsActive: true, IsSuperuser: i == 3, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		if err := db.Create(users[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	project, err := service.CreateProject(context.Background(), users[0], "成员管理员回归-"+uuid.NewString(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	role, err := service.CreateRole(context.Background(), RoleInput{Name: "member-manager-" + uuid.NewString(), DisplayName: "成员管理权限回归", Permissions: []string{"project.read", "project.member.update"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetProjectMember(context.Background(), users[3], project.ID, users[2].ID, role.ID); err != nil {
		t.Fatal(err)
	}
	return db, service, users, project, &role.Role
}
