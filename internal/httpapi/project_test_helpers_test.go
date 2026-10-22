package httpapi

import (
	"context"
	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func grantProjectMembershipForTest(t *testing.T, deps Dependencies, user *model.User) {
	t.Helper()
	ctx := context.Background()
	role, err := deps.Access.CreateRole(ctx, access.RoleInput{Name: "member-" + user.ID, DisplayName: "项目基本成员", Permissions: []string{"project.read"}})
	if err != nil {
		t.Fatal(err)
	}
	actor := &model.User{ID: "test-superuser", DepartmentID: user.DepartmentID, IsSuperuser: true}
	if err = deps.Access.SetProjectMember(ctx, actor, database.DefaultProjectID(user.DepartmentID), user.ID, role.ID); err != nil {
		t.Fatal(err)
	}
}

func joinCreatedUserProjectForTest(t *testing.T, router http.Handler, admin *http.Cookie, response *httptest.ResponseRecorder, roleIDs []string) {
	t.Helper()
	var user struct {
		User struct{ ID, DepartmentID string }
	}
	// 这里直接按 API 字段读取，不依赖登录态自动继承。
	var payload struct {
		User struct {
			ID           string `json:"id"`
			DepartmentID string `json:"department_id"`
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	user.User.ID = payload.User.ID
	user.User.DepartmentID = payload.User.DepartmentID
	roles := performJSONRequest(t, router, http.MethodGet, "/api/v1/roles", nil, admin)
	var catalog struct {
		Roles []struct {
			ID          string
			Permissions []string
		}
	}
	if err := json.Unmarshal(roles.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	codes := []string{"project.read"}
	for _, role := range catalog.Roles {
		for _, id := range roleIDs {
			if role.ID == id {
				for _, code := range role.Permissions {
					if access.ProjectPermission(code) {
						codes = append(codes, code)
					}
				}
			}
		}
	}
	role := createRoleForHTTPTest(t, router, admin, "project-"+user.User.ID, codes)
	assigned := performJSONRequest(t, router, http.MethodPut, "/api/v1/projects/"+database.DefaultProjectID(user.User.DepartmentID)+"/members/"+user.User.ID, map[string]string{"role_id": role}, admin)
	if assigned.Code != 204 {
		t.Fatal(assigned.Code, assigned.Body.String())
	}
}
