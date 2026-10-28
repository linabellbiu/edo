package httpapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"edo/internal/access"
	"edo/internal/database"
)

func rbacCapacityCatalog(t *testing.T, router http.Handler, admin *http.Cookie) (all, project []string) {
	t.Helper()
	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/permissions", nil, admin)
	var payload struct {
		Permissions []access.Permission `json:"permissions"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil {
		t.Fatalf("读取完整权限目录失败: status=%d", response.Code)
	}
	for _, permission := range payload.Permissions {
		all = append(all, permission.Code)
		if permission.Scope == "project" {
			project = append(project, permission.Code)
		}
	}
	slices.Sort(all)
	slices.Sort(project)
	if len(project) <= 100 || len(all) > 512 {
		t.Fatalf("权限目录未覆盖历史容量边界或已超过当前上限: project=%d all=%d", len(project), len(all))
	}
	return all, project
}

func TestRBACPermissionCapacityFullProjectRoleHTTP(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	_, project := rbacCapacityCatalog(t, router, admin)
	for _, operation := range []string{"创建", "整包更新", "权限更新"} {
		t.Run(operation, func(t *testing.T) {
			name, method, path, status := "capacity-project-create", http.MethodPost, "/api/v1/roles", http.StatusCreated
			body := map[string]any{"name": name, "display_name": name, "permissions": project}
			if operation != "创建" {
				name = "capacity-project-update"
				if operation == "权限更新" {
					name = "capacity-project-permissions"
				}
				roleID := createRoleForHTTPTest(t, router, admin, name, []string{"project.read"})
				method, path, status = http.MethodPut, "/api/v1/roles/"+roleID, http.StatusOK
				body["name"], body["display_name"] = name, name
				if operation == "权限更新" {
					path += "/permissions"
					body = map[string]any{"permissions": project}
				}
			}
			response := performJSONRequest(t, router, method, path, body, admin)
			if response.Code != status {
				t.Fatalf("完整项目权限角色%s失败: permissions=%d status=%d body=%s", operation, len(project), response.Code, response.Body.String())
			}
			assertRoleHTTPPayload(t, response.Body.Bytes(), name, name, project)
		})
	}
}

func TestRBACPermissionCapacityRejectsInvalidRoleWritesHTTP(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	_, project := rbacCapacityCatalog(t, router, admin)
	roleID := createRoleForHTTPTest(t, router, admin, "capacity-unchanged", []string{"project.read"})
	before := listRoleRowsForTest(t, router, admin)
	for _, invalid := range []struct {
		name        string
		permissions []string
		code        string
	}{
		{"未知权限", append(slices.Clone(project), "unknown.capacity.execute"), "invalid_permission"},
		{"超过容量", slices.Repeat([]string{"project.read"}, 513), "invalid_request"},
	} {
		for _, operation := range []string{"创建", "整包更新", "权限更新"} {
			t.Run(invalid.name+operation, func(t *testing.T) {
				method, path := http.MethodPost, "/api/v1/roles"
				body := map[string]any{"name": "capacity-invalid-new", "display_name": "不应保存", "permissions": invalid.permissions}
				if operation != "创建" {
					method, path = http.MethodPut, "/api/v1/roles/"+roleID
					body["name"] = "capacity-invalid-renamed"
					if operation == "权限更新" {
						path += "/permissions"
						body = map[string]any{"permissions": invalid.permissions}
					}
				}
				response := performJSONRequest(t, router, method, path, body, admin)
				var payload struct{ Code string }
				if response.Code != http.StatusBadRequest || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Code != invalid.code {
					t.Fatalf("非法权限集合未按约定拒绝: status=%d body=%s", response.Code, response.Body.String())
				}
				if after := listRoleRowsForTest(t, router, admin); !reflect.DeepEqual(before, after) {
					t.Fatal("被拒绝的角色请求改变了已有角色或新增了角色")
				}
			})
		}
	}
	boundary := performJSONRequest(t, router, http.MethodPut, "/api/v1/roles/"+roleID+"/permissions", map[string]any{
		"permissions": slices.Repeat([]string{"project.read"}, 512),
	}, admin)
	if boundary.Code != http.StatusOK {
		t.Fatalf("权限数组固定上限内的有效权限被拒绝: status=%d body=%s", boundary.Code, boundary.Body.String())
	}
	assertRoleHTTPPayload(t, boundary.Body.Bytes(), "capacity-unchanged", "capacity-unchanged", []string{"project.read"})
}

func rbacCapacityUserRow(t *testing.T, router http.Handler, admin *http.Cookie, userID string) map[string]any {
	t.Helper()
	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/users", nil, admin)
	var payload struct {
		Users []map[string]any `json:"users"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil {
		t.Fatalf("读取用户访问配置失败: status=%d", response.Code)
	}
	for _, user := range payload.Users {
		if user["id"] == userID {
			return user
		}
	}
	t.Fatal("测试用户未出现在用户列表中")
	return nil
}

func TestRBACPermissionCapacityUserOverridesHTTP(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	all, _ := rbacCapacityCatalog(t, router, admin)
	userID := createUserForHTTPTest(t, router, admin, "capacity-overrides", "", nil)
	for _, endpoint := range []string{"permissions", "access"} {
		for _, field := range []string{"allow", "deny"} {
			t.Run(endpoint+"/"+field, func(t *testing.T) {
				body := map[string]any{"allow": []string{}, "deny": []string{}}
				if endpoint == "access" {
					body["role_ids"] = []string{}
				}
				body[field] = all
				response := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userID+"/"+endpoint, body, admin)
				if response.Code != http.StatusNoContent {
					t.Fatalf("完整个人权限集合未通过: permissions=%d status=%d body=%s", len(all), response.Code, response.Body.String())
				}
				row := rbacCapacityUserRow(t, router, admin, userID)
				encoded, _ := json.Marshal(row["permission_overrides"])
				var stored access.UserPermissionOverrides
				if json.Unmarshal(encoded, &stored) != nil || field == "allow" && !slices.Equal(stored.Allow, all) || field == "deny" && !slices.Equal(stored.Deny, all) {
					t.Fatal("完整个人权限集合没有完整保存")
				}
				for _, invalid := range []struct {
					permissions []string
					code        string
				}{
					{append(slices.Clone(all), "unknown.capacity.execute"), "invalid_user_permissions"},
					{slices.Repeat([]string{"project.read"}, 513), "invalid_request"},
				} {
					body[field] = invalid.permissions
					response = performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userID+"/"+endpoint, body, admin)
					var payload struct{ Code string }
					if response.Code != http.StatusBadRequest || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Code != invalid.code {
						t.Fatalf("非法个人权限集合未按约定拒绝: status=%d body=%s", response.Code, response.Body.String())
					}
					if after := rbacCapacityUserRow(t, router, admin, userID); !reflect.DeepEqual(row, after) {
						t.Fatal("被拒绝的个人权限请求改变了用户访问配置")
					}
				}
			})
		}
	}
}

