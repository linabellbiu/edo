package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"edo/internal/access"
)

func TestRoleIndependentUpdateEndpointsAndLegacyCompatibility(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	adminCookie := loginForDepartmentTest(t, router, "admin")
	roleID := createRoleForHTTPTest(t, router, adminCookie, "independent-http", []string{access.PermissionRoleRead})

	basic := performJSONRequest(t, router, http.MethodPatch, "/api/v1/roles/"+roleID+"/basic", map[string]any{
		"name": "independent-http-renamed", "display_name": "独立基本信息", "description": "基本信息抽屉",
	}, adminCookie)
	if basic.Code != http.StatusOK {
		t.Fatalf("独立更新角色基本信息失败: status=%d body=%s", basic.Code, basic.Body.String())
	}
	assertRoleHTTPPayload(t, basic.Body.Bytes(), "independent-http-renamed", "独立基本信息", []string{access.PermissionRoleRead})

	permissions := performJSONRequest(t, router, http.MethodPut, "/api/v1/roles/"+roleID+"/permissions", map[string]any{
		"permissions": []string{access.PermissionUserRead},
	}, adminCookie)
	if permissions.Code != http.StatusOK {
		t.Fatalf("独立更新角色权限失败: status=%d body=%s", permissions.Code, permissions.Body.String())
	}
	assertRoleHTTPPayload(t, permissions.Body.Bytes(), "independent-http-renamed", "独立基本信息", []string{access.PermissionUserRead})
	missingPermissions := performJSONRequest(t, router, http.MethodPut, "/api/v1/roles/"+roleID+"/permissions", map[string]any{}, adminCookie)
	if missingPermissions.Code != http.StatusBadRequest {
		t.Fatalf("缺少 permissions 的完整替换请求未被拒绝: status=%d body=%s", missingPermissions.Code, missingPermissions.Body.String())
	}
	rows := listRoleRowsForTest(t, router, adminCookie)
	if values, ok := rows[roleID]["permissions"].([]any); !ok || len(values) != 1 || values[0] != access.PermissionUserRead {
		t.Fatalf("缺少 permissions 的请求意外清空了角色权限: %v", rows[roleID]["permissions"])
	}
	clearPermissions := performJSONRequest(t, router, http.MethodPut, "/api/v1/roles/"+roleID+"/permissions", map[string]any{
		"permissions": []string{},
	}, adminCookie)
	if clearPermissions.Code != http.StatusOK {
		t.Fatalf("显式空数组不能清空角色权限: status=%d body=%s", clearPermissions.Code, clearPermissions.Body.String())
	}
	assertRoleHTTPPayload(t, clearPermissions.Body.Bytes(), "independent-http-renamed", "独立基本信息", []string{})

	legacy := performJSONRequest(t, router, http.MethodPut, "/api/v1/roles/"+roleID, map[string]any{
		"name": "independent-http-legacy", "display_name": "兼容整包更新", "description": "旧接口",
		"permissions": []string{access.PermissionAuditRead},
	}, adminCookie)
	if legacy.Code != http.StatusOK {
		t.Fatalf("兼容整包角色更新失败: status=%d body=%s", legacy.Code, legacy.Body.String())
	}
	assertRoleHTTPPayload(t, legacy.Body.Bytes(), "independent-http-legacy", "兼容整包更新", []string{access.PermissionAuditRead})
}

func TestRolePermissionDelegationBoundary(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	adminCookie := loginForDepartmentTest(t, router, "admin")
	targetRoleID := createRoleForHTTPTest(t, router, adminCookie, "delegation-target", nil)
	managerRoleID := createRoleForHTTPTest(t, router, adminCookie, "delegation-manager", []string{
		access.PermissionRoleRead, access.PermissionRoleCreate, access.PermissionRoleUpdate,
	})
	createUserForHTTPTest(t, router, adminCookie, "delegator", "00000000-0000-0000-0000-000000000001", []string{managerRoleID})
	managerCookie := loginForDepartmentTest(t, router, "delegator")

	for label, request := range map[string]struct {
		method string
		path   string
		body   map[string]any
	}{
		"创建角色": {
			method: http.MethodPost, path: "/api/v1/roles",
			body: map[string]any{"name": "delegation-escalated", "display_name": "越权角色", "permissions": []string{access.PermissionUserDelete}},
		},
		"兼容整包更新": {
			method: http.MethodPut, path: "/api/v1/roles/" + targetRoleID,
			body: map[string]any{"name": "delegation-target", "display_name": "越权整包更新", "permissions": []string{access.PermissionUserDelete}},
		},
		"独立权限更新": {
			method: http.MethodPut, path: "/api/v1/roles/" + targetRoleID + "/permissions",
			body: map[string]any{"permissions": []string{access.PermissionUserDelete}},
		},
	} {
		response := performJSONRequest(t, router, request.method, request.path, request.body, managerCookie)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s 未拒绝超出当前账户范围的权限: status=%d body=%s", label, response.Code, response.Body.String())
		}
		var payload struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Code != "permission_delegation_denied" {
			t.Fatalf("%s 返回的越权错误不稳定: payload=%+v err=%v", label, payload, err)
		}
	}

	rows := listRoleRowsForTest(t, router, managerCookie)
	if permissions, ok := rows[targetRoleID]["permissions"].([]any); !ok || len(permissions) != 0 {
		t.Fatalf("被拒绝的角色权限更新仍修改了数据: %v", rows[targetRoleID]["permissions"])
	}
	allowed := performJSONRequest(t, router, http.MethodPut, "/api/v1/roles/"+targetRoleID+"/permissions", map[string]any{
		"permissions": []string{access.PermissionRoleRead},
	}, managerCookie)
	if allowed.Code != http.StatusOK {
		t.Fatalf("当前账户拥有的权限不能正常委派: status=%d body=%s", allowed.Code, allowed.Body.String())
	}
}

