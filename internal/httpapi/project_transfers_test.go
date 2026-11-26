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
	targetScope := scope
	targetScope.ProjectID = f.project.ID
	authorize := h.authorize(ctx, *f.user, scope, targetScope)
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

func TestProjectResourceTransferAdminCanMoveApplicationAcrossDepartments(t *testing.T) {
	f, admin, source, repositoryID, roleID := projectTransferHTTPFixture(t)
	departmentID := createDepartmentForHTTPTest(t, f.router, admin, "移动隔离部门")
	targetID := database.DefaultProjectID(departmentID)
	applicationID := createApplicationForDepartmentTest(t, f.router, admin, "transfer_department", repositoryID)
	input := map[string]any{
		"target_project_id": targetID, "include_dependencies": true,
		"resources": []map[string]string{{"kind": "application", "id": applicationID}},
	}
	// 即使遗留成员记录错误地跨部门关联，普通操作者仍不能读取或移动到该目标。
	if err := f.db.Create(&model.ProjectMember{ProjectID: targetID, UserID: f.user.ID, RoleID: roleID}).Error; err != nil {
		t.Fatal(err)
	}
	memberCatalog := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodGet, "/project-resource-transfers/catalog", source, nil)
	if memberCatalog.Code != http.StatusOK || strings.Contains(memberCatalog.Body.String(), targetID) {
		t.Fatalf("普通用户不应看到跨部门目标：%d %s", memberCatalog.Code, memberCatalog.Body.String())
	}
	for _, path := range []string{"/project-resource-transfers/preview", "/project-resource-transfers"} {
		response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, path, source, input)
		if response.Code != http.StatusForbidden {
			t.Fatalf("普通用户不能借跨部门成员记录移动资源：%d %s", response.Code, response.Body.String())
		}
	}
	catalog := deliveryVariableProjectRequest(t, f, admin, http.MethodGet, "/project-resource-transfers/catalog", source, nil)
	type projectView struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		DepartmentName string `json:"department_name"`
	}
	var directory struct {
		Source   projectView   `json:"source_project"`
		Projects []projectView `json:"projects"`
	}
	if err := json.Unmarshal(catalog.Body.Bytes(), &directory); err != nil || catalog.Code != http.StatusOK || directory.Source.ID != source || directory.Source.Name == "" || directory.Source.DepartmentName == "" {
		t.Fatalf("移动目录缺少准确来源项目：%d %s err=%v", catalog.Code, catalog.Body.String(), err)
	}
	found := false
	for _, target := range directory.Projects {
		if target.ID == targetID {
			found = true
			if target.Name == "" || target.DepartmentName != "移动隔离部门" {
				t.Fatalf("目标目录应显示实际项目及部门：%+v", target)
			}
		}
	}
	if !found {
		t.Fatalf("管理员的移动目录缺少其他部门的有效项目：%s", catalog.Body.String())
	}
	preview := deliveryVariableProjectRequest(t, f, admin, http.MethodPost, "/project-resource-transfers/preview", source, input)
	var plan projecttransfer.Plan
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil || preview.Code != http.StatusOK || plan.Confirmation == "" || len(plan.Blockers) != 0 || len(plan.Resources) != 3 {
		t.Fatalf("跨部门应用移动预览失败：%d %s err=%v", preview.Code, preview.Body.String(), err)
	}
	if plan.SourceDepartmentID != f.admin.DepartmentID || plan.TargetDepartmentID != departmentID {
		t.Fatalf("预览未冻结两端真实部门：%+v", plan)
	}
	var application model.Application
	if err := f.db.First(&application, "uuid = ?", applicationID).Error; err != nil || application.ProjectID != source || application.DepartmentID != f.admin.DepartmentID {
		t.Fatalf("预览不能修改应用归属：%v", err)
	}
	input["confirmation"] = plan.Confirmation
	response := deliveryVariableProjectRequest(t, f, admin, http.MethodPost, "/project-resource-transfers", source, input)
	if response.Code != http.StatusOK {
		t.Fatalf("管理员确认后跨部门移动失败：%d %s", response.Code, response.Body.String())
	}
	if err := f.db.First(&application, "uuid = ?", applicationID).Error; err != nil || application.ProjectID != targetID || application.DepartmentID != departmentID {
		t.Fatalf("应用未进入准确目标项目及部门：%+v err=%v", application, err)
	}
	var repository model.GitRepository
	if err := f.db.First(&repository, "uuid = ?", repositoryID).Error; err != nil || repository.ProjectID != targetID || repository.DepartmentID != departmentID {
		t.Fatalf("关联仓库未进入准确目标项目及部门：%+v err=%v", repository, err)
	}
	var workflow model.ReleaseWorkflow
	if err := f.db.First(&workflow, "application_id = ?", applicationID).Error; err != nil || workflow.ProjectID != targetID || workflow.DepartmentID != departmentID {
		t.Fatalf("关联流水线未进入准确目标项目及部门：%+v err=%v", workflow, err)
	}
	var entry model.AuditLog
	if err := f.db.Where("action = ? AND result = ?", access.PermissionProjectResourceMove, model.AuditSucceeded).Take(&entry).Error; err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(entry.Metadata, &metadata); err != nil || metadata["source_department_id"] != f.admin.DepartmentID || metadata["target_department_id"] != departmentID || entry.DepartmentID != f.admin.DepartmentID {
		t.Fatalf("跨部门移动审计应保留来源归属及两端部门：%+v err=%v", entry, err)
	}
}

