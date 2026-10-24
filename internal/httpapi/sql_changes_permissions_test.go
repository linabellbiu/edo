package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorm.io/gorm"

	"edo/internal/database"
)

type sqlChangeHTTPPermissionRoute struct {
	name       string
	method     string
	path       string
	permission string
}

func sqlChangeHTTPPermissionRoutes() []sqlChangeHTTPPermissionRoute {
	const resource = "/api/v1/sql-changes/514a3880-b6a2-4233-9006-7f38638fd42b"
	return []sqlChangeHTTPPermissionRoute{
		{"list", http.MethodGet, "/api/v1/sql-changes", "read"},
		{"options", http.MethodGet, "/api/v1/sql-changes/options", "read"},
		{"detail", http.MethodGet, resource, "read"},
		{"create", http.MethodPost, "/api/v1/sql-changes", "create"},
		{"update", http.MethodPut, resource, "update"},
		{"delete", http.MethodDelete, resource + "?version=1", "delete"},
		{"review", http.MethodPost, resource + "/reviews", "review"},
		{"execute", http.MethodPost, resource + "/execute", "execute"},
		{"skip", http.MethodPost, resource + "/skip", "skip"},
		{"check", http.MethodPost, resource + "/check", "check"},
	}
}

func sqlChangeHTTPActionPermissions() []string {
	return []string{"read", "create", "update", "delete", "review", "execute", "skip", "check"}
}

func sqlChangeHTTPAllPermissions() []string {
	permissions := []string{"application_database.execute"}
	for _, action := range sqlChangeHTTPActionPermissions() {
		permissions = append(permissions, "sql_change."+action)
	}
	return permissions
}

type sqlChangePermissionFixture struct {
	router    http.Handler
	admin     *http.Cookie
	projectID string
}

func newSQLChangePermissionFixture(t *testing.T) *sqlChangePermissionFixture {
	t.Helper()
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, _ *gorm.DB) {
		// 503 表明请求已穿过真实登录、项目范围和动作权限，而没有执行 SQL。
		deps.SQLChanges = nil
	})
	t.Cleanup(cleanup)
	return &sqlChangePermissionFixture{
		router: router, admin: loginForDepartmentTest(t, router, "admin"),
		projectID: database.DefaultProjectID(database.DefaultDepartmentID),
	}
}

func (f *sqlChangePermissionFixture) setMember(t *testing.T, projectID, userID, roleID string) {
	t.Helper()
	response := performJSONRequest(t, f.router, http.MethodPut,
		"/api/v1/projects/"+projectID+"/members/"+userID, map[string]string{"role_id": roleID}, f.admin)
	if response.Code != http.StatusNoContent {
		t.Fatalf("设置 SQL 权限测试成员失败: status=%d body=%s", response.Code, response.Body.String())
	}
}

func (f *sqlChangePermissionFixture) member(t *testing.T, name string, permissions []string) (string, *http.Cookie) {
	t.Helper()
	codes := append([]string{"project.read"}, permissions...)
	roleID := createRoleForHTTPTest(t, f.router, f.admin, "sql-project-"+name, codes)
	// 用户不绑定平台角色，项目权限只来自显式成员关系。
	userID := createUserForHTTPTest(t, f.router, f.admin, "sql-"+name, "", nil)
	f.setMember(t, f.projectID, userID, roleID)
	return userID, loginForDepartmentTest(t, f.router, "sql-"+name)
}

func (f *sqlChangePermissionFixture) project(t *testing.T, name string) string {
	t.Helper()
	response := performJSONRequest(t, f.router, http.MethodPost, "/api/v1/projects", map[string]string{"name": name}, f.admin)
	var payload struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Project.ID == "" {
		t.Fatalf("创建 SQL 权限测试项目失败: status=%d body=%s", response.Code, response.Body.String())
	}
	return payload.Project.ID
}

func sqlChangePermissionProjectPath(path, projectID string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "project_id=" + projectID
}

func assertSQLChangePermissionResponse(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var failure errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || response.Code != status || failure.Code != code {
		t.Fatalf("SQL API 权限边界不符: status=%d want=%d code=%s want_code=%s body=%s err=%v",
			response.Code, status, failure.Code, code, response.Body.String(), err)
	}
	if status == http.StatusServiceUnavailable && response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("SQL API 不应缓存工单响应")
	}
}

func (f *sqlChangePermissionFixture) request(t *testing.T, route sqlChangeHTTPPermissionRoute, cookie *http.Cookie, status int, code string) {
	t.Helper()
	response := performJSONRequest(t, f.router, route.method, route.path, map[string]any{}, cookie)
	assertSQLChangePermissionResponse(t, response, status, code)
}