func TestRoleListMemberVisibilityAndGlobalUsageFlag(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	adminCookie := loginForDepartmentTest(t, router, "admin")
	departmentA := createDepartmentForHTTPTest(t, router, adminCookie, "角色计数甲部门")
	departmentB := createDepartmentForHTTPTest(t, router, adminCookie, "角色计数乙部门")

	targetRoleID := createRoleForHTTPTest(t, router, adminCookie, "visibility-target", nil)
	otherDepartmentRoleID := createRoleForHTTPTest(t, router, adminCookie, "visibility-other-only", nil)
	roleReaderID := createRoleForHTTPTest(t, router, adminCookie, "visibility-role-reader", []string{access.PermissionRoleRead})
	userReaderID := createRoleForHTTPTest(t, router, adminCookie, "visibility-user-reader", []string{
		access.PermissionRoleRead, access.PermissionUserRead,
	})
	createUserForHTTPTest(t, router, adminCookie, "rolereader", departmentA, []string{roleReaderID})
	createUserForHTTPTest(t, router, adminCookie, "userreader", departmentA, []string{userReaderID})
	createUserForHTTPTest(t, router, adminCookie, "targetmembera", departmentA, []string{targetRoleID})
	createUserForHTTPTest(t, router, adminCookie, "targetmemberb", departmentB, []string{targetRoleID, otherDepartmentRoleID})

	roleOnlyRows := listRoleRowsForTest(t, router, loginForDepartmentTest(t, router, "rolereader"))
	if _, exists := roleOnlyRows[targetRoleID]["visible_member_count"]; exists {
		t.Fatalf("只有 role.read 的账户获知了成员数量: %v", roleOnlyRows[targetRoleID])
	}
	if _, exists := roleOnlyRows[targetRoleID]["member_count"]; exists {
		t.Fatalf("角色列表仍通过旧字段泄露成员数量: %v", roleOnlyRows[targetRoleID])
	}
	if inUse, ok := roleOnlyRows[targetRoleID]["in_use"].(bool); !ok || !inUse {
		t.Fatalf("角色全局使用状态缺失: %v", roleOnlyRows[targetRoleID])
	}

	userReaderRows := listRoleRowsForTest(t, router, loginForDepartmentTest(t, router, "userreader"))
	if count, ok := userReaderRows[targetRoleID]["visible_member_count"].(float64); !ok || count != 1 {
		t.Fatalf("当前部门可见成员计数错误: %v", userReaderRows[targetRoleID])
	}
	if count, ok := userReaderRows[otherDepartmentRoleID]["visible_member_count"].(float64); !ok || count != 0 {
		t.Fatalf("跨部门成员数量未被隔离: %v", userReaderRows[otherDepartmentRoleID])
	}
	if inUse, ok := userReaderRows[otherDepartmentRoleID]["in_use"].(bool); !ok || !inUse {
		t.Fatalf("仅跨部门使用的角色未返回全局使用状态: %v", userReaderRows[otherDepartmentRoleID])
	}
}

func assertRoleHTTPPayload(t *testing.T, body []byte, wantName, wantDisplayName string, wantPermissions []string) {
	t.Helper()
	var payload struct {
		Role struct {
			Name        string   `json:"name"`
			DisplayName string   `json:"display_name"`
			Permissions []string `json:"permissions"`
		} `json:"role"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("解析角色响应失败: %v", err)
	}
	if payload.Role.Name != wantName || payload.Role.DisplayName != wantDisplayName ||
		len(payload.Role.Permissions) != len(wantPermissions) {
		t.Fatalf("角色响应错误: got=%+v want_name=%s want_display_name=%s want_permissions=%v", payload.Role, wantName, wantDisplayName, wantPermissions)
	}
	for i := range wantPermissions {
		if payload.Role.Permissions[i] != wantPermissions[i] {
			t.Fatalf("角色权限响应错误: got=%v want=%v", payload.Role.Permissions, wantPermissions)
		}
	}
}

func listRoleRowsForTest(t *testing.T, router http.Handler, cookie *http.Cookie) map[string]map[string]any {
	t.Helper()
	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/roles", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("查询角色列表失败: status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Roles []map[string]any `json:"roles"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析角色列表失败: %v", err)
	}
	rows := make(map[string]map[string]any, len(payload.Roles))
	for _, role := range payload.Roles {
		id, _ := role["id"].(string)
		rows[id] = role
	}
	return rows
}
