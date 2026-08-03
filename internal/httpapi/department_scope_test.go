package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"edo/internal/access"
)

func TestDepartmentScopeSeparatesRepositoryAndApplicationHTTPResources(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	adminCookie := loginForDepartmentTest(t, router, "admin")
	departmentA := createDepartmentForHTTPTest(t, router, adminCookie, "研发一部")
	departmentB := createDepartmentForHTTPTest(t, router, adminCookie, "研发二部")
	roleID := createRoleForHTTPTest(t, router, adminCookie, "department-resource-owner", []string{
		access.PermissionRepositoryRead,
		access.PermissionRepositoryCreate,
		access.PermissionRepositoryUpdate,
		access.PermissionRepositoryDelete,
		access.PermissionDeliveryRead,
		access.PermissionDeliveryCreate,
		access.PermissionDeliveryUpdate,
	})
	createUserForHTTPTest(t, router, adminCookie, "dept-a-owner", departmentA, []string{roleID})
	createUserForHTTPTest(t, router, adminCookie, "dept-b-owner", departmentB, []string{roleID})
	departmentACookie := loginForDepartmentTest(t, router, "dept-a-owner")
	departmentBCookie := loginForDepartmentTest(t, router, "dept-b-owner")

	repositoryA := createRepositoryForDepartmentTest(t, router, departmentACookie, "研发一部仓库")
	repositoryB := createRepositoryForDepartmentTest(t, router, departmentBCookie, "研发二部仓库")
	applicationA := createApplicationForDepartmentTest(t, router, departmentACookie, "department_a_app", repositoryA)
	applicationB := createApplicationForDepartmentTest(t, router, departmentBCookie, "department_b_app", repositoryB)

	assertListContainsOnlyDepartmentResource(t, router, departmentACookie, "/api/v1/repositories", "repositories", repositoryA, repositoryB)
	assertListContainsOnlyDepartmentResource(t, router, departmentBCookie, "/api/v1/repositories", "repositories", repositoryB, repositoryA)
	assertListContainsOnlyDepartmentResource(t, router, departmentACookie, "/api/v1/applications", "applications", applicationA, applicationB)
	assertListContainsOnlyDepartmentResource(t, router, departmentBCookie, "/api/v1/applications", "applications", applicationB, applicationA)

	foreignRepositoryUpdate := performJSONRequest(t, router, http.MethodPut, "/api/v1/repositories/"+repositoryB, map[string]any{
		"name": "不能修改的仓库", "provider": "generic", "clone_url": "https://git.example.com/team/b.git",
		"auth_type": "none",
	}, departmentACookie)
	if foreignRepositoryUpdate.Code != http.StatusNotFound {
		t.Fatalf("跨部门修改仓库未按不存在处理: status=%d body=%s", foreignRepositoryUpdate.Code, foreignRepositoryUpdate.Body.String())
	}
	foreignRepositoryDelete := performJSONRequest(t, router, http.MethodDelete, "/api/v1/repositories/"+repositoryB, nil, departmentACookie)
	if foreignRepositoryDelete.Code != http.StatusNotFound {
		t.Fatalf("跨部门删除仓库未按不存在处理: status=%d body=%s", foreignRepositoryDelete.Code, foreignRepositoryDelete.Body.String())
	}
	foreignApplicationUpdate := performJSONRequest(t, router, http.MethodPut, "/api/v1/applications/"+applicationB, map[string]any{
		"name": "不能修改的应用", "repository_id": repositoryA, "poll_interval_seconds": 60,
	}, departmentACookie)
	if foreignApplicationUpdate.Code != http.StatusNotFound {
		t.Fatalf("跨部门修改应用未按不存在处理: status=%d body=%s", foreignApplicationUpdate.Code, foreignApplicationUpdate.Body.String())
	}

	assertListContainsAllResources(t, router, adminCookie, "/api/v1/repositories", repositoryA, repositoryB)
	assertListContainsAllResources(t, router, adminCookie, "/api/v1/applications", applicationA, applicationB)
}