func TestSQLChangeHTTPActionPermissions(t *testing.T) {
	f := newSQLChangePermissionFixture(t)
	_, unprivileged := f.member(t, "no-action", nil)
	for _, route := range sqlChangeHTTPPermissionRoutes() {
		t.Run("anonymous/"+route.name, func(t *testing.T) {
			f.request(t, route, nil, http.StatusUnauthorized, "authentication_required")
		})
		t.Run("unprivileged/"+route.name, func(t *testing.T) {
			f.request(t, route, unprivileged, http.StatusForbidden, "permission_denied")
		})
	}
	for _, action := range sqlChangeHTTPActionPermissions() {
		permissions := []string{"sql_change." + action}
		if action != "read" {
			permissions = append(permissions, "sql_change.read")
		}
		if action == "execute" {
			permissions = append(permissions, "application_database.execute")
		}
		_, cookie := f.member(t, "action-"+action, permissions)
		for _, route := range sqlChangeHTTPPermissionRoutes() {
			t.Run(action+"/"+route.name, func(t *testing.T) {
				status, code := http.StatusForbidden, "permission_denied"
				if route.permission == action || route.permission == "read" {
					status, code = http.StatusServiceUnavailable, "sql_change_unavailable"
				}
				f.request(t, route, cookie, status, code)
			})
		}
	}
	for _, action := range sqlChangeHTTPActionPermissions() {
		if action == "read" {
			continue
		}
		permissions := []string{"sql_change." + action}
		if action == "execute" {
			permissions = append(permissions, "application_database.execute")
		}
		_, cookie := f.member(t, "no-read-"+action, permissions)
		for _, route := range sqlChangeHTTPPermissionRoutes() {
			t.Run("action-without-read/"+action+"/"+route.name, func(t *testing.T) {
				f.request(t, route, cookie, http.StatusForbidden, "permission_denied")
			})
		}
	}
}

func TestSQLChangeHTTPPlatformPermissionsCannotReplaceProjectPermissions(t *testing.T) {
	f := newSQLChangePermissionFixture(t)
	platformRole := createRoleForHTTPTest(t, f.router, f.admin, "sql-platform-only", sqlChangeHTTPAllPermissions())
	userID := createUserForHTTPTest(t, f.router, f.admin, "sql-platform-only", "", []string{platformRole})
	basicRole := createRoleForHTTPTest(t, f.router, f.admin, "sql-project-basic", []string{"project.read"})
	// 通用测试工具会复制平台业务权限到项目角色，必须覆盖后才是真正的平台角色隔离测试。
	f.setMember(t, f.projectID, userID, basicRole)
	cookie := loginForDepartmentTest(t, f.router, "sql-platform-only")
	for _, route := range sqlChangeHTTPPermissionRoutes() {
		t.Run(route.name, func(t *testing.T) {
			f.request(t, route, cookie, http.StatusForbidden, "permission_denied")
		})
	}
}

