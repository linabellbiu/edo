package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDeploymentPlansAndReleasePlansAreSeparateResources(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	adminCookie := login.Result().Cookies()[0]
	missingTarget := performJSONRequest(t, router, http.MethodPost, "/api/v1/deployment-plans", map[string]any{
		"name": "缺少位置的部署方案", "kind": "script", "script": "./deploy.sh\n", "timeout_seconds": 600,
	}, adminCookie)
	if missingTarget.Code != http.StatusBadRequest {
		t.Fatalf("新建部署方案没有同时配置部署位置时应返回 400: status=%d body=%s", missingTarget.Code, missingTarget.Body.String())
	}
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/deployment-plans", map[string]any{
		"name": "测试脚本部署", "kind": "script", "script": "./deploy.sh\n", "timeout_seconds": 600,
		"deployment_target": map[string]any{
			"name": "测试脚本部署位置", "platform": "ssh", "host_id": "httpapi-deployment-host",
			"working_directory": "/srv/test", "rollout_timeout": 300,
		},
	}, adminCookie)
	var createdPayload struct {
		DeploymentPlan struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"deployment_plan"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &createdPayload) != nil || createdPayload.DeploymentPlan.ID == "" {
		t.Fatalf("创建部署方案失败: status=%d body=%s", created.Code, created.Body.String())
	}
	updated := performJSONRequest(t, router, http.MethodPut, "/api/v1/deployment-plans/"+createdPayload.DeploymentPlan.ID, map[string]any{
		"name": "测试脚本部署", "kind": "script", "script": "./deploy-v2.sh\n", "description": "更新后的方案", "timeout_seconds": 300,
		"deployment_target": map[string]any{
			"name": "测试脚本部署位置", "platform": "ssh", "host_id": "httpapi-deployment-host",
			"working_directory": "/srv/test-v2", "rollout_timeout": 300,
		},
	}, adminCookie)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"kind":"script"`) ||
		!strings.Contains(updated.Body.String(), `"working_directory":"/srv/test-v2"`) {
		t.Fatalf("更新部署方案失败: status=%d body=%s", updated.Code, updated.Body.String())
	}
	missing := performJSONRequest(t, router, http.MethodPut, "/api/v1/deployment-plans/not-found", map[string]any{
		"name": "不存在的部署方案", "kind": "script", "script": "./deploy.sh\n", "timeout_seconds": 300,
		"deployment_target": map[string]any{
			"name": "不存在的部署位置", "platform": "ssh", "host_id": "httpapi-deployment-host",
			"working_directory": "/srv/missing", "rollout_timeout": 300,
		},
	}, adminCookie)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("更新不存在的部署方案应返回 404: status=%d body=%s", missing.Code, missing.Body.String())
	}
	repositoryCreated := performJSONRequest(t, router, http.MethodPost, "/api/v1/repositories", map[string]any{
		"name": "发布计划测试仓库", "provider": "generic", "clone_url": "https://git.example.com/team/release.git",
		"default_branch": "main", "auth_type": "none",
	}, adminCookie)
	var repositoryPayload struct {
		Repository struct {
			ID string `json:"id"`
		} `json:"repository"`
	}
	if repositoryCreated.Code != http.StatusCreated || json.Unmarshal(repositoryCreated.Body.Bytes(), &repositoryPayload) != nil || repositoryPayload.Repository.ID == "" {
		t.Fatalf("创建发布计划测试仓库失败: status=%d body=%s", repositoryCreated.Code, repositoryCreated.Body.String())
	}
	applicationCreated := performJSONRequest(t, router, http.MethodPost, "/api/v1/applications", map[string]any{
		"name": "发布计划测试应用", "repository_id": repositoryPayload.Repository.ID, "branch": "main",
		"poll_enabled": true, "poll_interval_seconds": 3, "watch_push": true,
	}, adminCookie)
	var applicationPayload struct {
		Application struct {
			ID string `json:"id"`
		} `json:"application"`
	}
	if applicationCreated.Code != http.StatusCreated || json.Unmarshal(applicationCreated.Body.Bytes(), &applicationPayload) != nil || applicationPayload.Application.ID == "" {
		t.Fatalf("创建发布计划测试应用失败: status=%d body=%s", applicationCreated.Code, applicationCreated.Body.String())
	}
	missingApplications := performJSONRequest(t, router, http.MethodPost, "/api/v1/release-plans", map[string]any{
		"name": "缺少应用的发布计划", "version": "2026.06",
	}, adminCookie)
	if missingApplications.Code != http.StatusBadRequest {
		t.Fatalf("未拒绝没有选择应用的发布计划: status=%d body=%s", missingApplications.Code, missingApplications.Body.String())
	}

	listed := performJSONRequest(t, router, http.MethodGet, "/api/v1/deployment-plans", nil, adminCookie)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"deployment_plans"`) || !strings.Contains(listed.Body.String(), createdPayload.DeploymentPlan.ID) {
		t.Fatalf("标准部署方案接口未返回已有数据: status=%d body=%s", listed.Code, listed.Body.String())
	}
	releaseCreated := performJSONRequest(t, router, http.MethodPost, "/api/v1/release-plans", map[string]any{
		"name": "七月发布列车", "version": "2026.07", "description": "一次迭代发布",
		"applications": []map[string]any{{
			"application_id": applicationPayload.Application.ID, "manual_deploy": true,
			"source_type": "branch", "source_value": "main",
		}},
	}, adminCookie)
	var releasePayload struct {
		ReleasePlan struct {
			ID          string `json:"id"`
			Version     string `json:"version"`
			Description string `json:"description"`
			Status      string `json:"status"`
			IsActive    bool   `json:"is_active"`
		} `json:"release_plan"`
	}
	if releaseCreated.Code != http.StatusCreated || json.Unmarshal(releaseCreated.Body.Bytes(), &releasePayload) != nil || releasePayload.ReleasePlan.ID == "" {
		t.Fatalf("创建发布计划失败: status=%d body=%s", releaseCreated.Code, releaseCreated.Body.String())
	}
	if !strings.Contains(releaseCreated.Body.String(), `"manual_deploy":true`) || !strings.Contains(releaseCreated.Body.String(), `"source_value":"main"`) {
		t.Fatalf("发布计划未返回手动部署版本来源: body=%s", releaseCreated.Body.String())
	}
	if !releasePayload.ReleasePlan.IsActive {
		t.Fatalf("发布计划默认未启用: body=%s", releaseCreated.Body.String())
	}
	releaseUpdated := performJSONRequest(t, router, http.MethodPut, "/api/v1/release-plans/"+releasePayload.ReleasePlan.ID, map[string]any{
		"description": "修改后的迭代说明",
	}, adminCookie)
	if releaseUpdated.Code != http.StatusOK || !strings.Contains(releaseUpdated.Body.String(), `"description":"修改后的迭代说明"`) ||
		!strings.Contains(releaseUpdated.Body.String(), `"version":"2026.07"`) || !strings.Contains(releaseUpdated.Body.String(), `"status":"draft"`) {
		t.Fatalf("发布计划说明更新没有保留内部标识和生命周期: status=%d body=%s", releaseUpdated.Code, releaseUpdated.Body.String())
	}
	releaseDisabled := performJSONRequest(t, router, http.MethodPatch, "/api/v1/release-plans/"+releasePayload.ReleasePlan.ID+"/status", map[string]any{
		"active": false,
	}, adminCookie)
	if releaseDisabled.Code != http.StatusOK || !strings.Contains(releaseDisabled.Body.String(), `"is_active":false`) ||
		!strings.Contains(releaseDisabled.Body.String(), `"status":"draft"`) {
		t.Fatalf("停用发布计划不应改写生命周期状态: status=%d body=%s", releaseDisabled.Code, releaseDisabled.Body.String())
	}
	releases := performJSONRequest(t, router, http.MethodGet, "/api/v1/release-plans", nil, adminCookie)
	if releases.Code != http.StatusOK || !strings.Contains(releases.Body.String(), releasePayload.ReleasePlan.ID) || strings.Contains(releases.Body.String(), createdPayload.DeploymentPlan.ID) {
		t.Fatalf("发布计划和部署方案没有正确分离: status=%d body=%s", releases.Code, releases.Body.String())
	}
}