func TestDepartmentUserCreationMovementAndVisibilityHTTPPermissions(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	adminCookie := loginForDepartmentTest(t, router, "admin")
	departmentA := createDepartmentForHTTPTest(t, router, adminCookie, "交付一部")
	departmentB := createDepartmentForHTTPTest(t, router, adminCookie, "交付二部")
	roleID := createRoleForHTTPTest(t, router, adminCookie, "department-user-manager", []string{
		access.PermissionUserRead,
		access.PermissionUserCreate,
		access.PermissionUserUpdate,
		access.PermissionDepartmentRead,
	})
	managerA := createUserForHTTPTest(t, router, adminCookie, "manager-a", departmentA, []string{roleID})
	managerB := createUserForHTTPTest(t, router, adminCookie, "manager-b", departmentB, []string{roleID})
	managerACookie := loginForDepartmentTest(t, router, "manager-a")

	sameDepartment := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "member-a", "nickname": "同部门成员", "password": "correct horse battery staple",
	}, managerACookie)
	if sameDepartment.Code != http.StatusCreated || !bytes.Contains(sameDepartment.Body.Bytes(), []byte(`"department_id":"`+departmentA+`"`)) {
		t.Fatalf("普通用户不能在本部门创建用户: status=%d body=%s", sameDepartment.Code, sameDepartment.Body.String())
	}
	var member struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(sameDepartment.Body.Bytes(), &member); err != nil || member.User.ID == "" {
		t.Fatalf("解析本部门成员失败: payload=%+v err=%v", member, err)
	}

	crossDepartmentCreate := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "member-bad", "nickname": "越权成员", "password": "correct horse battery staple",
		"department_id": departmentB,
	}, managerACookie)
	if crossDepartmentCreate.Code != http.StatusForbidden {
		t.Fatalf("普通用户跨部门创建用户未被拒绝: status=%d body=%s", crossDepartmentCreate.Code, crossDepartmentCreate.Body.String())
	}
	crossDepartmentMove := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+member.User.ID+"/department", map[string]string{
		"department_id": departmentB,
	}, managerACookie)
	if crossDepartmentMove.Code != http.StatusForbidden {
		t.Fatalf("普通用户调整部门未被拒绝: status=%d body=%s", crossDepartmentMove.Code, crossDepartmentMove.Body.String())
	}

	usersA := performJSONRequest(t, router, http.MethodGet, "/api/v1/users", nil, managerACookie)
	if usersA.Code != http.StatusOK || !bytes.Contains(usersA.Body.Bytes(), []byte(managerA)) ||
		!bytes.Contains(usersA.Body.Bytes(), []byte(member.User.ID)) || bytes.Contains(usersA.Body.Bytes(), []byte(managerB)) {
		t.Fatalf("普通用户列表未按部门隔离: status=%d body=%s", usersA.Code, usersA.Body.String())
	}
	departmentsA := performJSONRequest(t, router, http.MethodGet, "/api/v1/departments", nil, managerACookie)
	if departmentsA.Code != http.StatusOK || !bytes.Contains(departmentsA.Body.Bytes(), []byte(departmentA)) ||
		bytes.Contains(departmentsA.Body.Bytes(), []byte(departmentB)) {
		t.Fatalf("普通用户部门列表未按部门隔离: status=%d body=%s", departmentsA.Code, departmentsA.Body.String())
	}

	moveByAdmin := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+member.User.ID+"/department", map[string]string{
		"department_id": departmentB,
	}, adminCookie)
	if moveByAdmin.Code != http.StatusNoContent {
		t.Fatalf("超级管理员调整用户部门失败: status=%d body=%s", moveByAdmin.Code, moveByAdmin.Body.String())
	}
	usersA = performJSONRequest(t, router, http.MethodGet, "/api/v1/users", nil, managerACookie)
	if usersA.Code != http.StatusOK || bytes.Contains(usersA.Body.Bytes(), []byte(member.User.ID)) {
		t.Fatalf("用户移动后仍出现在原部门列表: status=%d body=%s", usersA.Code, usersA.Body.String())
	}
	adminUsers := performJSONRequest(t, router, http.MethodGet, "/api/v1/users", nil, adminCookie)
	if adminUsers.Code != http.StatusOK || !bytes.Contains(adminUsers.Body.Bytes(), []byte(managerA)) ||
		!bytes.Contains(adminUsers.Body.Bytes(), []byte(managerB)) || !bytes.Contains(adminUsers.Body.Bytes(), []byte(member.User.ID)) {
		t.Fatalf("超级管理员不能跨部门查看用户: status=%d body=%s", adminUsers.Code, adminUsers.Body.String())
	}
}

