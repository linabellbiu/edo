package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestRepositoryCanBeUpdatedAndDeleted(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	adminCookie := login.Result().Cookies()[0]
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/repositories", map[string]any{
		"name": "待修改仓库", "provider": "generic", "clone_url": "https://git.example.com/team/old.git",
		"auth_type": "none", "webhook_enabled": false,
	}, adminCookie)
	var createdPayload struct {
		Repository struct {
			ID string `json:"id"`
		} `json:"repository"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &createdPayload) != nil {
		t.Fatalf("创建代码仓库失败: status=%d body=%s", created.Code, created.Body.String())
	}

	updated := performJSONRequest(t, router, http.MethodPut, "/api/v1/repositories/"+createdPayload.Repository.ID, map[string]any{
		"name": "已修改仓库", "provider": "gitea", "clone_url": "https://git.example.com/team/new.git",
		"default_branch": "develop", "auth_type": "none", "webhook_enabled": true,
	}, adminCookie)
	var updatedPayload struct {
		Repository struct {
			Name          string `json:"name"`
			DefaultBranch string `json:"default_branch"`
		} `json:"repository"`
	}
	if updated.Code != http.StatusOK || json.Unmarshal(updated.Body.Bytes(), &updatedPayload) != nil ||
		updatedPayload.Repository.Name != "已修改仓库" || updatedPayload.Repository.DefaultBranch != "develop" {
		t.Fatalf("修改代码仓库失败: status=%d body=%s", updated.Code, updated.Body.String())
	}

	deleted := performJSONRequest(t, router, http.MethodDelete, "/api/v1/repositories/"+createdPayload.Repository.ID, nil, adminCookie)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("删除代码仓库失败: status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	missing := performJSONRequest(t, router, http.MethodDelete, "/api/v1/repositories/"+createdPayload.Repository.ID, nil, adminCookie)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("重复删除代码仓库未返回 404: status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestRepositoryAPICredentialIsIsolatedByCurrentUser(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	adminLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	adminCookie := adminLogin.Result().Cookies()[0]
	adminCredentialResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/git-credentials", map[string]any{
		"name": "管理员 GitHub API", "provider": "github", "auth_type": "token", "secret": "admin-private-api-token",
	}, adminCookie)
	var adminCredential struct {
		Credential struct {
			ID string `json:"id"`
		} `json:"credential"`
	}
	if adminCredentialResponse.Code != http.StatusCreated || json.Unmarshal(adminCredentialResponse.Body.Bytes(), &adminCredential) != nil {
		t.Fatalf("创建管理员 API 令牌失败: status=%d body=%s", adminCredentialResponse.Code, adminCredentialResponse.Body.String())
	}

	roleResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/roles", map[string]any{
		"name": "repository-credential-owner", "display_name": "仓库凭据用户",
		"permissions": []string{"repository.read", "repository.create", "repository.update", "credential.read", "credential.create"},
	}, adminCookie)
	var role struct {
		Role struct {
			ID string `json:"id"`
		} `json:"role"`
	}
	if roleResponse.Code != http.StatusCreated || json.Unmarshal(roleResponse.Body.Bytes(), &role) != nil {
		t.Fatalf("创建仓库凭据角色失败: status=%d body=%s", roleResponse.Code, roleResponse.Body.String())
	}
	userResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "repository-credential-user", "nickname": "仓库凭据用户", "password": "correct horse battery staple",
		"role_ids": []string{role.Role.ID},
	}, adminCookie)
	if userResponse.Code != http.StatusCreated {
		t.Fatalf("创建仓库凭据用户失败: status=%d body=%s", userResponse.Code, userResponse.Body.String())
	}
	userLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "repository-credential-user", "password": "correct horse battery staple",
	}, nil)
	userCookie := userLogin.Result().Cookies()[0]
	userCredentialResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/git-credentials", map[string]any{
		"name": "我的 GitHub API", "provider": "github", "auth_type": "token", "secret": "user-private-api-token",
	}, userCookie)
	var userCredential struct {
		Credential struct {
			ID string `json:"id"`
		} `json:"credential"`
	}
	if userCredentialResponse.Code != http.StatusCreated || json.Unmarshal(userCredentialResponse.Body.Bytes(), &userCredential) != nil {
		t.Fatalf("创建用户 API 令牌失败: status=%d body=%s", userCredentialResponse.Code, userCredentialResponse.Body.String())
	}

	foreignCreate := performJSONRequest(t, router, http.MethodPost, "/api/v1/repositories", map[string]any{
		"name": "foreign-api-repository", "provider": "github", "clone_url": "https://github.com/example/foreign.git",
		"auth_type": "none", "api_credential_id": adminCredential.Credential.ID,
	}, userCookie)
	if foreignCreate.Code != http.StatusBadRequest {
		t.Fatalf("创建仓库时引用其他用户 API 令牌未被拒绝: status=%d body=%s", foreignCreate.Code, foreignCreate.Body.String())
	}
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/repositories", map[string]any{
		"name": "owned-api-repository", "provider": "github", "clone_url": "https://github.com/example/owned.git",
		"auth_type": "none", "api_credential_id": userCredential.Credential.ID,
	}, userCookie)
	var repositoryPayload struct {
		Repository struct {
			ID              string `json:"id"`
			APICredentialID string `json:"api_credential_id"`
		} `json:"repository"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &repositoryPayload) != nil ||
		repositoryPayload.Repository.APICredentialID != userCredential.Credential.ID {
		t.Fatalf("创建使用本人 API 令牌的仓库失败: status=%d body=%s", created.Code, created.Body.String())
	}
	foreignUpdate := performJSONRequest(t, router, http.MethodPut, "/api/v1/repositories/"+repositoryPayload.Repository.ID, map[string]any{
		"name": "owned-api-repository", "provider": "github", "clone_url": "https://github.com/example/owned.git",
		"auth_type": "none", "api_credential_id": adminCredential.Credential.ID,
	}, userCookie)
	if foreignUpdate.Code != http.StatusBadRequest {
		t.Fatalf("更新仓库时引用其他用户 API 令牌未被拒绝: status=%d body=%s", foreignUpdate.Code, foreignUpdate.Body.String())
	}
	adminList := performJSONRequest(t, router, http.MethodGet, "/api/v1/repositories", nil, adminCookie)
	if adminList.Code != http.StatusOK || bytes.Contains(adminList.Body.Bytes(), []byte(userCredential.Credential.ID)) {
		t.Fatalf("仓库接口向其他用户暴露了 API 令牌引用: status=%d body=%s", adminList.Code, adminList.Body.String())
	}
}
