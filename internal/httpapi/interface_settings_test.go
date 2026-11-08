package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"edo/internal/configuration"
)

func TestGitHubLogoSettingsAllowAuthenticatedReadAndRequirePlatformUpdate(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	endpoint := "/api/v1/settings/github-logo"
	if response := performJSONRequest(t, router, http.MethodGet, endpoint, nil, nil); response.Code != http.StatusUnauthorized {
		t.Fatal("未登录读取未被拒绝", response.Code)
	}
	admin := loginForDepartmentTest(t, router, "admin")
	read := performJSONRequest(t, router, http.MethodGet, endpoint, nil, admin)
	var original configuration.GitHubLogoSettings
	if read.Code != http.StatusOK || json.Unmarshal(read.Body.Bytes(), &original) != nil || !original.Enabled || original.Version != 0 {
		t.Fatal("默认显示状态错误", read.Code, read.Body.String())
	}
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{"username": "logo-viewer", "nickname": "普通用户", "password": "correct horse battery staple"}, admin)
	if created.Code != http.StatusCreated {
		t.Fatal(created.Code, created.Body.String())
	}
	viewer := loginForDepartmentTest(t, router, "logo-viewer")
	if response := performJSONRequest(t, router, http.MethodPut, endpoint, map[string]any{"enabled": false, "expected_version": 0}, viewer); response.Code != http.StatusForbidden {
		t.Fatal("普通用户不能更改全站设置", response.Code, response.Body.String())
	}
	if response := performJSONRequest(t, router, http.MethodPut, endpoint, map[string]any{"expected_version": 0}, admin); response.Code != http.StatusBadRequest {
		t.Fatal("缺少开关值应拒绝", response.Code)
	}
	changed := performJSONRequest(t, router, http.MethodPut, endpoint, map[string]any{"enabled": false, "expected_version": 0}, admin)
	if changed.Code != http.StatusOK {
		t.Fatal(changed.Code, changed.Body.String())
	}
	read = performJSONRequest(t, router, http.MethodGet, endpoint, nil, viewer)
	var saved configuration.GitHubLogoSettings
	if read.Code != http.StatusOK || json.Unmarshal(read.Body.Bytes(), &saved) != nil || saved.Enabled || saved.Version != 1 {
		t.Fatal("普通用户没有读取到已关闭的全站设置", read.Code, read.Body.String())
	}
	if response := performJSONRequest(t, router, http.MethodPut, endpoint, map[string]any{"enabled": true, "expected_version": 0}, admin); response.Code != http.StatusConflict {
		t.Fatal("过期版本未被拒绝", response.Code, response.Body.String())
	}
	if response := performJSONRequest(t, router, http.MethodPut, endpoint, map[string]any{"enabled": true, "expected_version": 1}, admin); response.Code != http.StatusOK {
		t.Fatal("重新开启失败", response.Code, response.Body.String())
	}
}