func TestProjectResourceTransferCrossDepartmentRechecksAdminAndTargetScope(t *testing.T) {
	f, admin, sourceID, _, _ := projectTransferHTTPFixture(t)
	departmentID := createDepartmentForHTTPTest(t, f.router, admin, "事务复核部门")
	source := database.DepartmentScope{UserID: f.admin.ID, DepartmentID: f.admin.DepartmentID, ProjectID: sourceID, AllDepartments: true}
	target := database.DepartmentScope{UserID: f.admin.ID, DepartmentID: departmentID, ProjectID: database.DefaultProjectID(departmentID), AllDepartments: true}
	ctx := database.WithDepartmentScope(context.Background(), source)
	h := projectTransferHandler{access: f.deps.Access, logger: f.deps.Logger}
	authorize := h.authorize(ctx, *f.admin, source, target)
	if err := f.db.Transaction(authorize); err != nil {
		t.Fatalf("有效管理员跨部门授权被拒绝：%v", err)
	}
	wrongTarget := target
	wrongTarget.DepartmentID = source.DepartmentID
	if err := f.db.Transaction(h.authorize(ctx, *f.admin, source, wrongTarget)); !errors.Is(err, projecttransfer.ErrForbidden) {
		t.Fatalf("目标部门不匹配时仍通过事务授权：%v", err)
	}
	if err := f.db.Model(&model.User{}).Where("uuid = ?", f.admin.ID).Update("is_superuser", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Transaction(authorize); !errors.Is(err, projecttransfer.ErrForbidden) {
		t.Fatalf("撤销管理员后仍可使用旧预览移动：%v", err)
	}
}

func TestProjectResourceTransferApplicationDetectsDependenciesBeforeConfirmation(t *testing.T) {
	f, admin, source, repositoryID, _ := projectTransferHTTPFixture(t)
	applicationID := createApplicationForDepartmentTest(t, f.router, admin, "transfer_application", repositoryID)
	var workflow model.ReleaseWorkflow
	if err := f.db.First(&workflow, "application_id = ?", applicationID).Error; err != nil {
		t.Fatalf("读取应用默认流水线失败：%v", err)
	}
	input := map[string]any{
		"target_project_id":    f.project.ID,
		"include_dependencies": true,
		"resources":            []map[string]string{{"kind": "application", "id": applicationID}},
	}
	assertProject := func(want string) {
		t.Helper()
		var application model.Application
		if err := f.db.First(&application, "uuid = ?", applicationID).Error; err != nil || application.ProjectID != want {
			t.Fatalf("应用的项目归属不正确：got=%s want=%s err=%v", application.ProjectID, want, err)
		}
		var repository model.GitRepository
		if err := f.db.First(&repository, "uuid = ?", repositoryID).Error; err != nil || repository.ProjectID != want {
			t.Fatalf("关联仓库的项目归属不正确：got=%s want=%s err=%v", repository.ProjectID, want, err)
		}
		var currentWorkflow model.ReleaseWorkflow
		if err := f.db.First(&currentWorkflow, "uuid = ?", workflow.ID).Error; err != nil || currentWorkflow.ProjectID != want {
			t.Fatalf("关联流水线的项目归属不正确：got=%s want=%s err=%v", currentWorkflow.ProjectID, want, err)
		}
	}
	preview := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/project-resource-transfers/preview", source, input)
	var plan projecttransfer.Plan
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil || preview.Code != http.StatusOK || plan.Confirmation == "" || len(plan.Blockers) != 0 {
		t.Fatalf("应用关联资源预览失败：%d %s err=%v", preview.Code, preview.Body.String(), err)
	}
	if len(plan.Resources) != 3 {
		t.Fatalf("预览应列出应用和自动检测到的仓库、流水线：%+v", plan.Resources)
	}
	for _, item := range plan.Resources {
		switch item.Kind {
		case "application":
			if item.ID != applicationID || item.Automatic {
				t.Fatalf("用户选择的应用应保留原始身份：%+v", item)
			}
		case "repository":
			if item.ID != repositoryID || !item.Automatic {
				t.Fatalf("关联仓库应标记为自动检测：%+v", item)
			}
		case "workflow":
			if item.ID != workflow.ID || !item.Automatic {
				t.Fatalf("关联流水线应标记为自动检测：%+v", item)
			}
		default:
			t.Fatalf("预览包含无关资源：%+v", item)
		}
	}
	assertProject(source)
	missingConfirmation := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/project-resource-transfers", source, input)
	if missingConfirmation.Code != http.StatusConflict || !strings.Contains(missingConfirmation.Body.String(), "project_transfer_confirmation") {
		t.Fatalf("未确认关联资源范围时不能移动：%d %s", missingConfirmation.Code, missingConfirmation.Body.String())
	}
	assertProject(source)
	input["confirmation"] = plan.Confirmation
	response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/project-resource-transfers", source, input)
	if response.Code != http.StatusOK {
		t.Fatalf("确认后移动应用及关联仓库失败：%d %s", response.Code, response.Body.String())
	}
	assertProject(f.project.ID)
}

func TestProjectResourceTransferDependencyDetectionRequiresSingleApplication(t *testing.T) {
	f, admin, source, repositoryID, _ := projectTransferHTTPFixture(t)
	applicationID := createApplicationForDepartmentTest(t, f.router, admin, "transfer_selection", repositoryID)
	selections := []struct {
		name      string
		resources []map[string]string
	}{
		{name: "非应用资源", resources: []map[string]string{{"kind": "repository", "id": repositoryID}}},
		{name: "应用和仓库", resources: []map[string]string{{"kind": "application", "id": applicationID}, {"kind": "repository", "id": repositoryID}}},
		{name: "重复应用", resources: []map[string]string{{"kind": "application", "id": applicationID}, {"kind": "application", "id": applicationID}}},
	}
	for _, selection := range selections {
		t.Run(selection.name, func(t *testing.T) {
			input := map[string]any{"target_project_id": f.project.ID, "include_dependencies": true, "resources": selection.resources}
			for _, path := range []string{"/project-resource-transfers/preview", "/project-resource-transfers"} {
				response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, path, source, input)
				if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_project_transfer") {
					t.Fatalf("自动检测只能针对一个应用：path=%s status=%d body=%s", path, response.Code, response.Body.String())
				}
			}
		})
	}
	var application model.Application
	if err := f.db.First(&application, "uuid = ?", applicationID).Error; err != nil || application.ProjectID != source {
		t.Fatalf("无效选择不能改变应用归属：%v", err)
	}
	var repository model.GitRepository
	if err := f.db.First(&repository, "uuid = ?", repositoryID).Error; err != nil || repository.ProjectID != source {
		t.Fatalf("无效选择不能改变仓库归属：%v", err)
	}
}
