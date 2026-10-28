package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestPersonalGitCredentialsAreIsolatedByOwner(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	adminLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	adminCookie := adminLogin.Result().Cookies()[0]
	adminCredential := performJSONRequest(t, router, http.MethodPost, "/api/v1/git-credentials", map[string]any{
		"name": "管理员 GitHub", "provider": "github", "auth_type": "token", "secret": "admin-token-value",
	}, adminCookie)
	if adminCredential.Code != http.StatusCreated {
		t.Fatalf("管理员保存令牌失败: status=%d body=%s", adminCredential.Code, adminCredential.Body.String())
	}

	role := performJSONRequest(t, router, http.MethodPost, "/api/v1/roles", map[string]any{
		"name": "credential-owner", "display_name": "个人令牌用户",
		"permissions": []string{"credential.read", "credential.secret.read", "credential.create"},
	}, adminCookie)
	var rolePayload struct {
		Role struct {
			ID string `json:"id"`
		} `json:"role"`
	}
	if role.Code != http.StatusCreated || json.Unmarshal(role.Body.Bytes(), &rolePayload) != nil {
		t.Fatalf("创建个人令牌角色失败: status=%d body=%s", role.Code, role.Body.String())
	}
	user := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "credential-user", "nickname": "令牌用户", "password": "correct horse battery staple",
		"role_ids": []string{rolePayload.Role.ID},
	}, adminCookie)
	if user.Code != http.StatusCreated {
		t.Fatalf("创建个人令牌用户失败: status=%d body=%s", user.Code, user.Body.String())
	}
	userLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "credential-user", "password": "correct horse battery staple",
	}, nil)
	userCookie := userLogin.Result().Cookies()[0]
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/git-credentials", map[string]any{
		"name": "我的 GitLab", "provider": "gitlab", "auth_type": "token", "secret": "user-token-value",
	}, userCookie)
	var credentialPayload struct {
		Credential struct {
			ID string `json:"id"`
		} `json:"credential"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &credentialPayload) != nil {
		t.Fatalf("普通用户保存令牌失败: status=%d body=%s", created.Code, created.Body.String())
	}

	userList := performJSONRequest(t, router, http.MethodGet, "/api/v1/git-credentials", nil, userCookie)
	if userList.Code != http.StatusOK || !bytes.Contains(userList.Body.Bytes(), []byte("我的 GitLab")) || bytes.Contains(userList.Body.Bytes(), []byte("管理员 GitHub")) {
		t.Fatalf("普通用户令牌列表未按所有者隔离: status=%d body=%s", userList.Code, userList.Body.String())
	}
	adminList := performJSONRequest(t, router, http.MethodGet, "/api/v1/git-credentials", nil, adminCookie)
	if adminList.Code != http.StatusOK || bytes.Contains(adminList.Body.Bytes(), []byte("我的 GitLab")) {
		t.Fatalf("超级管理员看到了其他用户令牌: status=%d body=%s", adminList.Code, adminList.Body.String())
	}
	adminReveal := performJSONRequest(t, router, http.MethodGet, "/api/v1/git-credentials/"+credentialPayload.Credential.ID+"/secret", nil, adminCookie)
	if adminReveal.Code != http.StatusNotFound {
		t.Fatalf("超级管理员读取了其他用户令牌: status=%d body=%s", adminReveal.Code, adminReveal.Body.String())
	}
	userReveal := performJSONRequest(t, router, http.MethodGet, "/api/v1/git-credentials/"+credentialPayload.Credential.ID+"/secret", nil, userCookie)
	if userReveal.Code != http.StatusOK || !bytes.Contains(userReveal.Body.Bytes(), []byte("user-token-value")) || userReveal.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("令牌所有者无法读取自己的令牌: status=%d body=%s", userReveal.Code, userReveal.Body.String())
	}
}

func TestPersonalCredentialRevealRequiresSeparatePermission(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	roleID := createRoleForHTTPTest(t, router, admin, "credential-metadata-reader", []string{"credential.read", "credential.create"})
	createUserForHTTPTest(t, router, admin, "credential-metadata-reader", "", []string{roleID})
	owner := loginForDepartmentTest(t, router, "credential-metadata-reader")
	const plaintext = "synthetic-separated-secret"
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/git-credentials", map[string]any{
		"name": "独立授权测试", "provider": "generic", "auth_type": "token", "secret": plaintext,
	}, owner)
	var payload struct {
		Credential struct {
			ID string `json:"id"`
		} `json:"credential"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &payload) != nil || payload.Credential.ID == "" {
		t.Fatalf("创建合成凭据失败: status=%d", created.Code)
	}
	path := "/api/v1/git-credentials/" + payload.Credential.ID + "/secret"
	for _, granted := range []bool{false, true, false} {
		permissions := []string{"credential.read", "credential.create"}
		if granted {
			permissions = append(permissions, "credential.secret.read")
		}
		updated := performJSONRequest(t, router, http.MethodPut, "/api/v1/roles/"+roleID+"/permissions", map[string]any{"permissions": permissions}, admin)
		if updated.Code != http.StatusOK {
			t.Fatalf("更新查看权限失败: status=%d", updated.Code)
		}
		listed := performJSONRequest(t, router, http.MethodGet, "/api/v1/git-credentials", nil, owner)
		if listed.Code != http.StatusOK || bytes.Contains(listed.Body.Bytes(), []byte(plaintext)) {
			t.Fatalf("元数据读取失效或暴露原文: status=%d", listed.Code)
		}
		revealed := performJSONRequest(t, router, http.MethodGet, path, nil, owner)
		if granted {
			if revealed.Code != http.StatusOK || !bytes.Contains(revealed.Body.Bytes(), []byte(plaintext)) || revealed.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("独立授权未生效或允许缓存: status=%d", revealed.Code)
			}
		} else if revealed.Code != http.StatusForbidden || bytes.Contains(revealed.Body.Bytes(), []byte(plaintext)) {
			t.Fatalf("未授权或撤权后仍返回凭据: status=%d", revealed.Code)
		}
	}
}
