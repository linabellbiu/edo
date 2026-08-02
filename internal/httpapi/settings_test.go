package httpapi

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestLoginLockoutDefaultsOffAndCanBeEnabledFromSettings(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	for range 3 {
		failed := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
			"username": "admin", "password": "wrong password",
		}, nil)
		if failed.Code != http.StatusUnauthorized {
			t.Fatalf("默认关闭时错误密码响应异常: status=%d body=%s", failed.Code, failed.Body.String())
		}
	}
	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("默认关闭时不应锁定登录: status=%d body=%s", login.Code, login.Body.String())
	}
	adminCookie := login.Result().Cookies()[0]

	current := performJSONRequest(t, router, http.MethodGet, "/api/v1/settings/login-lockout", nil, adminCookie)
	if current.Code != http.StatusOK || !bytes.Contains(current.Body.Bytes(), []byte(`"enabled":false`)) ||
		!bytes.Contains(current.Body.Bytes(), []byte(`"max_failures":3`)) {
		t.Fatalf("登录锁定默认设置错误: status=%d body=%s", current.Code, current.Body.String())
	}
	enabled := performJSONRequest(t, router, http.MethodPut, "/api/v1/settings/login-lockout", map[string]any{
		"enabled": true, "expected_version": 0,
	}, adminCookie)
	if enabled.Code != http.StatusOK || !bytes.Contains(enabled.Body.Bytes(), []byte(`"enabled":true`)) {
		t.Fatalf("启用登录锁定失败: status=%d body=%s", enabled.Code, enabled.Body.String())
	}

	for range 3 {
		failed := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
			"username": "admin", "password": "wrong password",
		}, nil)
		if failed.Code != http.StatusUnauthorized {
			t.Fatalf("启用后错误密码响应异常: status=%d body=%s", failed.Code, failed.Body.String())
		}
	}
	blocked := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("达到阈值后未锁定登录: status=%d body=%s", blocked.Code, blocked.Body.String())
	}

	disabled := performJSONRequest(t, router, http.MethodPut, "/api/v1/settings/login-lockout", map[string]any{
		"enabled": false, "expected_version": 1,
	}, adminCookie)
	if disabled.Code != http.StatusOK {
		t.Fatalf("关闭登录锁定失败: status=%d body=%s", disabled.Code, disabled.Body.String())
	}
	unblocked := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if unblocked.Code != http.StatusOK {
		t.Fatalf("关闭后登录仍被锁定: status=%d body=%s", unblocked.Code, unblocked.Body.String())
	}
}

