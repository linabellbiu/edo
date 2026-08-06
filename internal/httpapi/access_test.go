package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"edo/internal/access"
)

func TestRBACAndAuditAPI(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	adminLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if adminLogin.Code != http.StatusOK {
		t.Fatalf("管理员登录失败: %s", adminLogin.Body.String())
	}
	adminCookie := adminLogin.Result().Cookies()[0]

	roleResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/roles", map[string]any{
		"name": "user-reader", "display_name": "用户查看员", "permissions": []string{"user.read"},
	}, adminCookie)
	if roleResponse.Code != http.StatusCreated {
		t.Fatalf("创建角色失败: status=%d body=%s", roleResponse.Code, roleResponse.Body.String())
	}
	var rolePayload struct {
		Role struct {
			ID string `json:"id"`
		} `json:"role"`
	}
	if err := json.Unmarshal(roleResponse.Body.Bytes(), &rolePayload); err != nil || rolePayload.Role.ID == "" {
		t.Fatalf("解析角色响应失败: payload=%+v err=%v", rolePayload, err)
	}

	userResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "reader", "nickname": "只读用户", "password": "correct horse battery staple",
		"role_ids": []string{rolePayload.Role.ID},
	}, adminCookie)
	if userResponse.Code != http.StatusCreated {
		t.Fatalf("创建普通用户失败: status=%d body=%s", userResponse.Code, userResponse.Body.String())
	}
	var userPayload struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(userResponse.Body.Bytes(), &userPayload); err != nil || userPayload.User.ID == "" {
		t.Fatalf("解析用户响应失败: payload=%+v err=%v", userPayload, err)
	}

	readerLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "reader", "password": "correct horse battery staple",
	}, nil)
	if readerLogin.Code != http.StatusOK || !bytes.Contains(readerLogin.Body.Bytes(), []byte(`"user.read"`)) {
		t.Fatalf("普通用户登录或权限响应错误: status=%d body=%s", readerLogin.Code, readerLogin.Body.String())
	}
	readerCookie := readerLogin.Result().Cookies()[0]

	users := performJSONRequest(t, router, http.MethodGet, "/api/v1/users", nil, readerCookie)
	if users.Code != http.StatusOK {
		t.Fatalf("已授权用户不能查看用户列表: status=%d body=%s", users.Code, users.Body.String())
	}
	denied := performJSONRequest(t, router, http.MethodPost, "/api/v1/roles", map[string]any{
		"name": "forbidden", "display_name": "不应创建", "permissions": []string{},
	}, readerCookie)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("越权创建角色未被拒绝: status=%d body=%s", denied.Code, denied.Body.String())
	}
	override := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userPayload.User.ID+"/permissions", map[string]any{
		"allow": []string{"role.read"}, "deny": []string{"user.read"},
	}, adminCookie)
	if override.Code != http.StatusNoContent {
		t.Fatalf("配置用户权限覆盖失败: status=%d body=%s", override.Code, override.Body.String())
	}
	users = performJSONRequest(t, router, http.MethodGet, "/api/v1/users", nil, readerCookie)
	if users.Code != http.StatusForbidden {
		t.Fatalf("显式拒绝未覆盖角色授权: status=%d body=%s", users.Code, users.Body.String())
	}
	roles := performJSONRequest(t, router, http.MethodGet, "/api/v1/roles", nil, readerCookie)
	if roles.Code != http.StatusOK {
		t.Fatalf("用户级额外授权未生效: status=%d body=%s", roles.Code, roles.Body.String())
	}

	audits := performJSONRequest(t, router, http.MethodGet, "/api/v1/audit-logs", nil, adminCookie)
	if audits.Code != http.StatusOK || !bytes.Contains(audits.Body.Bytes(), []byte(`"role.create"`)) ||
		!bytes.Contains(audits.Body.Bytes(), []byte(`"denied"`)) {
		t.Fatalf("审计日志未记录成功或拒绝操作: status=%d body=%s", audits.Code, audits.Body.String())
	}
	selfDelete := performJSONRequest(t, router, http.MethodDelete, "/api/v1/users/"+currentUserIDFromResponse(t, adminLogin.Body.Bytes()), nil, adminCookie)
	if selfDelete.Code != http.StatusConflict {
		t.Fatalf("删除当前登录账户未被拒绝: status=%d body=%s", selfDelete.Code, selfDelete.Body.String())
	}
	deleted := performJSONRequest(t, router, http.MethodDelete, "/api/v1/users/"+userPayload.User.ID, nil, adminCookie)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("拥有删除权限的管理员不能删除用户: status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	readerLogin = performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "reader", "password": "correct horse battery staple",
	}, nil)
	if readerLogin.Code != http.StatusUnauthorized {
		t.Fatalf("已删除用户仍能登录: status=%d body=%s", readerLogin.Code, readerLogin.Body.String())
	}
}

