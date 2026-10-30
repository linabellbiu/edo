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

func TestProjectHTTPLastOwnerProtectionAndTransfer(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	creator := createRoleForHTTPTest(t, router, admin, "owner-create", []string{"project.create"})
	limited := createRoleForHTTPTest(t, router, admin, "owner-limited", []string{"project.read", "project.member.update"})
	firstID := createUserForHTTPTest(t, router, admin, "owner-first", database.DefaultDepartmentID, []string{creator})
	secondID := createUserForHTTPTest(t, router, admin, "owner-second", database.DefaultDepartmentID, nil)
	managerID := createUserForHTTPTest(t, router, admin, "owner-manager", database.DefaultDepartmentID, nil)
	first := loginForDepartmentTest(t, router, "owner-first")
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]string{"name": "管理员移交回归"}, first)
	var item struct{ Project struct{ ID string } }
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &item) != nil || item.Project.ID == "" {
		t.Fatalf("普通用户创建项目失败: status=%d body=%s", created.Code, created.Body.String())
	}
	assign := func(actor *http.Cookie, userID, roleID string, expected int) *httptest.ResponseRecorder {
		t.Helper()
		response := performJSONRequest(t, router, http.MethodPut, "/api/v1/projects/"+item.Project.ID+"/members/"+userID, map[string]string{"role_id": roleID}, actor)
		if response.Code != expected {
			t.Fatalf("分配项目成员结果不符: status=%d want=%d body=%s", response.Code, expected, response.Body.String())
		}
		return response
	}
	assign(first, firstID, "", http.StatusForbidden)
	assign(first, secondID, access.ProjectOwnerRoleID, http.StatusNoContent)
	second := loginForDepartmentTest(t, router, "owner-second")
	viewer := "bbf0404f-7165-5119-8620-194a3fab6c02"
	assign(second, firstID, viewer, http.StatusNoContent)
	assign(admin, managerID, limited, http.StatusNoContent)
	manager := loginForDepartmentTest(t, router, "owner-manager")
	assign(first, managerID, viewer, http.StatusForbidden)
	for _, roleID := range []string{"", limited} {
		response := assign(manager, secondID, roleID, http.StatusConflict)
		var rejected struct{ Code, Message string }
		if err := json.Unmarshal(response.Body.Bytes(), &rejected); err != nil || rejected.Code != "project_conflict" || rejected.Message != access.ErrProjectLastOwner.Error() {
			t.Fatalf("最后管理员未返回稳定中文冲突: response=%s err=%v", response.Body.String(), err)
		}
	}
	detail := performJSONRequest(t, router, http.MethodGet, "/api/v1/projects/"+item.Project.ID, nil, second)
	var members struct {
		Members []struct {
			UserID string `json:"user_id"`
			RoleID string `json:"role_id"`
		}
	}
	if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &members) != nil {
		t.Fatalf("不能读回被拒绝操作后的成员: status=%d body=%s", detail.Code, detail.Body.String())
	}
	roles := map[string]string{}
	for _, member := range members.Members {
		roles[member.UserID] = member.RoleID
	}
	if len(roles) != 3 || roles[firstID] != viewer || roles[secondID] != access.ProjectOwnerRoleID || roles[managerID] != limited {
		t.Fatalf("拒绝请求改变了成员: %+v", roles)
	}
	assign(admin, secondID, "", http.StatusNoContent)
	assign(admin, firstID, access.ProjectOwnerRoleID, http.StatusNoContent)
}
