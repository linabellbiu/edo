package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"edo/internal/access"
	"edo/internal/database"
)

func TestProjectHTTPMembershipIsolationAndRevocation(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	createProject := func(name string) string {
		t.Helper()
		r := performJSONRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]any{"name": name}, admin)
		if r.Code != 201 {
			t.Fatal(r.Code, r.Body.String())
		}
		var v struct{ Project struct{ ID string } }
		if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v.Project.ID
	}
	a, b := createProject("项目一"), createProject("项目二")
	userID := createUserForHTTPTest(t, router, admin, "space-reader", database.DefaultDepartmentID, nil)
	assign := func(project, role string) {
		t.Helper()
		r := performJSONRequest(t, router, http.MethodPut, "/api/v1/projects/"+project+"/members/"+userID, map[string]string{"role_id": role}, admin)
		if r.Code != 204 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	assign(a, "bbf0404f-7165-5119-8620-194a3fab6c02")
	assign(b, access.ProjectOwnerRoleID)
	member := loginForDepartmentTest(t, router, "space-reader")
	repo := func(project string) string {
		t.Helper()
		r := performJSONRequest(t, router, http.MethodPost, "/api/v1/repositories?project_id="+project, map[string]any{"name": "同名仓库", "provider": "generic", "clone_url": "https://example.com/team/project.git", "default_branch": "main", "auth_type": "none"}, admin)
		if r.Code != 201 {
			t.Fatal(r.Code, r.Body.String())
		}
		var v struct{ Repository struct{ ID string } }
		json.Unmarshal(r.Body.Bytes(), &v)
		return v.Repository.ID
	}
	repoA, repoB := repo(a), repo(b)
	assertListContainsOnlyDepartmentResource(t, router, member, "/api/v1/repositories?project_id="+a, "repositories", repoA, repoB)
	assertListContainsOnlyDepartmentResource(t, router, admin, "/api/v1/repositories?project_id="+b, "repositories", repoB, repoA)
	denied := performJSONRequest(t, router, http.MethodPost, "/api/v1/repositories?project_id="+a, map[string]string{"name": "禁止创建"}, member)
	if denied.Code != 403 {
		t.Fatal("只读角色出现创建入口", denied.Code, denied.Body.String())
	}
	foreign := performJSONRequest(t, router, http.MethodDelete, "/api/v1/repositories/"+repoA+"?project_id="+b, nil, member)
	if foreign.Code != 404 {
		t.Fatal("跨项目ID应按不存在处理", foreign.Code, foreign.Body.String())
	}
	// 即使成员拥有项目管理员角色，也不能通过项目头修改平台用户。
	platform := performJSONRequest(t, router, http.MethodGet, "/api/v1/users?project_id="+b, nil, member)
	if platform.Code != 403 {
		t.Fatal("项目角色越权访问平台用户", platform.Code)
	}
	assign(a, "")
	denied = performJSONRequest(t, router, http.MethodGet, "/api/v1/repositories?project_id="+a, nil, member)
	if denied.Code != 403 {
		t.Fatal("移除后旧会话仍可访问项目", denied.Code)
	}
	// 伪造 Header 不能改变成员资格。
	request := httptest.NewRequest(http.MethodGet, "/api/v1/repositories", nil)
	request.AddCookie(member)
	request.Header.Set("X-EDO-Project-ID", a)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("伪造项目头绕过授权")
	}
	projects := performJSONRequest(t, router, http.MethodGet, "/api/v1/projects", nil, member)
	if bytes.Contains(projects.Body.Bytes(), []byte(a)) || !bytes.Contains(projects.Body.Bytes(), []byte(b)) {
		t.Fatal("项目列表未及时撤权", projects.Body.String())
	}
	deleteBusy := performJSONRequest(t, router, http.MethodDelete, "/api/v1/projects/"+b, nil, admin)
	if deleteBusy.Code != 409 {
		t.Fatal("有资源项目不能删除", deleteBusy.Code, deleteBusy.Body.String())
	}
}
