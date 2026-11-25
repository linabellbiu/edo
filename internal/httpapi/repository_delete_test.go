package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/repository"
)

func TestRepositoryDeleteRequiresCurrentProjectPermissionAndRecordsAudit(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	admin := loginForDepartmentTest(t, f.router, "admin")
	projectID := database.DefaultProjectID(f.admin.DepartmentID)
	id := createRepositoryForDepartmentTest(t, f.router, admin, "delete-boundary-repo")
	path := "/repositories/" + id

	if response := deliveryVariableProjectRequest(t, f, admin, http.MethodDelete, path, f.project.ID, nil); response.Code != http.StatusNotFound {
		t.Fatalf("其他项目不能删除仓库：%d %s", response.Code, response.Body.String())
	}
	updateRoleID := createRoleForHTTPTest(t, f.router, admin, "repo-delete-update-only", []string{access.PermissionRepositoryUpdate})
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, projectID, f.user.ID, updateRoleID); err != nil {
		t.Fatal(err)
	}
	if response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodDelete, path, projectID, nil); response.Code != http.StatusForbidden {
		t.Fatalf("仓库修改权限不能代替删除授权：%d %s", response.Code, response.Body.String())
	}
	assertRepositoryDeleteAudit(t, f, id, projectID, f.user.ID, model.AuditDenied)

	deleteRoleID := createRoleForHTTPTest(t, f.router, admin, "repo-delete-authorized", []string{access.PermissionRepositoryDelete})
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, projectID, f.user.ID, deleteRoleID); err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, deleteRoleID); err != nil {
		t.Fatal(err)
	}
	if response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodDelete, path, f.project.ID, nil); response.Code != http.StatusNotFound {
		t.Fatalf("其他项目的删除授权不能跨项目删除仓库：%d %s", response.Code, response.Body.String())
	}
	if response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodDelete, path, projectID, nil); response.Code != http.StatusNoContent {
		t.Fatalf("具备独立删除权限的项目成员应返回 204：%d %s", response.Code, response.Body.String())
	}
	var deleted model.GitRepository
	if err := f.db.First(&deleted, "uuid = ?", id).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("成功删除后仓库仍存在：%v", err)
	}
	var retained model.GitRepository
	if err := f.db.Unscoped().First(&retained, "uuid = ?", id).Error; err != nil {
		t.Fatalf("删除仓库应保留历史记录：%v", err)
	}
	assertRepositoryDeleteAudit(t, f, id, projectID, f.user.ID, model.AuditSucceeded)
	if response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodDelete, path, projectID, nil); response.Code != http.StatusNotFound {
		t.Fatalf("重复删除应返回 404：%d %s", response.Code, response.Body.String())
	}

	remainingID := createRepositoryForDepartmentTest(t, f.router, admin, "delete-revoked-repo")
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, projectID, f.user.ID, updateRoleID); err != nil {
		t.Fatal(err)
	}
	if response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodDelete, "/repositories/"+remainingID, projectID, nil); response.Code != http.StatusForbidden {
		t.Fatalf("同一会话撤销删除权限后仍允许删除：%d %s", response.Code, response.Body.String())
	}
	var remaining model.GitRepository
	if err := f.db.First(&remaining, "uuid = ?", remainingID).Error; err != nil {
		t.Fatalf("拒绝删除时不应修改仓库：%v", err)
	}
}

func TestRepositoryDeleteReferenceBoundariesPreserveHistory(t *testing.T) {
	for _, test := range []struct {
		name     string
		archived bool
		inactive bool
	}{
		{name: "正在使用"},
		{name: "停用应用仍引用", inactive: true},
		{name: "归档应用保留历史", archived: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newStreamBoundaryFixture(t)
			admin := loginForDepartmentTest(t, f.router, "admin")
			projectID := database.DefaultProjectID(f.admin.DepartmentID)
			id := createRepositoryForDepartmentTest(t, f.router, admin, "delete-in-use-repo")
			applicationID := createApplicationForDepartmentTest(t, f.router, admin, "delete_in_use_app", id)
			if test.inactive {
				if err := f.db.Model(&model.Application{}).Where("uuid = ?", applicationID).Update("is_active", false).Error; err != nil {
					t.Fatal(err)
				}
			}
			if test.archived {
				if response := deliveryVariableProjectRequest(t, f, admin, http.MethodDelete, "/applications/"+applicationID, projectID, nil); response.Code != http.StatusNoContent {
					t.Fatalf("归档测试应用失败：%d %s", response.Code, response.Body.String())
				}
			}
			roleID := createRoleForHTTPTest(t, f.router, admin, "repo-delete-in-use", []string{access.PermissionRepositoryDelete})
			if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, projectID, f.user.ID, roleID); err != nil {
				t.Fatal(err)
			}

			response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodDelete, "/repositories/"+id, projectID, nil)
			if test.archived {
				if response.Code != http.StatusNoContent {
					t.Fatalf("仅有归档应用历史引用时应允许删除：%d %s", response.Code, response.Body.String())
				}
				var history model.Application
				if err := f.db.Unscoped().First(&history, "uuid = ?", applicationID).Error; err != nil || history.RepositoryID != id || !history.DeletedAt.Valid {
					t.Fatalf("删除仓库不应破坏归档应用历史：%v", err)
				}
				var retained model.GitRepository
				if err := f.db.Unscoped().First(&retained, "uuid = ?", id).Error; err != nil {
					t.Fatalf("删除仓库应保留历史记录：%v", err)
				}
				var visible model.GitRepository
				if err := f.db.First(&visible, "uuid = ?", id).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
					t.Fatalf("删除后的仓库不应在正常查询中可见：%v", err)
				}
				assertRepositoryDeleteAudit(t, f, id, projectID, f.user.ID, model.AuditSucceeded)
				return
			}
			var payload struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || response.Code != http.StatusConflict || payload.Code != "repository_in_use" || payload.Message != repository.ErrRepositoryInUse.Error() {
				t.Fatalf("引用中的仓库应返回稳定 409 提示：status=%d body=%s err=%v", response.Code, response.Body.String(), err)
			}
			var repo model.GitRepository
			if err := f.db.First(&repo, "uuid = ?", id).Error; err != nil {
				t.Fatalf("冲突不应删除仓库：%v", err)
			}
			var app model.Application
			if err := f.db.Unscoped().First(&app, "uuid = ?", applicationID).Error; err != nil || app.RepositoryID != id || app.DeletedAt.Valid || app.IsActive == test.inactive {
				t.Fatalf("冲突不应改变应用引用或归档状态：%v", err)
			}
			assertRepositoryDeleteAudit(t, f, id, projectID, f.user.ID, model.AuditFailed)
		})
	}
}

func assertRepositoryDeleteAudit(t *testing.T, f *streamBoundaryFixture, repositoryID, projectID, actorID string, result model.AuditResult) {
	t.Helper()
	var audit model.AuditLog
	if err := f.db.First(&audit, "action = ? AND resource_id = ? AND project_id = ? AND result = ? AND actor_user_id = ?", "repository.delete", repositoryID, projectID, result, actorID).Error; err != nil || audit.ResourceType != "repository" || audit.RequestID == "" {
		t.Fatalf("缺少准确目标、项目和操作者的删除审计：result=%s err=%v", result, err)
	}
}
