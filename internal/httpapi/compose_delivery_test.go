package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"gorm.io/gorm"

	"edo/internal/model"
	"edo/internal/pipeline"
)

func TestComposeDeliveryHTTPUsesNormalPermissionsAndCreatesOnlyDraft(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	registry := model.ImageRegistry{ID: "compose-http-registry", Name: "通用接口镜像仓库", Provider: model.RegistryGeneric, Endpoint: "https://registry.example.com", IsActive: true}
	if err := db.Create(&registry).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.HostCapability{HostID: "httpapi-deployment-host", Kind: model.HostCapabilityDocker, Status: model.HostCapabilityReady}).Error; err != nil {
		t.Fatal(err)
	}
	build := performJSONRequest(t, router, http.MethodPost, "/api/v1/build-plans", map[string]any{"name": "接口镜像构建", "kind": "dockerfile", "dockerfile_path": "Dockerfile", "image_registry_id": registry.ID}, admin)
	var buildBody struct {
		Build model.BuildPlan `json:"build_plan"`
	}
	if build.Code != 201 || json.Unmarshal(build.Body.Bytes(), &buildBody) != nil {
		t.Fatalf("构建方案创建失败: %d", build.Code)
	}
	repo := createRepositoryForDepartmentTest(t, router, admin, "通用 Compose 仓库")
	app := performJSONRequest(t, router, http.MethodPost, "/api/v1/applications", map[string]any{"name": "compose_http", "repository_id": repo, "empty_workflow": true}, admin)
	var appBody struct {
		App model.Application `json:"application"`
	}
	if app.Code != 201 || json.Unmarshal(app.Body.Bytes(), &appBody) != nil {
		t.Fatal("应用创建失败")
	}
	path := "/api/v1/applications/" + appBody.App.ID + "/compose-delivery-workflows"
	body := map[string]any{"compose_yaml": "services:\n  web:\n    image: registry.example.com/web:${EDO_IMAGE_TAG}\n", "services": []map[string]string{{"service": "web", "build_plan_id": buildBody.Build.ID}}, "target": map[string]string{"name": "通用位置", "platform": "ssh", "environment_id": "httpapi-deployment-environment", "host_id": "httpapi-deployment-host", "working_directory": "/srv/custom"}}
	created := performJSONRequest(t, router, http.MethodPost, path, body, admin)
	var result pipeline.WorkflowResult
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &result) != nil || result.Valid || result.Workflow.IsActive || len(result.Issues) != 1 || result.Issues[0].Code != "missing_deployment_registry" {
		t.Fatalf("未生成待显式选择部署仓库的草稿: %d %s", created.Code, created.Body.String())
	}
	if settings := result.Workflow.Stages[1].Tasks[0].Config.Compose; settings == nil || settings.ProjectName != "" {
		t.Fatal("草稿不应保存用户指定的项目名或伪造运行解析结果")
	}
	role := createRoleForHTTPTest(t, router, admin, "compose-no-build-create", []string{"application.update", "workflow.read", "deployment_plan.create", "deployment_plan.read", "build_plan.read", "cluster.read", "deployment.read"})
	createUserForHTTPTest(t, router, admin, "compose_config_reader", appBody.App.DepartmentID, []string{role})
	reader := loginForDepartmentTest(t, router, "compose_config_reader")
	denied := performJSONRequest(t, router, http.MethodPost, path, body, reader)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("未要求正常流水线创建权限: %d", denied.Code)
	}
	for _, value := range []any{&model.PipelineRun{}, &model.DeploymentRecord{}, &model.Job{}} {
		var count int64
		db.Model(value).Count(&count)
		if count != 0 {
			t.Fatal("配置接口触发了任务")
		}
	}
}