func TestRuntimeLoggingCanBeUpdatedWithoutRestart(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	adminCookie := login.Result().Cookies()[0]

	current := performJSONRequest(t, router, http.MethodGet, "/api/v1/settings/runtime-logging", nil, adminCookie)
	if current.Code != http.StatusOK || !bytes.Contains(current.Body.Bytes(), []byte(`"level":"info"`)) ||
		!bytes.Contains(current.Body.Bytes(), []byte(`"http_access_enabled":true`)) ||
		!bytes.Contains(current.Body.Bytes(), []byte(`"max_file_size_mb":100`)) ||
		!bytes.Contains(current.Body.Bytes(), []byte(`"compress_after_days":3`)) {
		t.Fatalf("运行日志默认设置错误: status=%d body=%s", current.Code, current.Body.String())
	}
	updated := performJSONRequest(t, router, http.MethodPut, "/api/v1/settings/runtime-logging", map[string]any{
		"level": "error", "http_access_enabled": false,
		"file_enabled": false, "file_directory": "logs", "max_file_size_mb": 256, "compress_after_days": 7,
		"expected_version": 0,
	}, adminCookie)
	if updated.Code != http.StatusOK || !bytes.Contains(updated.Body.Bytes(), []byte(`"level":"error"`)) ||
		!bytes.Contains(updated.Body.Bytes(), []byte(`"http_access_enabled":false`)) ||
		!bytes.Contains(updated.Body.Bytes(), []byte(`"max_file_size_mb":256`)) {
		t.Fatalf("热更新运行日志设置失败: status=%d body=%s", updated.Code, updated.Body.String())
	}
	invalid := performJSONRequest(t, router, http.MethodPut, "/api/v1/settings/runtime-logging", map[string]any{
		"level": "verbose", "http_access_enabled": true,
		"file_enabled": true, "file_directory": "logs", "max_file_size_mb": 100, "compress_after_days": 3,
		"expected_version": 1,
	}, adminCookie)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("无效日志级别未被接口拒绝: status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	persisted := performJSONRequest(t, router, http.MethodGet, "/api/v1/settings/runtime-logging", nil, adminCookie)
	if persisted.Code != http.StatusOK || !bytes.Contains(persisted.Body.Bytes(), []byte(`"version":1`)) ||
		!bytes.Contains(persisted.Body.Bytes(), []byte(`"http_access_enabled":false`)) {
		t.Fatalf("运行日志设置未持久化: status=%d body=%s", persisted.Code, persisted.Body.String())
	}
}

func TestRuntimeDirectoriesCanBeHotUpdatedAndCleanedSeparately(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	adminCookie := login.Result().Cookies()[0]

	current := performJSONRequest(t, router, http.MethodGet, "/api/v1/settings/runtime-directories", nil, adminCookie)
	if current.Code != http.StatusOK || !bytes.Contains(current.Body.Bytes(), []byte(`"version":0`)) ||
		!bytes.Contains(current.Body.Bytes(), []byte(`"workspace_usage"`)) ||
		!bytes.Contains(current.Body.Bytes(), []byte(`"build_usage"`)) ||
		!bytes.Contains(current.Body.Bytes(), []byte(`"cache_usage"`)) ||
		!bytes.Contains(current.Body.Bytes(), []byte(`"artifact_usage"`)) {
		t.Fatalf("读取默认运行目录失败: status=%d body=%s", current.Code, current.Body.String())
	}
	workspaceDirectory, buildDirectory, cacheDirectory, artifactDirectory := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	updated := performJSONRequest(t, router, http.MethodPut, "/api/v1/settings/runtime-directories", map[string]any{
		"workspace_directory":      workspaceDirectory,
		"build_directory":          buildDirectory,
		"cache_directory":          cacheDirectory,
		"local_artifact_directory": artifactDirectory,
		"expected_version":         0,
	}, adminCookie)
	if updated.Code != http.StatusOK || !bytes.Contains(updated.Body.Bytes(), []byte(`"version":1`)) ||
		!bytes.Contains(updated.Body.Bytes(), []byte(`"workspace_directory"`)) ||
		!bytes.Contains(updated.Body.Bytes(), []byte(`"workspace_usage":{"path":"`)) ||
		!bytes.Contains(updated.Body.Bytes(), []byte(`"artifact_usage":{"path":"`)) {
		t.Fatalf("热更新运行目录失败: status=%d body=%s", updated.Code, updated.Body.String())
	}
	for _, target := range []string{
		filepath.Join(workspaceDirectory, "workspace", "source.go"),
		filepath.Join(buildDirectory, "output", "app.jar"),
		filepath.Join(cacheDirectory, "objects", "pack"),
		filepath.Join(artifactDirectory, "blobs", "sha256", "1234"),
	} {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("1234"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, cleanup := range []struct {
		path string
		file string
	}{
		{path: "/api/v1/settings/runtime-directories/cleanup-workspaces", file: filepath.Join(workspaceDirectory, "workspace", "source.go")},
		{path: "/api/v1/settings/runtime-directories/cleanup-builds", file: filepath.Join(buildDirectory, "output", "app.jar")},
		{path: "/api/v1/settings/runtime-directories/cleanup-cache", file: filepath.Join(cacheDirectory, "objects", "pack")},
		{path: "/api/v1/settings/runtime-directories/cleanup-artifacts", file: filepath.Join(artifactDirectory, "blobs", "sha256", "1234")},
	} {
		response := performJSONRequest(t, router, http.MethodPost, cleanup.path, nil, adminCookie)
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"files_deleted":1`)) {
			t.Fatalf("运行目录独立清理失败: path=%s status=%d body=%s", cleanup.path, response.Code, response.Body.String())
		}
		if _, err := os.Stat(cleanup.file); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("目标文件未被清理: path=%s err=%v", cleanup.file, err)
		}
	}

	overlapRoot := t.TempDir()
	overlap := performJSONRequest(t, router, http.MethodPut, "/api/v1/settings/runtime-directories", map[string]any{
		"workspace_directory":      overlapRoot,
		"build_directory":          t.TempDir(),
		"cache_directory":          filepath.Join(overlapRoot, "cache"),
		"local_artifact_directory": t.TempDir(),
		"expected_version":         1,
	}, adminCookie)
	if overlap.Code != http.StatusBadRequest || !bytes.Contains(overlap.Body.Bytes(), []byte(`"code":"directory_overlap"`)) {
		t.Fatalf("互相包含的运行目录未被拒绝: status=%d body=%s", overlap.Code, overlap.Body.String())
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
	request.Header.Set("X-EDO-Event", "push")
	request.Header.Set("X-EDO-Delivery", deliveryID)
	request.Header.Set("X-EDO-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
