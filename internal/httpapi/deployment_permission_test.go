package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestRollbackRequiresDeploymentRunInsteadOfReview(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	adminLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if adminLogin.Code != http.StatusOK {
		t.Fatalf("管理员登录失败: status=%d body=%s", adminLogin.Code, adminLogin.Body.String())
	}
	adminCookie := adminLogin.Result().Cookies()[0]
	roleResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/roles", map[string]any{
		"name": "deployment-reviewer", "display_name": "发布审核员", "permissions": []string{"deployment.review"},
	}, adminCookie)
	var rolePayload struct {
		Role struct {
			ID string `json:"id"`
		} `json:"role"`
	}
	if roleResponse.Code != http.StatusCreated || json.Unmarshal(roleResponse.Body.Bytes(), &rolePayload) != nil || rolePayload.Role.ID == "" {
		t.Fatalf("创建发布审核角色失败: status=%d body=%s", roleResponse.Code, roleResponse.Body.String())
	}
	userResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "deployment-reviewer", "nickname": "发布审核员", "password": "correct horse battery staple",
		"role_ids": []string{rolePayload.Role.ID},
	}, adminCookie)
	var userPayload struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if userResponse.Code != http.StatusCreated || json.Unmarshal(userResponse.Body.Bytes(), &userPayload) != nil || userPayload.User.ID == "" {
		t.Fatalf("创建发布审核用户失败: status=%d body=%s", userResponse.Code, userResponse.Body.String())
	}
	reviewerLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "deployment-reviewer", "password": "correct horse battery staple",
	}, nil)
	if reviewerLogin.Code != http.StatusOK {
		t.Fatalf("发布审核用户登录失败: status=%d body=%s", reviewerLogin.Code, reviewerLogin.Body.String())
	}
	reviewerCookie := reviewerLogin.Result().Cookies()[0]

	denied := performJSONRequest(t, router, http.MethodPost, "/api/v1/deployments/not-found/rollback", nil, reviewerCookie)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("只有 deployment.review 时仍可发起回滚: status=%d body=%s", denied.Code, denied.Body.String())
	}
	grant := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userPayload.User.ID+"/permissions", map[string]any{
		"allow": []string{"deployment.run"}, "deny": []string{},
	}, adminCookie)
	if grant.Code != http.StatusNoContent {
		t.Fatalf("授予 deployment.run 失败: status=%d body=%s", grant.Code, grant.Body.String())
	}
	allowed := performJSONRequest(t, router, http.MethodPost, "/api/v1/deployments/not-found/rollback", nil, reviewerCookie)
	if allowed.Code != http.StatusNotFound {
		t.Fatalf("授予 deployment.run 后未进入回滚业务校验: status=%d body=%s", allowed.Code, allowed.Body.String())
	}
}
