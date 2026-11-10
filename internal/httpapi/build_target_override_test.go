package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"edo/internal/model"
	"edo/internal/pipeline"
)

func TestWorkflowBuildTargetOverrideHTTP(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/build-plans", map[string]any{"name": "任务覆盖的公共方案", "kind": "dockerfile", "dockerfile_path": "Dockerfile", "target_stage": "runtime"}, admin)
	var build struct {
		Plan model.BuildPlan `json:"build_plan"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &build) != nil {
		t.Fatalf("方案创建失败: %s", created.Body.String())
	}
	payload := map[string]any{"schema_version": 1, "name": "阶段覆盖测试", "source": map[string]any{"id": "source", "type": "trigger", "name": "代码源", "config": map[string]any{"events": []string{"manual"}, "branch": "main"}}, "stages": []any{map[string]any{"id": "build", "name": "构建", "tasks": []any{map[string]any{"id": "image", "name": "镜像构建", "type": "build", "config": map[string]any{"build_plan_id": build.Plan.ID}}}}}}
	workflow := performJSONRequest(t, router, http.MethodPost, "/api/v1/workflow-templates", payload, admin)
	var result pipeline.WorkflowTemplateResult
	if workflow.Code != http.StatusCreated || json.Unmarshal(workflow.Body.Bytes(), &result) != nil {
		t.Fatalf("流水线创建失败: %d %s", workflow.Code, workflow.Body.String())
	}
	path := "/api/v1/workflow-templates/" + result.WorkflowTemplate.ID
	for _, target := range []string{"builder", "", "web,api"} {
		payload["revision"] = result.WorkflowTemplate.Revision
		config := payload["stages"].([]any)[0].(map[string]any)["tasks"].([]any)[0].(map[string]any)["config"].(map[string]any)
		config["target_stage"] = target
		config["build_platform"] = "linux/arm64"
		saved := performJSONRequest(t, router, http.MethodPut, path, payload, admin)
		if target == "web,api" {
			if saved.Code != http.StatusBadRequest {
				t.Fatalf("无效覆盖应拒绝: %d %s", saved.Code, saved.Body.String())
			}
			continue
		}
		if saved.Code != http.StatusOK || json.Unmarshal(saved.Body.Bytes(), &result) != nil {
			t.Fatalf("覆盖保存失败: %d %s", saved.Code, saved.Body.String())
		}
		value := result.WorkflowTemplate.Stages[0].Tasks[0].Config.TargetStage
		if result.WorkflowTemplate.Stages[0].Tasks[0].Config.BuildPlatform != "linux/arm64" {
			t.Fatal("任务构建平台未保存")
		}
		if value == nil || *value != target {
			t.Fatalf("接口丢失任务覆盖值: %s", saved.Body.String())
		}
	}
	config := payload["stages"].([]any)[0].(map[string]any)["tasks"].([]any)[0].(map[string]any)["config"].(map[string]any)
	config["target_stage"] = ""
	config["build_platform"] = "linux/386"
	payload["revision"] = result.WorkflowTemplate.Revision
	rejected := performJSONRequest(t, router, http.MethodPut, path, payload, admin)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("非法平台未拒绝: %d %s", rejected.Code, rejected.Body.String())
	}
	reopened := performJSONRequest(t, router, http.MethodGet, path, nil, admin)
	if reopened.Code != http.StatusOK || json.Unmarshal(reopened.Body.Bytes(), &result) != nil {
		t.Fatal("重新读取失败")
	}
	value := result.WorkflowTemplate.Stages[0].Tasks[0].Config.TargetStage
	if result.WorkflowTemplate.Stages[0].Tasks[0].Config.BuildPlatform != "linux/arm64" {
		t.Fatal("非法请求改变已保存的平台")
	}
	if value == nil || *value != "" {
		t.Fatal("非法保存改变了已保存的明确空值")
	}
	plans := performJSONRequest(t, router, http.MethodGet, "/api/v1/build-plans", nil, admin)
	var listed struct {
		Plans []model.BuildPlan `json:"build_plans"`
	}
	if plans.Code != http.StatusOK || json.Unmarshal(plans.Body.Bytes(), &listed) != nil || len(listed.Plans) != 1 || listed.Plans[0].TargetStage != "runtime" {
		t.Fatal("任务覆盖修改了公共构建方案")
	}
}