func TestPermissionCatalogHTTPUsesChineseFineGrainedActions(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	adminCookie := loginForDepartmentTest(t, router, "admin")

	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/permissions", nil, adminCookie)
	if response.Code != http.StatusOK {
		t.Fatalf("读取权限目录失败: status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Permissions []access.Permission `json:"permissions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析权限目录失败: %v", err)
	}
	wantActions := map[string]bool{"查看": false, "创建": false, "修改": false, "删除": false, "执行": false}
	for _, permission := range payload.Permissions {
		if permission.Code == access.PermissionRepositoryManage || permission.Code == access.PermissionDeliveryRun ||
			permission.Action == "manage" || permission.Action == "run" {
			t.Fatalf("权限目录仍向前端暴露旧聚合权限: %+v", permission)
		}
		if _, ok := wantActions[permission.ActionName]; ok {
			wantActions[permission.ActionName] = true
		}
		if permission.Name == "" || permission.Group == "" || permission.ResourceName == "" || permission.ActionName == "" {
			t.Fatalf("权限目录存在不可读元数据: %+v", permission)
		}
	}
	for action, found := range wantActions {
		if !found {
			t.Fatalf("权限目录缺少中文动作 %q", action)
		}
	}
}

func TestRepositoryUpdateAndDeletePermissionsAreIndependent(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	adminCookie := loginForDepartmentTest(t, router, "admin")

	updateRoleID := createRoleForHTTPTest(t, router, adminCookie, "repository-updater", []string{
		access.PermissionRepositoryUpdate,
	})
	deleteRoleID := createRoleForHTTPTest(t, router, adminCookie, "repository-deleter", []string{
		access.PermissionRepositoryDelete,
	})
	createUserForHTTPTest(t, router, adminCookie, "repository-updater", "", []string{updateRoleID})
	createUserForHTTPTest(t, router, adminCookie, "repository-deleter", "", []string{deleteRoleID})
	updateCookie := loginForDepartmentTest(t, router, "repository-updater")
	deleteCookie := loginForDepartmentTest(t, router, "repository-deleter")
	updateBody := map[string]any{
		"name": "不存在的仓库", "provider": "generic",
		"clone_url": "https://git.example.com/team/missing.git", "auth_type": "none",
	}

	if response := performJSONRequest(t, router, http.MethodPut, "/api/v1/repositories/missing", updateBody, updateCookie); response.Code != http.StatusNotFound {
		t.Fatalf("只有修改权限时未进入修改业务校验: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := performJSONRequest(t, router, http.MethodDelete, "/api/v1/repositories/missing", nil, updateCookie); response.Code != http.StatusForbidden {
		t.Fatalf("修改权限不应隐含删除权限: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := performJSONRequest(t, router, http.MethodPut, "/api/v1/repositories/missing", updateBody, deleteCookie); response.Code != http.StatusForbidden {
		t.Fatalf("删除权限不应隐含修改权限: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := performJSONRequest(t, router, http.MethodDelete, "/api/v1/repositories/missing", nil, deleteCookie); response.Code != http.StatusNotFound {
		t.Fatalf("只有删除权限时未进入删除业务校验: status=%d body=%s", response.Code, response.Body.String())
	}
}

func loginForDepartmentTest(t *testing.T, router http.Handler, username string) *http.Cookie {
	t.Helper()
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": username, "password": "correct horse battery staple",
	}, nil)
	if response.Code != http.StatusOK || len(response.Result().Cookies()) == 0 {
		t.Fatalf("用户 %s 登录失败: status=%d body=%s", username, response.Code, response.Body.String())
	}
	return response.Result().Cookies()[0]
}

func createDepartmentForHTTPTest(t *testing.T, router http.Handler, cookie *http.Cookie, name string) string {
	t.Helper()
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/departments", map[string]string{
		"name": name, "description": name + "测试范围",
	}, cookie)
	var payload struct {
		Department struct {
			ID string `json:"id"`
		} `json:"department"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Department.ID == "" {
		t.Fatalf("创建部门 %s 失败: status=%d body=%s", name, response.Code, response.Body.String())
	}
	return payload.Department.ID
}

func createRoleForHTTPTest(t *testing.T, router http.Handler, cookie *http.Cookie, name string, permissions []string) string {
	t.Helper()
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/roles", map[string]any{
		"name": name, "display_name": name, "permissions": permissions,
	}, cookie)
	var payload struct {
		Role struct {
			ID string `json:"id"`
		} `json:"role"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Role.ID == "" {
		t.Fatalf("创建测试角色 %s 失败: status=%d body=%s", name, response.Code, response.Body.String())
	}
	return payload.Role.ID
}

func createUserForHTTPTest(t *testing.T, router http.Handler, cookie *http.Cookie, username, departmentID string, roleIDs []string) string {
	t.Helper()
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": username, "nickname": username, "password": "correct horse battery staple",
		"department_id": departmentID, "role_ids": roleIDs,
	}, cookie)
	var payload struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.User.ID == "" {
		t.Fatalf("创建测试用户 %s 失败: status=%d body=%s", username, response.Code, response.Body.String())
	}
	return payload.User.ID
}