func TestApplicationRequestAcceptsMultipleEnvironments(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	payload := `{
		"name":"流水线界面验收",
		"repository_id":"637a764b-e79e-41a2-8dd4-cc038479ebee",
		"poll_interval_seconds":60,
		"environments":[
			{"key":"test","name":"测试环境","branch":"test","watch_push":true,"watch_pull_request":true,"tag_pattern":"v*","sort_order":0},
			{"key":"prod","name":"生产环境","branch":"release","watch_tags":true,"tag_pattern":"v*","sort_order":1}
		]
	}`
	context.Request = httptest.NewRequest("POST", "/api/v1/applications", strings.NewReader(payload))
	context.Request.Header.Set("Content-Type", "application/json")
	var request applicationRequest
	if err := context.ShouldBindJSON(&request); err != nil {
		t.Fatalf("多环境应用请求不应被拒绝: %v", err)
	}
	if len(request.Environments) != 2 || request.Environments[0].Key != "test" || request.Environments[1].Key != "prod" {
		t.Fatalf("多环境应用请求解析错误: %+v", request.Environments)
	}
	if !request.Environments[0].WatchPush || !request.Environments[0].WatchPullRequest || !request.Environments[1].WatchTags {
		t.Fatalf("多环境触发方式解析错误: %+v", request.Environments)
	}
}

