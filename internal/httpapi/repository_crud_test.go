package httpapi

import (
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