func createRepositoryForDepartmentTest(t *testing.T, router http.Handler, cookie *http.Cookie, name string) string {
	t.Helper()
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/repositories", map[string]any{
		"name": name, "provider": "generic", "clone_url": "https://git.example.com/" + name + ".git",
		"auth_type": "none", "webhook_enabled": false,
	}, cookie)
	var payload struct {
		Repository struct {
			ID string `json:"id"`
		} `json:"repository"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Repository.ID == "" {
		t.Fatalf("创建测试仓库 %s 失败: status=%d body=%s", name, response.Code, response.Body.String())
	}
	return payload.Repository.ID
}

func createApplicationForDepartmentTest(t *testing.T, router http.Handler, cookie *http.Cookie, name, repositoryID string) string {
	t.Helper()
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/applications", map[string]any{
		"name": name, "repository_id": repositoryID, "poll_interval_seconds": 60,
	}, cookie)
	var payload struct {
		Application struct {
			ID string `json:"id"`
		} `json:"application"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Application.ID == "" {
		t.Fatalf("创建测试应用 %s 失败: status=%d body=%s", name, response.Code, response.Body.String())
	}
	return payload.Application.ID
}

func assertListContainsOnlyDepartmentResource(
	t *testing.T,
	router http.Handler,
	cookie *http.Cookie,
	path, field, ownID, foreignID string,
) {
	t.Helper()
	response := performJSONRequest(t, router, http.MethodGet, path, nil, cookie)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(ownID)) ||
		bytes.Contains(response.Body.Bytes(), []byte(foreignID)) {
		t.Fatalf("%s 未按部门隔离 %s: status=%d body=%s", path, field, response.Code, response.Body.String())
	}
}

func assertListContainsAllResources(t *testing.T, router http.Handler, cookie *http.Cookie, path string, ids ...string) {
	t.Helper()
	response := performJSONRequest(t, router, http.MethodGet, path, nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("超级管理员读取 %s 失败: status=%d body=%s", path, response.Code, response.Body.String())
	}
	for _, id := range ids {
		if !bytes.Contains(response.Body.Bytes(), []byte(id)) {
			t.Fatalf("超级管理员读取 %s 缺少资源 %s: body=%s", path, id, response.Body.String())
		}
	}
}