func TestAtomicUserAccessAPI(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	adminLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if adminLogin.Code != http.StatusOK {
		t.Fatalf("管理员登录失败: %s", adminLogin.Body.String())
	}
	adminCookie := adminLogin.Result().Cookies()[0]

	readerRoleID := createRoleForHTTPTest(t, router, adminCookie, "atomic-api-reader", []string{access.PermissionUserRead})
	operatorRoleID := createRoleForHTTPTest(t, router, adminCookie, "atomic-api-operator", []string{access.PermissionRoleUpdate})
	userID := createUserForHTTPTest(t, router, adminCookie, "atomic-api-user", "", []string{readerRoleID})

	var response *httptest.ResponseRecorder
	for label, body := range map[string]map[string]any{
		"全部缺失":        {},
		"缺少 role_ids": {"allow": []string{}, "deny": []string{}},
		"缺少 allow":    {"role_ids": []string{}, "deny": []string{}},
		"缺少 deny":     {"role_ids": []string{}, "allow": []string{}},
	} {
		response = performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userID+"/access", body, adminCookie)
		if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"invalid_request"`)) {
			t.Fatalf("%s时未拒绝原子访问配置请求: status=%d body=%s", label, response.Code, response.Body.String())
		}
	}

	response = performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userID+"/access", map[string]any{
		"role_ids": []string{operatorRoleID},
		"allow":    []string{access.PermissionAuditRead},
		"deny":     []string{access.PermissionRoleUpdate},
	}, adminCookie)
	if response.Code != http.StatusNoContent {
		t.Fatalf("原子配置用户访问权限失败: status=%d body=%s", response.Code, response.Body.String())
	}

	readUserAccess := func() ([]string, []string, []string) {
		t.Helper()
		usersResponse := performJSONRequest(t, router, http.MethodGet, "/api/v1/users?limit=200", nil, adminCookie)
		var payload struct {
			Users []struct {
				ID                  string   `json:"id"`
				RoleIDs             []string `json:"role_ids"`
				PermissionOverrides struct {
					Allow []string `json:"allow"`
					Deny  []string `json:"deny"`
				} `json:"permission_overrides"`
			} `json:"users"`
		}
		if usersResponse.Code != http.StatusOK || json.Unmarshal(usersResponse.Body.Bytes(), &payload) != nil {
			t.Fatalf("读取用户访问配置失败: status=%d body=%s", usersResponse.Code, usersResponse.Body.String())
		}
		for _, item := range payload.Users {
			if item.ID == userID {
				return item.RoleIDs, item.PermissionOverrides.Allow, item.PermissionOverrides.Deny
			}
		}
		t.Fatalf("用户列表缺少原子配置测试用户 %s", userID)
		return nil, nil, nil
	}
	assertUserAccess := func(wantRoles, wantAllow, wantDeny []string) {
		t.Helper()
		roleIDs, allow, deny := readUserAccess()
		if !slices.Equal(roleIDs, wantRoles) || !slices.Equal(allow, wantAllow) || !slices.Equal(deny, wantDeny) {
			t.Fatalf("用户访问配置不符合预期: roles=%v allow=%v deny=%v", roleIDs, allow, deny)
		}
	}
	assertUserAccess([]string{operatorRoleID}, []string{access.PermissionAuditRead}, []string{access.PermissionRoleUpdate})

	response = performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userID+"/access", map[string]any{
		"role_ids": []string{"missing-role"}, "allow": []string{access.PermissionUserDelete}, "deny": []string{},
	}, adminCookie)
	if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"invalid_roles"`)) {
		t.Fatalf("无效角色未返回安全业务错误: status=%d body=%s", response.Code, response.Body.String())
	}
	assertUserAccess([]string{operatorRoleID}, []string{access.PermissionAuditRead}, []string{access.PermissionRoleUpdate})

	adminID := currentUserIDFromResponse(t, adminLogin.Body.Bytes())
	response = performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+adminID+"/access", map[string]any{
		"role_ids": []string{readerRoleID}, "allow": []string{}, "deny": []string{},
	}, adminCookie)
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"superuser_immutable"`)) {
		t.Fatalf("超级管理员访问配置未被保护: status=%d body=%s", response.Code, response.Body.String())
	}

	audits := performJSONRequest(t, router, http.MethodGet, "/api/v1/audit-logs", nil, adminCookie)
	if audits.Code != http.StatusOK || !bytes.Contains(audits.Body.Bytes(), []byte(`"action":"user.access.update"`)) {
		t.Fatalf("原子用户访问配置未写入审计: status=%d body=%s", audits.Code, audits.Body.String())
	}

	response = performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userID+"/access", map[string]any{
		"role_ids": []string{}, "allow": []string{}, "deny": []string{},
	}, adminCookie)
	if response.Code != http.StatusNoContent {
		t.Fatalf("显式空数组不能清空用户访问配置: status=%d body=%s", response.Code, response.Body.String())
	}
	assertUserAccess([]string{}, []string{}, []string{})
}

func TestAtomicUserAccessAPIHonorsDepartmentScope(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	adminCookie := loginForDepartmentTest(t, router, "admin")
	departmentA := createDepartmentForHTTPTest(t, router, adminCookie, "原子权限一部")
	departmentB := createDepartmentForHTTPTest(t, router, adminCookie, "原子权限二部")
	managerRoleID := createRoleForHTTPTest(t, router, adminCookie, "atomic-access-manager", []string{
		access.PermissionUserUpdate,
	})
	managerID := createUserForHTTPTest(t, router, adminCookie, "atomic-manager-a", departmentA, []string{managerRoleID})
	targetID := createUserForHTTPTest(t, router, adminCookie, "atomic-member-b", departmentB, nil)
	if managerID == "" || targetID == "" {
		t.Fatal("创建部门作用域测试用户失败")
	}
	managerCookie := loginForDepartmentTest(t, router, "atomic-manager-a")

	response := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+targetID+"/access", map[string]any{
		"role_ids": []string{managerRoleID}, "allow": []string{}, "deny": []string{},
	}, managerCookie)
	if response.Code != http.StatusNotFound || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"user_not_found"`)) {
		t.Fatalf("跨部门配置用户访问权限未按不存在处理: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestUserAccessDelegationBoundaryHTTP(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	adminCookie := loginForDepartmentTest(t, router, "admin")
	managerRoleID := createRoleForHTTPTest(t, router, adminCookie, "http-delegation-manager", []string{
		access.PermissionUserCreate, access.PermissionUserUpdate, access.PermissionUserRead,
	})
	readerRoleID := createRoleForHTTPTest(t, router, adminCookie, "http-delegation-reader", []string{
		access.PermissionUserRead,
	})
	strongRoleID := createRoleForHTTPTest(t, router, adminCookie, "http-delegation-strong", []string{
		access.PermissionUserDelete,
	})
	managerID := createUserForHTTPTest(t, router, adminCookie, "http-delegation-manager", "", []string{managerRoleID})
	managerCookie := loginForDepartmentTest(t, router, "http-delegation-manager")

	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "http-delegation-bad", "nickname": "不应创建", "password": "correct horse battery staple",
		"role_ids": []string{strongRoleID},
	}, managerCookie)
	if response.Code != http.StatusForbidden || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"access_delegation_denied"`)) {
		t.Fatalf("创建用户接口允许普通管理员分配越权角色: status=%d body=%s", response.Code, response.Body.String())
	}

	response = performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "http-delegation-target", "nickname": "允许创建", "password": "correct horse battery staple",
		"role_ids": []string{readerRoleID},
	}, managerCookie)
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &created) != nil || created.User.ID == "" {
		t.Fatalf("普通管理员不能创建并分配自身权限子集: status=%d body=%s", response.Code, response.Body.String())
	}
	targetID := created.User.ID
	maskedTargetID := createUserForHTTPTest(t, router, adminCookie, "http-delegation-masked", "", []string{strongRoleID})
	response = performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+maskedTargetID+"/permissions", map[string]any{
		"allow": []string{}, "deny": []string{access.PermissionUserDelete},
	}, adminCookie)
	if response.Code != http.StatusNoContent {
		t.Fatalf("准备被 deny 遮蔽的高权限用户失败: status=%d body=%s", response.Code, response.Body.String())
	}
	response = performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+maskedTargetID+"/permissions", map[string]any{
		"allow": []string{}, "deny": []string{},
	}, managerCookie)
	if response.Code != http.StatusForbidden || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"access_delegation_denied"`)) {
		t.Fatalf("分步权限接口允许通过移除 deny 激活越权角色: status=%d body=%s", response.Code, response.Body.String())
	}

	for label, testCase := range map[string]struct {
		path string
		body map[string]any
	}{
		"分步角色": {path: "/api/v1/users/" + targetID + "/roles", body: map[string]any{"role_ids": []string{strongRoleID}}},
		"分步权限": {path: "/api/v1/users/" + targetID + "/permissions", body: map[string]any{"allow": []string{access.PermissionUserDelete}, "deny": []string{}}},
		"原子角色": {path: "/api/v1/users/" + targetID + "/access", body: map[string]any{"role_ids": []string{strongRoleID}, "allow": []string{}, "deny": []string{access.PermissionUserDelete}}},
		"原子允许": {path: "/api/v1/users/" + targetID + "/access", body: map[string]any{"role_ids": []string{readerRoleID}, "allow": []string{access.PermissionUserDelete}, "deny": []string{}}},
	} {
		response = performJSONRequest(t, router, http.MethodPut, testCase.path, testCase.body, managerCookie)
		if response.Code != http.StatusForbidden || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"access_delegation_denied"`)) {
			t.Fatalf("%s接口允许普通管理员越权委派: status=%d body=%s", label, response.Code, response.Body.String())
		}
	}

	for label, testCase := range map[string]struct {
		path string
		body map[string]any
	}{
		"自身角色":   {path: "/api/v1/users/" + managerID + "/roles", body: map[string]any{"role_ids": []string{readerRoleID}}},
		"自身权限":   {path: "/api/v1/users/" + managerID + "/permissions", body: map[string]any{"allow": []string{}, "deny": []string{}}},
		"自身原子配置": {path: "/api/v1/users/" + managerID + "/access", body: map[string]any{"role_ids": []string{managerRoleID}, "allow": []string{}, "deny": []string{}}},
	} {
		response = performJSONRequest(t, router, http.MethodPut, testCase.path, testCase.body, managerCookie)
		if response.Code != http.StatusForbidden || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"self_access_update_denied"`)) {
			t.Fatalf("%s接口允许普通管理员修改自身访问配置: status=%d body=%s", label, response.Code, response.Body.String())
		}
	}
}

func currentUserIDFromResponse(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.User.ID == "" {
		t.Fatalf("解析当前用户失败: payload=%+v err=%v", payload, err)
	}
	return payload.User.ID
}
