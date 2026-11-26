package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/projecttransfer"
)

func projectTransferHTTPFixture(t *testing.T) (*streamBoundaryFixture, *http.Cookie, string, string, string) {
	t.Helper()
	f := newStreamBoundaryFixture(t)
	admin := loginForDepartmentTest(t, f.router, "admin")
	source := database.DefaultProjectID(f.admin.DepartmentID)
	repositoryID := createRepositoryForDepartmentTest(t, f.router, admin, "project-move-repo")
	roleID := createRoleForHTTPTest(t, f.router, admin, "project-resource-mover", []string{access.PermissionProjectResourceMove})
	for _, id := range []string{source, f.project.ID} {
		if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, id, f.user.ID, roleID); err != nil {
			t.Fatal(err)
		}
	}
	return f, admin, source, repositoryID, roleID
}

func projectTransferHTTPInput(target, repositoryID string) map[string]any {
	return map[string]any{"target_project_id": target, "resources": []map[string]string{{"kind": "repository", "id": repositoryID}}}
}

func TestProjectResourceTransferRequiresBothProjectsAndCurrentPermission(t *testing.T) {
	f, admin, source, repositoryID, roleID := projectTransferHTTPFixture(t)
	input := projectTransferHTTPInput(f.project.ID, repositoryID)
	readRole := createRoleForHTTPTest(t, f.router, admin, "project-resource-read-only", []string{"project.read", "project.update", "repository.update"})
	for _, projectID := range []string{source, f.project.ID} {
		if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, projectID, f.user.ID, readRole); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/project-resource-transfers/preview", "/project-resource-transfers"} {
			response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, path, source, input)
			if response.Code != http.StatusForbidden {
				t.Fatalf("项目或资源修改权限不能代替两端移动授权：project=%s status=%d body=%s", projectID, response.Code, response.Body.String())
			}
		}
		if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, projectID, f.user.ID, roleID); err != nil {
			t.Fatal(err)
		}
	}
	// 模拟其他实例已撤权但本机 Casbin 未刷新，目录与预览仍应使用数据库中的当前授权。
	if err := f.db.Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", f.project.ID, f.user.ID).Update("role_id", readRole).Error; err != nil {
		t.Fatal(err)
	}
	catalog := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodGet, "/project-resource-transfers/catalog", source, nil)
	if catalog.Code != http.StatusOK || strings.Contains(catalog.Body.String(), f.project.ID) {
		t.Fatalf("移动目录仍显示已撤权目标：%d %s", catalog.Code, catalog.Body.String())
	}
	response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/project-resource-transfers/preview", source, input)
	if response.Code != http.StatusForbidden {
		t.Fatalf("撤权后预览仍被允许：%d %s", response.Code, response.Body.String())
	}
	var repo model.GitRepository
	if err := f.db.First(&repo, "uuid = ?", repositoryID).Error; err != nil || repo.ProjectID != source {
		t.Fatalf("拒绝移动时不能改变原项目：%v", err)
	}
}

func TestProjectResourceTransferMovesWithOneAtomicAudit(t *testing.T) {
	f, _, source, repositoryID, _ := projectTransferHTTPFixture(t)
	input := projectTransferHTTPInput(f.project.ID, repositoryID)
	preview := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/project-resource-transfers/preview", source, input)
	var plan projecttransfer.Plan
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil || preview.Code != http.StatusOK || plan.Confirmation == "" || len(plan.Blockers) != 0 {
		t.Fatalf("移动预览失败：%d %s err=%v", preview.Code, preview.Body.String(), err)
	}
	input["confirmation"] = plan.Confirmation
	response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/project-resource-transfers", source, input)
	if response.Code != http.StatusOK {
		t.Fatalf("授权用户移动资源失败：%d %s", response.Code, response.Body.String())
	}
	var repo model.GitRepository
	if err := f.db.First(&repo, "uuid = ?", repositoryID).Error; err != nil || repo.ProjectID != f.project.ID || repo.DepartmentID != f.project.DepartmentID {
		t.Fatalf("移动后资源项目不正确：%v", err)
	}
	var audits []model.AuditLog
	if err := f.db.Where("action = ? AND result = ?", access.PermissionProjectResourceMove, model.AuditSucceeded).Find(&audits).Error; err != nil || len(audits) != 1 {
		t.Fatalf("应只记录一次事务成功审计：count=%d err=%v", len(audits), err)
	}
	entry := audits[0]
	if entry.ProjectID != source || entry.ResourceID != source || entry.ActorUserID == nil || *entry.ActorUserID != f.user.ID || !strings.Contains(string(entry.Metadata), repositoryID) || !strings.Contains(string(entry.Metadata), f.project.ID) {
		t.Fatalf("移动审计缺少准确源、目标或资源身份：%+v", entry)
	}
}

