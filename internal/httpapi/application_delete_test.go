package httpapi

import (
	"context"
	"net/http"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
)

func TestApplicationDeleteRequiresProjectPermissionAndRecordsAudit(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	admin := loginForDepartmentTest(t, f.router, "admin")
	repositoryID := createRepositoryForDepartmentTest(t, f.router, admin, "delete-app-repo")
	id := createApplicationForDepartmentTest(t, f.router, admin, "delete_app", repositoryID)
	path := "/applications/" + id
	projectID := database.DefaultProjectID(f.admin.DepartmentID)
	if response := deliveryVariableProjectRequest(t, f, admin, http.MethodDelete, path, f.project.ID, nil); response.Code != http.StatusNotFound {
		t.Fatalf("其他项目不能删除应用：%d %s", response.Code, response.Body.String())
	}
	roleID := createRoleForHTTPTest(t, f.router, admin, "app-edit-only", []string{"application.read", "application.update"})
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, projectID, f.user.ID, roleID); err != nil {
		t.Fatal(err)
	}
	if response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodDelete, path, projectID, nil); response.Code != http.StatusForbidden {
		t.Fatalf("应用编辑权限不能代替删除授权：%d %s", response.Code, response.Body.String())
	}
	deleteRoleID := createRoleForHTTPTest(t, f.router, admin, "app-delete", []string{"application.read", "application.delete"})
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, projectID, f.user.ID, deleteRoleID); err != nil {
		t.Fatal(err)
	}
	if response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodDelete, path, projectID, nil); response.Code != http.StatusNoContent {
		t.Fatalf("具备独立删除权限的项目成员应返回 204：%d %s", response.Code, response.Body.String())
	}
	var archived model.Application
	if err := f.db.Unscoped().First(&archived, "uuid = ?", id).Error; err != nil || !archived.DeletedAt.Valid {
		t.Fatalf("应用应保留软删除记录：%v", err)
	}
	var audit model.AuditLog
	if err := f.db.First(&audit, "action = ? AND resource_id = ? AND result = ?", "application.delete", id, model.AuditSucceeded).Error; err != nil || audit.ProjectID != projectID || audit.ActorUserID == nil || *audit.ActorUserID != f.user.ID {
		t.Fatalf("缺少当前项目的成功删除审计：%v", err)
	}
	if response := deliveryVariableProjectRequest(t, f, admin, http.MethodDelete, path, projectID, nil); response.Code != http.StatusNotFound {
		t.Fatalf("重复删除应返回 404：%d %s", response.Code, response.Body.String())
	}
}
