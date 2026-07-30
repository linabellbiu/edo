package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
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
			ID             string `json:"id"`
			DockerfilePath string `json:"dockerfile_path"`
			ContextPath    string `json:"context_path"`
			Pull           bool   `json:"pull"`
			CacheEnabled   bool   `json:"cache_enabled"`
		} `json:"build_plan"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &createdPayload) != nil ||
		createdPayload.BuildPlan.DockerfilePath != "Dockerfile" || createdPayload.BuildPlan.ContextPath != "." ||
		!createdPayload.BuildPlan.Pull || !createdPayload.BuildPlan.CacheEnabled {
		t.Fatalf("创建构建方案失败: status=%d body=%s", created.Code, created.Body.String())
	}

	updated := performJSONRequest(t, router, http.MethodPut, "/api/v1/build-plans/"+createdPayload.BuildPlan.ID, map[string]any{
		"name": "正式构建", "kind": "dockerfile", "description": "构建服务镜像",
		"dockerfile_path": "deploy/Dockerfile", "context_path": "service",
		"timeout_seconds": 900,
	}, adminCookie)
	var updatedPayload struct {
		BuildPlan struct {
			Name           string `json:"name"`
			DockerfilePath string `json:"dockerfile_path"`
			ContextPath    string `json:"context_path"`
			Pull           bool   `json:"pull"`
			CacheEnabled   bool   `json:"cache_enabled"`
		} `json:"build_plan"`
	}
	if updated.Code != http.StatusOK || json.Unmarshal(updated.Body.Bytes(), &updatedPayload) != nil ||
		updatedPayload.BuildPlan.Name != "正式构建" ||
		updatedPayload.BuildPlan.DockerfilePath != "deploy/Dockerfile" ||
		updatedPayload.BuildPlan.ContextPath != "service" ||
		!updatedPayload.BuildPlan.Pull || !updatedPayload.BuildPlan.CacheEnabled {
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

func TestScriptBuildPlanRuntimeImageContract(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	adminCookie := login.Result().Cookies()[0]
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/build-plans", map[string]any{
		"name": "脚本制品构建", "kind": "script", "script": "printf done > output",
		"artifact_path": "output", "working_directory": ".", "timeout_seconds": 120,
	}, adminCookie)
	var payload struct {
		BuildPlan map[string]any `json:"build_plan"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &payload) != nil {
		t.Fatalf("创建脚本构建方案失败: status=%d body=%s", created.Code, created.Body.String())
	}
	if payload.BuildPlan["runtime_image"] != "alpine:3.22" {
		t.Fatalf("接口没有返回默认运行镜像: body=%s", created.Body.String())
	}
	if _, exists := payload.BuildPlan["package_format"]; exists {
		t.Fatalf("接口仍暴露已经移除的 package_format: body=%s", created.Body.String())
	}

	rejected := performJSONRequest(t, router, http.MethodPost, "/api/v1/build-plans", map[string]any{
		"name": "浮动脚本镜像", "kind": "script", "script": "echo ok",
		"artifact_path": "output", "runtime_image": "alpine:latest", "timeout_seconds": 120,
	}, adminCookie)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("接口允许 latest 运行镜像: status=%d body=%s", rejected.Code, rejected.Body.String())
	}

	reservedEnvironment := performJSONRequest(t, router, http.MethodPost, "/api/v1/build-plans", map[string]any{
		"name": "保留变量脚本构建", "kind": "script", "script": "echo ok > output",
		"artifact_path": "output", "runtime_image": "alpine:3.22", "timeout_seconds": 120,
		"environment_variables": map[string]string{"HOME": "/tmp/fake"},
	}, adminCookie)
	if reservedEnvironment.Code != http.StatusBadRequest ||
		!strings.Contains(reservedEnvironment.Body.String(), "脚本环境变量无效") {
		t.Fatalf("保留环境变量未返回明确 400: status=%d body=%s", reservedEnvironment.Code, reservedEnvironment.Body.String())
	}
}