func TestApplicationRequestAcceptsPublicWorkflowTemplate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	payload := `{
		"name":"选择公共流水线方案",
		"repository_id":"637a764b-e79e-41a2-8dd4-cc038479ebee",
		"workflow_template_id":"dd448d0b-df10-45c2-9436-42ee44817399",
		"poll_interval_seconds":60
	}`
	context.Request = httptest.NewRequest("POST", "/api/v1/applications", strings.NewReader(payload))
	context.Request.Header.Set("Content-Type", "application/json")
	var request applicationRequest
	if err := context.ShouldBindJSON(&request); err != nil {
		t.Fatalf("公共流水线方案选择不应被拒绝: %v", err)
	}
	if request.WorkflowTemplateID != "dd448d0b-df10-45c2-9436-42ee44817399" {
		t.Fatalf("公共流水线方案未正确解析: %+v", request)
	}
}

func TestApplicationRequestRestrictsPullCheckInterval(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, interval := range []string{"3", "5", "10", "60"} {
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		payload := `{"name":"Pull 检查间隔","repository_id":"637a764b-e79e-41a2-8dd4-cc038479ebee","poll_interval_seconds":` + interval + `}`
		context.Request = httptest.NewRequest("POST", "/api/v1/applications", strings.NewReader(payload))
		context.Request.Header.Set("Content-Type", "application/json")
		var request applicationRequest
		if err := context.ShouldBindJSON(&request); err != nil {
			t.Fatalf("Pull 检查间隔 %s 秒不应被拒绝: %v", interval, err)
		}
	}

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("POST", "/api/v1/applications", strings.NewReader(`{"name":"无效间隔","repository_id":"637a764b-e79e-41a2-8dd4-cc038479ebee","poll_interval_seconds":30}`))
	context.Request.Header.Set("Content-Type", "application/json")
	var request applicationRequest
	if err := context.ShouldBindJSON(&request); err == nil {
		t.Fatal("未拒绝设置项之外的 Pull 检查间隔")
	}
}
