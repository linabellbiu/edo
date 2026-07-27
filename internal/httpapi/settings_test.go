package httpapi

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExternalGitWebhookCanBeEnabledFromSettings(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	adminCookie := login.Result().Cookies()[0]

	current := performJSONRequest(t, router, http.MethodGet, "/api/v1/settings/external-git-webhook", nil, adminCookie)
	if current.Code != http.StatusOK || !bytes.Contains(current.Body.Bytes(), []byte(`"enabled":false`)) ||
		!bytes.Contains(current.Body.Bytes(), []byte(`"path_template":"/api/v1/webhooks/git/{repository_id}"`)) {
		t.Fatalf("外部 Git Webhook 默认设置错误: status=%d body=%s", current.Code, current.Body.String())
	}

	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/repositories", map[string]any{
		"name": "external-webhook", "provider": "generic", "clone_url": "https://git.example.com/team/project.git",
		"auth_type": "none", "webhook_enabled": true,
	}, adminCookie)
	var repositoryPayload struct {
		Repository struct {
			ID string `json:"id"`
		} `json:"repository"`
		WebhookSecret string `json:"webhook_secret"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &repositoryPayload) != nil || repositoryPayload.WebhookSecret == "" {
		t.Fatalf("创建外部 Webhook 仓库失败: status=%d body=%s", created.Code, created.Body.String())
	}

	body := []byte(`{"ref":"refs/heads/main","after":"0123456789012345678901234567890123456789"}`)
	path := "/api/v1/webhooks/git/" + repositoryPayload.Repository.ID
	if response := performGenericWebhookRequest(t, router, path, body, repositoryPayload.WebhookSecret, "delivery-disabled"); response.Code != http.StatusNotFound {
		t.Fatalf("全局开关关闭时外部 Webhook 仍可访问: status=%d body=%s", response.Code, response.Body.String())
	}

	enabled := performJSONRequest(t, router, http.MethodPut, "/api/v1/settings/external-git-webhook", map[string]any{
		"enabled": true, "expected_version": 0,
	}, adminCookie)
	if enabled.Code != http.StatusOK || !bytes.Contains(enabled.Body.Bytes(), []byte(`"enabled":true`)) {
		t.Fatalf("从设置启用外部 Git Webhook 失败: status=%d body=%s", enabled.Code, enabled.Body.String())
	}

	accepted := performGenericWebhookRequest(t, router, path, body, repositoryPayload.WebhookSecret, "delivery-enabled")
	if accepted.Code != http.StatusAccepted || !bytes.Contains(accepted.Body.Bytes(), []byte(`"job_id"`)) {
		t.Fatalf("启用后外部 Git Webhook 未进入任务队列: status=%d body=%s", accepted.Code, accepted.Body.String())
	}
}

func performGenericWebhookRequest(
	t *testing.T,
	handler http.Handler,
	path string,
	body []byte,
	secret string,
	deliveryID string,
) *httptest.ResponseRecorder {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-ZRT-Event", "push")
	request.Header.Set("X-ZRT-Delivery", deliveryID)
	request.Header.Set("X-ZRT-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