func TestSQLChangeHTTPProjectIsolationAndDeny(t *testing.T) {
	f := newSQLChangePermissionFixture(t)
	userID, cookie := f.member(t, "scope-deny", sqlChangeHTTPAllPermissions())
	otherProject := f.project(t, "SQL 权限隔离项目")
	basicRole := createRoleForHTTPTest(t, f.router, f.admin, "sql-scope-basic", []string{"project.read"})
	for _, route := range sqlChangeHTTPPermissionRoutes() {
		t.Run("missing-project/"+route.name, func(t *testing.T) {
			request := httptest.NewRequest(route.method, route.path, strings.NewReader("{}"))
			request.Header.Set("Content-Type", "application/json")
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			f.router.ServeHTTP(response, request)
			assertSQLChangePermissionResponse(t, response, http.StatusBadRequest, "project_required")
		})
		t.Run("own-project/"+route.name, func(t *testing.T) {
			own := route
			own.path = sqlChangePermissionProjectPath(route.path, f.projectID)
			f.request(t, own, cookie, http.StatusServiceUnavailable, "sql_change_unavailable")
		})
		t.Run("not-a-member/"+route.name, func(t *testing.T) {
			other := route
			other.path = sqlChangePermissionProjectPath(route.path, otherProject)
			f.request(t, other, cookie, http.StatusForbidden, "project_denied")
		})
	}
	f.setMember(t, otherProject, userID, basicRole)
	for _, route := range sqlChangeHTTPPermissionRoutes() {
		t.Run("other-project-no-action/"+route.name, func(t *testing.T) {
			other := route
			other.path = sqlChangePermissionProjectPath(route.path, otherProject)
			f.request(t, other, cookie, http.StatusForbidden, "permission_denied")
		})
		t.Run("personal-deny/"+route.name, func(t *testing.T) {
			response := performJSONRequest(t, f.router, http.MethodPut, "/api/v1/users/"+userID+"/permissions",
				map[string]any{"allow": []string{}, "deny": []string{"sql_change." + route.permission}}, f.admin)
			if response.Code != http.StatusNoContent {
				t.Fatalf("配置个人拒绝失败: status=%d body=%s", response.Code, response.Body.String())
			}
			f.request(t, route, cookie, http.StatusForbidden, "permission_denied")
			response = performJSONRequest(t, f.router, http.MethodPut, "/api/v1/users/"+userID+"/permissions",
				map[string]any{"allow": []string{}, "deny": []string{}}, f.admin)
			if response.Code != http.StatusNoContent {
				t.Fatalf("清除个人拒绝失败: status=%d body=%s", response.Code, response.Body.String())
			}
			f.request(t, route, cookie, http.StatusServiceUnavailable, "sql_change_unavailable")
		})
	}
	response := performJSONRequest(t, f.router, http.MethodPut, "/api/v1/users/"+userID+"/permissions",
		map[string]any{"allow": []string{}, "deny": []string{"sql_change.read"}}, f.admin)
	if response.Code != http.StatusNoContent {
		t.Fatalf("配置个人查看拒绝失败: status=%d body=%s", response.Code, response.Body.String())
	}
	for _, route := range sqlChangeHTTPPermissionRoutes() {
		t.Run("personal-read-deny/"+route.name, func(t *testing.T) {
			f.request(t, route, cookie, http.StatusForbidden, "permission_denied")
		})
	}
	response = performJSONRequest(t, f.router, http.MethodPut, "/api/v1/users/"+userID+"/permissions",
		map[string]any{"allow": []string{}, "deny": []string{}}, f.admin)
	if response.Code != http.StatusNoContent {
		t.Fatalf("清除个人查看拒绝失败: status=%d body=%s", response.Code, response.Body.String())
	}
	f.setMember(t, f.projectID, userID, "")
	for _, route := range sqlChangeHTTPPermissionRoutes() {
		t.Run("revoked-old-session/"+route.name, func(t *testing.T) {
			f.request(t, route, cookie, http.StatusForbidden, "project_denied")
		})
	}
}

func TestSQLChangeHTTPExecuteRequiresDatabasePermission(t *testing.T) {
	f := newSQLChangePermissionFixture(t)
	var route sqlChangeHTTPPermissionRoute
	for _, candidate := range sqlChangeHTTPPermissionRoutes() {
		if candidate.name == "execute" {
			route = candidate
			break
		}
	}
	if route.path == "" {
		t.Fatal("SQL 执行权限矩阵缺少执行路由")
	}
	for _, item := range []struct {
		name        string
		permissions []string
		status      int
		code        string
	}{
		{"sql-only", []string{"sql_change.execute"}, http.StatusForbidden, "permission_denied"},
		{"database-only", []string{"application_database.execute"}, http.StatusForbidden, "permission_denied"},
		{"both", []string{"sql_change.execute", "application_database.execute"}, http.StatusServiceUnavailable, "sql_change_unavailable"},
	} {
		t.Run(item.name, func(t *testing.T) {
			permissions := append([]string{"sql_change.read"}, item.permissions...)
			_, cookie := f.member(t, "execute-"+item.name, permissions)
			f.request(t, route, cookie, item.status, item.code)
		})
	}
	t.Run("platform-database-permission-is-insufficient", func(t *testing.T) {
		userID, cookie := f.member(t, "execute-platform-db", []string{"sql_change.read", "sql_change.execute"})
		platformRole := createRoleForHTTPTest(t, f.router, f.admin, "sql-platform-database", []string{"application_database.execute"})
		response := performJSONRequest(t, f.router, http.MethodPut, "/api/v1/users/"+userID+"/roles",
			map[string]any{"role_ids": []string{platformRole}}, f.admin)
		if response.Code != http.StatusNoContent {
			t.Fatalf("设置平台数据库角色失败: status=%d body=%s", response.Code, response.Body.String())
		}
		f.request(t, route, cookie, http.StatusForbidden, "permission_denied")
	})
	t.Run("personal-database-deny-overrides-project-role", func(t *testing.T) {
		userID, cookie := f.member(t, "execute-deny-db", []string{"sql_change.read", "sql_change.execute", "application_database.execute"})
		f.request(t, route, cookie, http.StatusServiceUnavailable, "sql_change_unavailable")
		response := performJSONRequest(t, f.router, http.MethodPut, "/api/v1/users/"+userID+"/permissions",
			map[string]any{"allow": []string{}, "deny": []string{"application_database.execute"}}, f.admin)
		if response.Code != http.StatusNoContent {
			t.Fatalf("设置个人数据库拒绝失败: status=%d body=%s", response.Code, response.Body.String())
		}
		f.request(t, route, cookie, http.StatusForbidden, "permission_denied")
	})
}
