package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestContainerActionRoutesRequireIndependentPermissions(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	admin := performJSONRequest(t, router, "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "correct horse battery staple"}, nil)
	if admin.Code != http.StatusOK {
		t.Fatal(admin.Code, admin.Body.String())
	}
	cookie := admin.Result().Cookies()[0]
	role := performJSONRequest(t, router, "POST", "/api/v1/roles", map[string]any{"name": "container-operator", "display_name": "单容器操作员", "permissions": []string{"deployment.read", "deployment.execute", "cluster.execute"}}, cookie)
	var rolePayload struct {
		Role struct {
			ID string `json:"id"`
		} `json:"role"`
	}
	if role.Code != http.StatusCreated || json.Unmarshal(role.Body.Bytes(), &rolePayload) != nil {
		t.Fatal(role.Code, role.Body.String())
	}
	created := performJSONRequest(t, router, "POST", "/api/v1/users", map[string]any{"username": "container-operator", "nickname": "容器操作员", "password": "test-password", "role_ids": []string{rolePayload.Role.ID}}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatal(created.Code, created.Body.String())
	}
	var userPayload struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &userPayload); err != nil {
		t.Fatal(err)
	}
	joinCreatedUserProjectForTest(t, router, cookie, created, []string{rolePayload.Role.ID})
	login := performJSONRequest(t, router, "POST", "/api/v1/auth/login", map[string]string{"username": "container-operator", "password": "test-password"}, nil)
	if login.Code != http.StatusOK {
		t.Fatal(login.Code, login.Body.String())
	}
	operator := login.Result().Cookies()[0]
	path := "/api/v1/deployments/not-found/runtime/containers/" + strings.Repeat("a", 64) + "/"
	check := func(action string, status int) {
		t.Helper()
		response := performJSONRequest(t, router, "POST", path+action, map[string]string{"digest": strings.Repeat("f", 64)}, operator)
		if response.Code != status {
			t.Fatalf("动作权限不符: action=%s status=%d want=%d body=%s", action, response.Code, status, response.Body.String())
		}
	}
	for _, action := range []string{"restart", "stop", "recreate"} {
		check(action, http.StatusForbidden)
	}
	grant := performJSONRequest(t, router, "PUT", "/api/v1/users/"+userPayload.User.ID+"/permissions", map[string]any{"allow": []string{"deployment.container.stop"}, "deny": []string{}}, cookie)
	if grant.Code != http.StatusNoContent {
		t.Fatal(grant.Code, grant.Body.String())
	}
	check("stop", http.StatusNotFound)
	check("restart", http.StatusForbidden)
	check("recreate", http.StatusForbidden)
	deny := performJSONRequest(t, router, "PUT", "/api/v1/users/"+userPayload.User.ID+"/permissions", map[string]any{"allow": []string{}, "deny": []string{"deployment.container.stop"}}, cookie)
	if deny.Code != http.StatusNoContent {
		t.Fatal(deny.Code, deny.Body.String())
	}
	check("stop", http.StatusForbidden)
}
