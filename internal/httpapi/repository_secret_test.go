package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestWebhookSecretRequiresPermissionAndCanBeReadRepeatedly(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	adminLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	adminCookie := adminLogin.Result().Cookies()[0]
	role := performJSONRequest(t, router, http.MethodPost, "/api/v1/roles", map[string]any{
		"name": "repository-manager", "display_name": "仓库管理员",
		"permissions": []string{"repository.read", "repository.manage"},
	}, adminCookie)
	var rolePayload struct {
		Role struct {
			ID string `json:"id"`
		} `json:"role"`
	}
	if role.Code != http.StatusCreated || json.Unmarshal(role.Body.Bytes(), &rolePayload) != nil {
		t.Fatalf("创建仓库角色失败: status=%d body=%s", role.Code, role.Body.String())
	}
	user := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "repository-user", "nickname": "仓库用户", "password": "correct horse battery staple",
		"role_ids": []string{rolePayload.Role.ID},
	}, adminCookie)
	var userPayload struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if user.Code != http.StatusCreated || json.Unmarshal(user.Body.Bytes(), &userPayload) != nil {
		t.Fatalf("创建仓库用户失败: status=%d body=%s", user.Code, user.Body.String())
	}
	userLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "repository-user", "password": "correct horse battery staple",
	}, nil)
	userCookie := userLogin.Result().Cookies()[0]
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/repositories", map[string]any{
		"name": "webhook-repository", "provider": "github", "clone_url": "https://github.com/example/project.git",
		"auth_type": "none", "webhook_enabled": true,
	}, userCookie)
	var repositoryPayload struct {
		Repository struct {
			ID string `json:"id"`
		} `json:"repository"`
		WebhookSecret string `json:"webhook_secret"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &repositoryPayload) != nil {
		t.Fatalf("创建 Webhook 仓库失败: status=%d body=%s", created.Code, created.Body.String())
	}
	if repositoryPayload.WebhookSecret != "" {
		t.Fatal("缺少 Webhook 密钥读取权限的仓库管理员在创建响应中拿到了密钥")
	}
	denied := performJSONRequest(t, router, http.MethodGet, "/api/v1/repositories/"+repositoryPayload.Repository.ID+"/webhook", nil, userCookie)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺少权限的 Webhook 密钥读取未被拒绝: status=%d body=%s", denied.Code, denied.Body.String())
	}
	grant := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userPayload.User.ID+"/permissions", map[string]any{
		"allow": []string{"repository.secret.read"}, "deny": []string{},
	}, adminCookie)
	if grant.Code != http.StatusNoContent {
		t.Fatalf("授予 Webhook 密钥读取权限失败: status=%d body=%s", grant.Code, grant.Body.String())
	}
	first := performJSONRequest(t, router, http.MethodGet, "/api/v1/repositories/"+repositoryPayload.Repository.ID+"/webhook", nil, userCookie)
	second := performJSONRequest(t, router, http.MethodGet, "/api/v1/repositories/"+repositoryPayload.Repository.ID+"/webhook", nil, userCookie)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || first.Body.String() != second.Body.String() ||
		!bytes.Contains(first.Body.Bytes(), []byte("webhook_secret")) || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Webhook 密钥不能稳定重复读取: first=%d/%s second=%d/%s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
}
