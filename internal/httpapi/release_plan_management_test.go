package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestReleasePlanManagementAPI(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	adminCookie := login.Result().Cookies()[0]
	repositoryResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/repositories", map[string]any{
		"name": "发布计划管理仓库", "provider": "generic", "clone_url": "https://git.example.com/team/plan.git",
		"default_branch": "main", "auth_type": "none",
	}, adminCookie)
	var repositoryPayload struct {
		Repository struct {
			ID string `json:"id"`
		} `json:"repository"`
	}
	if repositoryResponse.Code != http.StatusCreated || json.Unmarshal(repositoryResponse.Body.Bytes(), &repositoryPayload) != nil {
		t.Fatalf("创建发布计划管理仓库失败: status=%d body=%s", repositoryResponse.Code, repositoryResponse.Body.String())
	}
	applicationResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/applications", map[string]any{
		"name": "release_plan_management_app", "repository_id": repositoryPayload.Repository.ID, "branch": "main",
		"poll_enabled": true, "poll_interval_seconds": 60, "watch_push": true,
	}, adminCookie)
	var applicationPayload struct {
		Application struct {
			ID string `json:"id"`
		} `json:"application"`
	}
	if applicationResponse.Code != http.StatusCreated || json.Unmarshal(applicationResponse.Body.Bytes(), &applicationPayload) != nil {
		t.Fatalf("创建发布计划管理应用失败: status=%d body=%s", applicationResponse.Code, applicationResponse.Body.String())
	}
	createResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/release-plans", map[string]any{
		"description": "首版说明",
		"groups": []map[string]any{{
			"name": "默认发布组", "applications": []map[string]any{{"application_id": applicationPayload.Application.ID}},
		}},
	}, adminCookie)
	var planPayload struct {
		ReleasePlan struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Version  string `json:"version"`
			Status   string `json:"status"`
			IsActive bool   `json:"is_active"`
			Groups   []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"groups"`
		} `json:"release_plan"`
	}
	if createResponse.Code != http.StatusCreated || json.Unmarshal(createResponse.Body.Bytes(), &planPayload) != nil ||
		planPayload.ReleasePlan.ID == "" || !planPayload.ReleasePlan.IsActive || len(planPayload.ReleasePlan.Groups) != 1 {
		t.Fatalf("创建发布计划失败: status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	configurationResponse := performJSONRequest(t, router, http.MethodPut, "/api/v1/release-plans/"+planPayload.ReleasePlan.ID+"/configuration", map[string]any{
		"description": "批量配置说明",
		"groups": []map[string]any{{
			"id": planPayload.ReleasePlan.Groups[0].ID, "name": "串行发布组", "mode": "sequential",
			"failure_policy": "stop", "applications": []map[string]any{{"application_id": applicationPayload.Application.ID}},
			"depends_on_group_ids": []string{},
		}},
	}, adminCookie)
	if configurationResponse.Code != http.StatusOK || !strings.Contains(configurationResponse.Body.String(), `"description":"批量配置说明"`) ||
		!strings.Contains(configurationResponse.Body.String(), `"mode":"sequential"`) || !strings.Contains(configurationResponse.Body.String(), `"sort_order":0`) {
		t.Fatalf("原子保存发布计划配置失败: status=%d body=%s", configurationResponse.Code, configurationResponse.Body.String())
	}
	updateResponse := performJSONRequest(t, router, http.MethodPut, "/api/v1/release-plans/"+planPayload.ReleasePlan.ID, map[string]any{
		"description": "更新后的说明",
	}, adminCookie)
	if updateResponse.Code != http.StatusOK || !strings.Contains(updateResponse.Body.String(), `"description":"更新后的说明"`) ||
		!strings.Contains(updateResponse.Body.String(), `"name":"`+planPayload.ReleasePlan.Name+`"`) ||
		!strings.Contains(updateResponse.Body.String(), `"version":"`+planPayload.ReleasePlan.Version+`"`) {
		t.Fatalf("更新发布计划未保留内部标识: status=%d body=%s", updateResponse.Code, updateResponse.Body.String())
	}
	disableResponse := performJSONRequest(t, router, http.MethodPatch, "/api/v1/release-plans/"+planPayload.ReleasePlan.ID+"/status", map[string]any{
		"active": false,
	}, adminCookie)
	if disableResponse.Code != http.StatusOK || !strings.Contains(disableResponse.Body.String(), `"is_active":false`) ||
		!strings.Contains(disableResponse.Body.String(), `"status":"draft"`) {
		t.Fatalf("停用发布计划不应改变生命周期: status=%d body=%s", disableResponse.Code, disableResponse.Body.String())
	}
	var disabledPayload struct {
		ReleasePlan struct {
			UpdatedAt string `json:"updated_at"`
		} `json:"release_plan"`
	}
	if json.Unmarshal(disableResponse.Body.Bytes(), &disabledPayload) != nil || disabledPayload.ReleasePlan.UpdatedAt == "" {
		t.Fatalf("停用响应缺少更新时间: body=%s", disableResponse.Body.String())
	}
	executeDisabled := performJSONRequest(t, router, http.MethodPost, "/api/v1/release-plans/"+planPayload.ReleasePlan.ID+"/executions", map[string]any{
		"request_id": "disabled-plan-http", "expected_plan_updated_at": disabledPayload.ReleasePlan.UpdatedAt,
		"selections": []map[string]any{{
			"release_group_application_id": "placeholder", "expected_workflow_revision": 1,
			"workflow_id":    "placeholder",
			"source_node_id": "manual", "ref": "refs/heads/main", "commit_sha": strings.Repeat("a", 40),
		}},
	}, adminCookie)
	if executeDisabled.Code != http.StatusConflict || !strings.Contains(executeDisabled.Body.String(), "发布计划已停用") {
		t.Fatalf("停用发布计划仍允许创建执行: status=%d body=%s", executeDisabled.Code, executeDisabled.Body.String())
	}
	invalidStatus := performJSONRequest(t, router, http.MethodPatch, "/api/v1/release-plans/"+planPayload.ReleasePlan.ID+"/status", map[string]any{}, adminCookie)
	if invalidStatus.Code != http.StatusBadRequest {
		t.Fatalf("缺少 active 的停用请求应被拒绝: status=%d body=%s", invalidStatus.Code, invalidStatus.Body.String())
	}
	deleteResponse := performJSONRequest(t, router, http.MethodDelete, "/api/v1/release-plans/"+planPayload.ReleasePlan.ID, nil, adminCookie)
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("删除未执行发布计划失败: status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}
	deletedDetail := performJSONRequest(t, router, http.MethodGet, "/api/v1/release-plans/"+planPayload.ReleasePlan.ID, nil, adminCookie)
	if deletedDetail.Code != http.StatusNotFound {
		t.Fatalf("普通接口仍返回软删除发布计划: status=%d body=%s", deletedDetail.Code, deletedDetail.Body.String())
	}
	deletedList := performJSONRequest(t, router, http.MethodGet, "/api/v1/release-plans", nil, adminCookie)
	if deletedList.Code != http.StatusOK || strings.Contains(deletedList.Body.String(), planPayload.ReleasePlan.ID) {
		t.Fatalf("发布计划列表仍包含软删除记录: status=%d body=%s", deletedList.Code, deletedList.Body.String())
	}
}
