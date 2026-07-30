package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestDeploymentPlanLifecycleEndpoints(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	adminCookie := login.Result().Cookies()[0]
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/deployment-plans", deploymentPlanLifecyclePayload("接口生命周期方案"), adminCookie)
	var createdPayload struct {
		DeploymentPlan struct {
			ID string `json:"id"`
		} `json:"deployment_plan"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &createdPayload) != nil || createdPayload.DeploymentPlan.ID == "" {
		t.Fatalf("创建部署方案失败: status=%d body=%s", created.Code, created.Body.String())
	}
	planPath := "/api/v1/deployment-plans/" + createdPayload.DeploymentPlan.ID
	invalid := performJSONRequest(t, router, http.MethodPatch, planPath+"/status", map[string]any{"active": "no"}, adminCookie)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("无效状态未被拒绝: status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	disabled := performJSONRequest(t, router, http.MethodPatch, planPath+"/status", map[string]any{"active": false}, adminCookie)
	if disabled.Code != http.StatusNoContent {
		t.Fatalf("停用部署方案失败: status=%d body=%s", disabled.Code, disabled.Body.String())
	}
	listed := performJSONRequest(t, router, http.MethodGet, "/api/v1/deployment-plans", nil, adminCookie)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), createdPayload.DeploymentPlan.ID) ||
		!strings.Contains(listed.Body.String(), `"is_active":false`) {
		t.Fatalf("列表未返回已停用方案: status=%d body=%s", listed.Code, listed.Body.String())
	}
	deleted := performJSONRequest(t, router, http.MethodDelete, planPath, nil, adminCookie)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("删除部署方案失败: status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	listed = performJSONRequest(t, router, http.MethodGet, "/api/v1/deployment-plans", nil, adminCookie)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), createdPayload.DeploymentPlan.ID) {
		t.Fatalf("软删除方案仍出现在列表: status=%d body=%s", listed.Code, listed.Body.String())
	}
	missingStatus := performJSONRequest(t, router, http.MethodPatch, planPath+"/status", map[string]any{"active": true}, adminCookie)
	if missingStatus.Code != http.StatusNotFound {
		t.Fatalf("软删除方案仍可启用: status=%d body=%s", missingStatus.Code, missingStatus.Body.String())
	}
	missingDelete := performJSONRequest(t, router, http.MethodDelete, planPath, nil, adminCookie)
	if missingDelete.Code != http.StatusNotFound {
		t.Fatalf("重复删除未返回 404: status=%d body=%s", missingDelete.Code, missingDelete.Body.String())
	}
}

func TestDeploymentPlanLifecycleRequiresManagePermission(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	adminCookie := login.Result().Cookies()[0]
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/deployment-plans", deploymentPlanLifecyclePayload("权限测试部署方案"), adminCookie)
	var createdPayload struct {
		DeploymentPlan struct {
			ID string `json:"id"`
		} `json:"deployment_plan"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &createdPayload) != nil {
		t.Fatalf("创建权限测试方案失败: status=%d body=%s", created.Code, created.Body.String())
	}
	role := performJSONRequest(t, router, http.MethodPost, "/api/v1/roles", map[string]any{
		"name": "deployment-plan-reader", "display_name": "部署方案只读", "permissions": []string{"delivery.read"},
	}, adminCookie)
	var rolePayload struct {
		Role struct {
			ID string `json:"id"`
		} `json:"role"`
	}
	if role.Code != http.StatusCreated || json.Unmarshal(role.Body.Bytes(), &rolePayload) != nil {
		t.Fatalf("创建只读角色失败: status=%d body=%s", role.Code, role.Body.String())
	}
	user := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "deployment-reader", "nickname": "部署方案只读用户",
		"password": "correct horse battery staple", "role_ids": []string{rolePayload.Role.ID},
	}, adminCookie)
	if user.Code != http.StatusCreated {
		t.Fatalf("创建只读用户失败: status=%d body=%s", user.Code, user.Body.String())
	}
	readerLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "deployment-reader", "password": "correct horse battery staple",
	}, nil)
	readerCookie := readerLogin.Result().Cookies()[0]
	planPath := "/api/v1/deployment-plans/" + createdPayload.DeploymentPlan.ID
	listed := performJSONRequest(t, router, http.MethodGet, "/api/v1/deployment-plans", nil, readerCookie)
	if listed.Code != http.StatusOK {
		t.Fatalf("具有 delivery.read 的用户无法查看方案: status=%d body=%s", listed.Code, listed.Body.String())
	}
	status := performJSONRequest(t, router, http.MethodPatch, planPath+"/status", map[string]any{"active": false}, readerCookie)
	if status.Code != http.StatusForbidden {
		t.Fatalf("只读用户可以修改部署方案状态: status=%d body=%s", status.Code, status.Body.String())
	}
	deleted := performJSONRequest(t, router, http.MethodDelete, planPath, nil, readerCookie)
	if deleted.Code != http.StatusForbidden {
		t.Fatalf("只读用户可以删除部署方案: status=%d body=%s", deleted.Code, deleted.Body.String())
	}
}

func deploymentPlanLifecyclePayload(name string) map[string]any {
	return map[string]any{
		"name": name, "kind": "script", "script": "set -eu\necho deploy\n", "timeout_seconds": 120,
		"deployment_target": map[string]any{
			"name": name + "位置", "platform": "ssh", "environment_id": "httpapi-deployment-environment",
			"host_id": "httpapi-deployment-host", "working_directory": "/srv/app", "rollout_timeout": 120,
		},
	}
}