func TestProjectResourceTransferAuditFailureRollsBack(t *testing.T) {
	f, _, source, repositoryID, _ := projectTransferHTTPFixture(t)
	input := projectTransferHTTPInput(f.project.ID, repositoryID)
	preview := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/project-resource-transfers/preview", source, input)
	var plan projecttransfer.Plan
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil || preview.Code != http.StatusOK || plan.Confirmation == "" {
		t.Fatalf("移动预览失败：%d %s err=%v", preview.Code, preview.Body.String(), err)
	}
	input["confirmation"] = plan.Confirmation
	const callback = "test:project-transfer-audit-failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if entry, ok := tx.Statement.Dest.(*model.AuditLog); ok && entry.Action == access.PermissionProjectResourceMove && entry.Result == model.AuditSucceeded {
			tx.AddError(errors.New("内部审计故障测试信息"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Create().Remove(callback) })
	response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/project-resource-transfers", source, input)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "内部审计故障") {
		t.Fatalf("审计失败须安全返回错误：%d %s", response.Code, response.Body.String())
	}
	var repo model.GitRepository
	if err := f.db.First(&repo, "uuid = ?", repositoryID).Error; err != nil || repo.ProjectID != source {
		t.Fatalf("审计失败未回滚移动：%v", err)
	}
}

func TestProjectResourceTransferTransactionRechecksRevokedIdentity(t *testing.T) {
	f, _, source, _, roleID := projectTransferHTTPFixture(t)
	scope := database.DepartmentScope{UserID: f.user.ID, DepartmentID: f.user.DepartmentID, ProjectID: source}
	ctx := database.WithDepartmentScope(context.Background(), scope)
	h := projectTransferHandler{access: f.deps.Access, logger: f.deps.Logger}
	authorize := h.authorize(ctx, *f.user, scope, f.project.ID)
	if err := f.db.Transaction(authorize); err != nil {
		t.Fatalf("有效授权不能被拒绝：%v", err)
	}
	if err := f.db.Where("role_id = ? AND permission = ?", roleID, access.PermissionProjectResourceMove).Delete(&model.RolePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Transaction(authorize); !errors.Is(err, projecttransfer.ErrForbidden) {
		t.Fatalf("事务内仍沿用已撤销的授权：%v", err)
	}
}

func TestProjectResourceTransferRequiresConfirmationAndFreshTargetAfterPreview(t *testing.T) {
	f, admin, source, repositoryID, _ := projectTransferHTTPFixture(t)
	input := projectTransferHTTPInput(f.project.ID, repositoryID)
	missingConfirmation := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/project-resource-transfers", source, input)
	if missingConfirmation.Code != http.StatusConflict {
		t.Fatalf("缺少预览确认时不能移动：%d %s", missingConfirmation.Code, missingConfirmation.Body.String())
	}
	preview := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/project-resource-transfers/preview", source, input)
	var plan projecttransfer.Plan
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil || preview.Code != http.StatusOK || plan.Confirmation == "" {
		t.Fatalf("移动预览失败：%d %s err=%v", preview.Code, preview.Body.String(), err)
	}
	input["confirmation"] = plan.Confirmation
	role := createRoleForHTTPTest(t, f.router, admin, "project-transfer-revoked-target", []string{"project.read"})
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, role); err != nil {
		t.Fatal(err)
	}
	response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/project-resource-transfers", source, input)
	if response.Code != http.StatusForbidden {
		t.Fatalf("预览凭据不能绕过之后的目标撤权：%d %s", response.Code, response.Body.String())
	}
	var repo model.GitRepository
	if err := f.db.First(&repo, "uuid = ?", repositoryID).Error; err != nil || repo.ProjectID != source {
		t.Fatalf("未确认或已撤权时不能改变项目归属：%v", err)
	}
}

func TestProjectResourceTransferCatalogHonorsExplicitDeny(t *testing.T) {
	f, _, source, _, _ := projectTransferHTTPFixture(t)
	catalog := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodGet, "/project-resource-transfers/catalog", source, nil)
	if catalog.Code != http.StatusOK || !strings.Contains(catalog.Body.String(), f.project.ID) {
		t.Fatalf("具备目标权限时目录缺少目标：%d %s", catalog.Code, catalog.Body.String())
	}
	if err := f.deps.Access.SetUserPermissions(context.Background(), f.user.ID, access.UserPermissionOverrides{Deny: []string{access.PermissionProjectResourceMove}}); err != nil {
		t.Fatal(err)
	}
	catalog = deliveryVariableProjectRequest(t, f, f.cookie, http.MethodGet, "/project-resource-transfers/catalog", source, nil)
	if catalog.Code != http.StatusForbidden {
		t.Fatalf("显式拒绝后仍可读取资源移动目录：%d %s", catalog.Code, catalog.Body.String())
	}
}

func TestProjectResourceTransferRejectsOtherDepartmentEvenForAdmin(t *testing.T) {
	f, admin, source, repositoryID, _ := projectTransferHTTPFixture(t)
	departmentID := createDepartmentForHTTPTest(t, f.router, admin, "移动隔离部门")
	response := deliveryVariableProjectRequest(t, f, admin, http.MethodPost, "/project-resource-transfers/preview", source, projectTransferHTTPInput(database.DefaultProjectID(departmentID), repositoryID))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("跨部门移动应被明确拒绝：%d %s", response.Code, response.Body.String())
	}
	catalog := deliveryVariableProjectRequest(t, f, admin, http.MethodGet, "/project-resource-transfers/catalog", source, nil)
	if catalog.Code != http.StatusOK || strings.Contains(catalog.Body.String(), database.DefaultProjectID(departmentID)) {
		t.Fatalf("管理员的移动目录不应包含其他部门：%d %s", catalog.Code, catalog.Body.String())
	}
}
