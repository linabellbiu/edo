package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestBuildPlanManagementEndpoints(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	adminCookie := login.Result().Cookies()[0]
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/build-plans", map[string]any{
		"name": "待修改构建", "kind": "dockerfile", "dockerfile_path": "Dockerfile",
		"context_path": ".", "timeout_seconds": 1800,
	}, adminCookie)
	var createdPayload struct {
		BuildPlan struct {
			ID string `json:"id"`
		} `json:"build_plan"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &createdPayload) != nil {
		t.Fatalf("创建构建方案失败: status=%d body=%s", created.Code, created.Body.String())
	}

	updated := performJSONRequest(t, router, http.MethodPut, "/api/v1/build-plans/"+createdPayload.BuildPlan.ID, map[string]any{
		"name": "正式构建", "kind": "dockerfile", "description": "构建服务镜像",
		"dockerfile_path": "deploy/Dockerfile", "context_path": "service", "artifact_path": "dist",
		"timeout_seconds": 900,
	}, adminCookie)
	var updatedPayload struct {
		BuildPlan struct {
			Name           string `json:"name"`
			DockerfilePath string `json:"dockerfile_path"`
			ArtifactPath   string `json:"artifact_path"`
		} `json:"build_plan"`
	}
	if updated.Code != http.StatusOK || json.Unmarshal(updated.Body.Bytes(), &updatedPayload) != nil ||
		updatedPayload.BuildPlan.Name != "正式构建" ||
		updatedPayload.BuildPlan.DockerfilePath != "deploy/Dockerfile" ||
		updatedPayload.BuildPlan.ArtifactPath != "dist" {
		t.Fatalf("更新构建方案失败: status=%d body=%s", updated.Code, updated.Body.String())
	}

	disabled := performJSONRequest(t, router, http.MethodPatch, "/api/v1/build-plans/"+createdPayload.BuildPlan.ID+"/status", map[string]any{
		"active": false,
	}, adminCookie)
	if disabled.Code != http.StatusNoContent {
		t.Fatalf("停用构建方案失败: status=%d body=%s", disabled.Code, disabled.Body.String())
	}
	deleted := performJSONRequest(t, router, http.MethodDelete, "/api/v1/build-plans/"+createdPayload.BuildPlan.ID, nil, adminCookie)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("删除构建方案失败: status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	missing := performJSONRequest(t, router, http.MethodDelete, "/api/v1/build-plans/"+createdPayload.BuildPlan.ID, nil, adminCookie)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("重复删除构建方案未返回 404: status=%d body=%s", missing.Code, missing.Body.String())
	}
}
