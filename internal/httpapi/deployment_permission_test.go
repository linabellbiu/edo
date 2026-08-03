package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestRollbackRequiresDeploymentExecuteInsteadOfReview(t *testing.T) {
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
	for _, path := range []string{
		"/api/v1/deployments/not-found/runtime/restart",
		"/api/v1/deployments/not-found/runtime/stop",
		"/api/v1/deployments/not-found/runtime/scale",
	} {
		body := any(nil)
		if path == "/api/v1/deployments/not-found/runtime/scale" {
			body = map[string]any{"replicas": 2}
		}
		response := performJSONRequest(t, router, http.MethodPost, path, body, reviewerCookie)
		if response.Code != http.StatusForbidden {
			t.Fatalf("只有 deployment.review 时仍可控制运行资源: path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	removed := performJSONRequest(t, router, http.MethodDelete, "/api/v1/deployments/not-found/runtime", nil, reviewerCookie)
	if removed.Code != http.StatusForbidden {
		t.Fatalf("只有 deployment.review 时仍可删除容器实例: status=%d body=%s", removed.Code, removed.Body.String())
	}
	grant := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userPayload.User.ID+"/permissions", map[string]any{
		"allow": []string{"deployment.execute"}, "deny": []string{},
	}, adminCookie)
	if grant.Code != http.StatusNoContent {
		t.Fatalf("授予 deployment.execute 失败: status=%d body=%s", grant.Code, grant.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		body := any(nil)
		if method == http.MethodPut {
			body = map[string]any{"restart_script": "true", "stop_script": "true", "timeout_seconds": 300}
		}
		response := performJSONRequest(t, router, method, "/api/v1/deployments/not-found/runtime/configuration", body, reviewerCookie)
		if response.Code != http.StatusForbidden {
			t.Fatalf("只有 deployment.execute 时仍可读取或修改 Shell 部署实例命令: method=%s status=%d body=%s", method, response.Code, response.Body.String())
		}
	}
	allowed := performJSONRequest(t, router, http.MethodPost, "/api/v1/deployments/not-found/rollback", nil, reviewerCookie)
	if allowed.Code != http.StatusNotFound {
		t.Fatalf("授予 deployment.execute 后未进入回滚业务校验: status=%d body=%s", allowed.Code, allowed.Body.String())
	}
	for _, path := range []string{
		"/api/v1/deployments/not-found/runtime/restart",
		"/api/v1/deployments/not-found/runtime/stop",
		"/api/v1/deployments/not-found/runtime/scale",
	} {
		body := any(nil)
		if path == "/api/v1/deployments/not-found/runtime/scale" {
			body = map[string]any{"replicas": 2}
		}
		response := performJSONRequest(t, router, http.MethodPost, path, body, reviewerCookie)
		if response.Code != http.StatusNotFound {
			t.Fatalf("授予 deployment.execute 后未进入运行控制业务校验: path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	removed = performJSONRequest(t, router, http.MethodDelete, "/api/v1/deployments/not-found/runtime", nil, reviewerCookie)
	if removed.Code != http.StatusNotFound {
		t.Fatalf("授予 deployment.execute 后未进入删除容器实例业务校验: status=%d body=%s", removed.Code, removed.Body.String())
	}
}
