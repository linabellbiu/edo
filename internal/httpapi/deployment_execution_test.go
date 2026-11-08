package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"context"
	"edo/internal/model"
	"edo/internal/pipeline"
)

func TestDeploymentPlanExecutionConfigurationHTTP(t *testing.T) {
	f := newReleasePlanExecutionHTTPFixture(t, "0123456789abcdef0123456789abcdef01234567")
	defer f.close()
	cookie := loginForDepartmentTest(t, f.router, "admin")
	registry, err := f.pipelines.CreateRegistry(context.Background(), "admin", pipeline.RegistryInput{Name: "保存的部署仓库", Provider: model.RegistryGeneric, Endpoint: "https://registry.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"name": "方案执行配置 HTTP", "kind": "docker", "reusable": true, "timeout_seconds": 600, "execution_config": map[string]any{"deployment_registry_id": registry.ID, "deployment_input_version": 1, "deployment_script": "printf original", "variable_names": []string{"SETTING"}}}
	response := performJSONRequest(t, f.router, http.MethodPost, "/api/v1/deployment-plans", body, cookie)
	if response.Code != http.StatusCreated {
		t.Fatalf("创建配置失败: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Plan model.DeploymentPlan `json:"deployment_plan"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Plan.ExecutionConfig == nil || result.Plan.ExecutionConfig.DeploymentScript != "printf original" || result.Plan.ExecutionConfig.DeploymentRegistryID != registry.ID {
		t.Fatal("创建响应未保留配置", err)
	}
	path := "/api/v1/deployment-plans/" + result.Plan.ID
	for _, step := range []struct {
		value  any
		omit   bool
		script string
	}{{map[string]any{"deployment_registry_id": registry.ID, "deployment_input_version": 1, "deployment_script": "printf updated"}, false, "printf updated"}, {nil, true, "printf updated"}, {map[string]any{}, false, ""}} {
		if step.omit {
			delete(body, "execution_config")
		} else {
			body["execution_config"] = step.value
		}
		response = performJSONRequest(t, f.router, http.MethodPut, path, body, cookie)
		if response.Code != http.StatusOK {
			t.Fatalf("更新配置失败: %d %s", response.Code, response.Body.String())
		}
		var stored model.DeploymentPlan
		if err := f.db.First(&stored, "uuid = ?", result.Plan.ID).Error; err != nil || stored.ExecutionConfig == nil || stored.ExecutionConfig.DeploymentScript != step.script || step.script != "" && stored.ExecutionConfig.DeploymentRegistryID != registry.ID {
			t.Fatal("更新、旧客户端保留或显式清空语义错误", err)
		}
	}
	body["execution_config"] = map[string]any{"deployment_input_version": 1, "deployment_script": "if true; then\n"}
	response = performJSONRequest(t, f.router, http.MethodPut, path, body, cookie)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("无效 Shell 方案未拒绝: %d", response.Code)
	}
}