func TestRBACPermissionCapacityKeepsRoleCountAndProjectIsolationHTTP(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	_, project := rbacCapacityCatalog(t, router, admin)
	roleID := createRoleForHTTPTest(t, router, admin, "capacity-platform-template", project)
	userID := createUserForHTTPTest(t, router, admin, "capacity-platform-member", "", nil)
	roles := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userID+"/roles", map[string]any{
		"role_ids": []string{roleID},
	}, admin)
	if roles.Code != http.StatusNoContent {
		t.Fatalf("分配完整项目权限的平台角色模板失败: status=%d body=%s", roles.Code, roles.Body.String())
	}
	member := loginForDepartmentTest(t, router, "capacity-platform-member")
	denied := performJSONRequest(t, router, http.MethodGet, "/api/v1/applications?project_id="+database.DefaultProjectID(database.DefaultDepartmentID), nil, member)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("平台角色意外赋予了项目成员资格或业务权限: status=%d", denied.Code)
	}
	before := rbacCapacityUserRow(t, router, admin, userID)
	for _, endpoint := range []string{"roles", "access"} {
		body := map[string]any{"role_ids": slices.Repeat([]string{roleID}, 101)}
		if endpoint == "access" {
			body["allow"], body["deny"] = []string{}, []string{}
		}
		response := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userID+"/"+endpoint, body, admin)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("角色个数上限被意外放宽: endpoint=%s status=%d", endpoint, response.Code)
		}
		if after := rbacCapacityUserRow(t, router, admin, userID); !reflect.DeepEqual(before, after) {
			t.Fatal("超过角色个数上限的请求改变了用户访问配置")
		}
	}
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "capacity-over-limit", "nickname": "不应创建", "password": "correct horse battery staple",
		"role_ids": slices.Repeat([]string{roleID}, 101),
	}, admin)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("创建用户的角色个数上限被意外放宽: status=%d", response.Code)
	}
}

func TestRBACPermissionCapacityDoesNotExpandPlatformDelegationHTTP(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	_, project := rbacCapacityCatalog(t, router, admin)
	roleID := createRoleForHTTPTest(t, router, admin, "capacity-delegation-target", []string{"project.read"})
	managerRole := createRoleForHTTPTest(t, router, admin, "capacity-delegation-manager", append(slices.Clone(project),
		access.PermissionRoleRead, access.PermissionRoleCreate, access.PermissionRoleUpdate))
	managerID := createUserForHTTPTest(t, router, admin, "capacity-delegator", "", nil)
	assigned := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+managerID+"/roles", map[string]any{
		"role_ids": []string{managerRole},
	}, admin)
	if assigned.Code != http.StatusNoContent {
		t.Fatalf("准备仅有平台角色的管理员失败: status=%d", assigned.Code)
	}
	manager := loginForDepartmentTest(t, router, "capacity-delegator")
	before := listRoleRowsForTest(t, router, admin)
	for _, operation := range []string{"创建", "整包更新", "权限更新"} {
		method, path := http.MethodPost, "/api/v1/roles"
		body := map[string]any{"name": "capacity-escalated", "display_name": "不应保存", "permissions": project}
		if operation != "创建" {
			method, path = http.MethodPut, "/api/v1/roles/"+roleID
			if operation == "权限更新" {
				path += "/permissions"
				body = map[string]any{"permissions": project}
			}
		}
		response := performJSONRequest(t, router, method, path, body, manager)
		var payload struct{ Code string }
		if response.Code != http.StatusForbidden || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Code != "permission_delegation_denied" {
			t.Fatalf("%s允许平台角色中的项目权限越权委派: status=%d body=%s", operation, response.Code, response.Body.String())
		}
		if after := listRoleRowsForTest(t, router, admin); !reflect.DeepEqual(before, after) {
			t.Fatal("被拒绝的完整项目权限委派改变了角色配置")
		}
	}
}
