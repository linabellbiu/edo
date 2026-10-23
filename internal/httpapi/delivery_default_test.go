package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"edo/internal/model"
	"edo/internal/pipeline"
	"gorm.io/gorm"
)

func TestDeliveryConfigurationHTTPAndPermissions(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	defer cleanup()
	cookie := loginForDepartmentTest(t, router, "admin")
	missingProjectRequest := httptest.NewRequest(http.MethodGet, "/api/v1/delivery-defaults", nil)
	missingProjectRequest.AddCookie(cookie)
	missingProjectResponse := httptest.NewRecorder()
	router.ServeHTTP(missingProjectResponse, missingProjectRequest)
	if missingProjectResponse.Code != http.StatusBadRequest {
		t.Fatalf("未选项目不得读取默认项: %d", missingProjectResponse.Code)
	}

	defaults := performJSONRequest(t, router, http.MethodGet, "/api/v1/delivery-defaults", nil, cookie)
	if defaults.Code != 200 {
		t.Fatalf("读取默认失败: %d %s", defaults.Code, defaults.Body.String())
	}
	var defaultPayload struct {
		Defaults []pipeline.DeliveryDefaultView `json:"defaults"`
	}
	if err := json.Unmarshal(defaults.Body.Bytes(), &defaultPayload); err != nil || len(defaultPayload.Defaults) != 4 {
		t.Fatalf("四种默认不完整: %v %s", err, defaults.Body.String())
	}
	plan := performJSONRequest(t, router, http.MethodPost, "/api/v1/deployment-plans", map[string]any{"name": "通用主机脚本", "kind": "script", "script": "true", "reusable": true, "timeout_seconds": 120}, cookie)
	var planPayload struct {
		DeploymentPlan model.DeploymentPlan `json:"deployment_plan"`
	}
	if plan.Code != 201 || json.Unmarshal(plan.Body.Bytes(), &planPayload) != nil || planPayload.DeploymentPlan.DeploymentTargetID != "" {
		t.Fatalf("通用方案创建失败: %d %s", plan.Code, plan.Body.String())
	}
	change := map[string]any{"workflow_template_id": "", "deployment_plan_id": planPayload.DeploymentPlan.ID, "expected_version": 0}
	saved := performJSONRequest(t, router, http.MethodPut, "/api/v1/delivery-defaults/script", change, cookie)
	if saved.Code != 200 {
		t.Fatalf("保存默认失败: %d %s", saved.Code, saved.Body.String())
	}
	stale := performJSONRequest(t, router, http.MethodPut, "/api/v1/delivery-defaults/script", change, cookie)
	if stale.Code != 409 {
		t.Fatalf("过期版本未拒绝: %d %s", stale.Code, stale.Body.String())
	}
	repoID := createRepositoryForDepartmentTest(t, router, cookie, "统一发布仓库")
	app := performJSONRequest(t, router, http.MethodPost, "/api/v1/applications", map[string]any{"name": "unified_delivery", "repository_id": repoID, "empty_workflow": true}, cookie)
	var appPayload struct {
		Application model.Application `json:"application"`
	}
	if app.Code != 201 || json.Unmarshal(app.Body.Bytes(), &appPayload) != nil || len(appPayload.Application.Workflows) != 0 {
		t.Fatalf("新应用不应隐式创建流水线: %d %s", app.Code, app.Body.String())
	}
	build := performJSONRequest(t, router, http.MethodPost, "/api/v1/build-plans", map[string]any{"name": "文件构建", "kind": "script", "script": "printf app > app.tar", "artifact_path": "app.tar"}, cookie)
	var buildPayload struct {
		BuildPlan model.BuildPlan `json:"build_plan"`
	}
	if build.Code != 201 || json.Unmarshal(build.Body.Bytes(), &buildPayload) != nil {
		t.Fatalf("构建创建失败: %d %s", build.Code, build.Body.String())
	}
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/applications/"+appPayload.Application.ID+"/delivery-workflows", map[string]any{"kind": "script", "build_plan_id": buildPayload.BuildPlan.ID, "expected_default_version": 1, "target": map[string]any{"name": "新位置", "platform": "ssh", "environment_id": "httpapi-deployment-environment", "host_id": "httpapi-deployment-host"}}, cookie)
	var workflow pipeline.WorkflowResult
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &workflow) != nil || workflow.Workflow == nil || workflow.Workflow.IsActive || !workflow.Valid {
		t.Fatalf("统一发布配置失败: %d %s", created.Code, created.Body.String())
	}
	for _, value := range []any{&model.PipelineRun{}, &model.DeploymentRecord{}, &model.Job{}} {
		var count int64
		db.Model(value).Count(&count)
		if count != 0 {
			t.Fatal("配置创建了发布任务")
		}
	}
	roleID := createRoleForHTTPTest(t, router, cookie, "delivery-readonly", []string{"application.read", "workflow.read", "deployment_plan.read"})
	createUserForHTTPTest(t, router, cookie, "delivery-reader", appPayload.Application.DepartmentID, []string{roleID})
	reader := loginForDepartmentTest(t, router, "delivery-reader")
	if response := performJSONRequest(t, router, http.MethodGet, "/api/v1/delivery-defaults", nil, reader); response.Code != 200 {
		t.Fatalf("只读应可查看默认: %d %s", response.Code, response.Body.String())
	}
	for _, path := range []string{"/api/v1/delivery-defaults/docker", "/api/v1/applications/" + appPayload.Application.ID + "/delivery-workflows"} {
		method := http.MethodPut
		if path != "/api/v1/delivery-defaults/docker" {
			method = http.MethodPost
		}
		if response := performJSONRequest(t, router, method, path, map[string]any{}, reader); response.Code != 403 {
			t.Fatalf("只读应禁止写入: %s %d %s", path, response.Code, response.Body.String())
		}
	}
	if response := performJSONRequest(t, router, http.MethodPost, "/api/v1/deployments", map[string]any{"target_id": "anything", "image": "image:tag"}, cookie); response.Code != 404 {
		t.Fatalf("不得开放绕过流水线发布入口: %d", response.Code)
	}
}
