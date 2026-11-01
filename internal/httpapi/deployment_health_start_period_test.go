package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"edo/internal/model"
)

func TestDeploymentPlanHealthStartPeriodPreservedOnCreateAndUpdate(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if login.Code != http.StatusOK || len(login.Result().Cookies()) == 0 {
		t.Fatal("管理员登录失败")
	}
	cookie := login.Result().Cookies()[0]
	payload := func(seconds int) map[string]any {
		return map[string]any{
			"name": "零启动宽限方案", "kind": "docker", "reusable": true, "timeout_seconds": 600,
			"docker_config": model.DockerContainerConfig{HealthCheck: model.DockerHealthCheck{
				Enabled: true, Command: []string{"CMD-SHELL", "true"}, IntervalSeconds: 30,
				TimeoutSeconds: 5, Retries: 3, StartPeriodSeconds: seconds,
			}},
		}
	}
	var id string
	for index, seconds := range []int{0, 10, 0} {
		method, path, want := http.MethodPost, "/api/v1/deployment-plans", http.StatusCreated
		if index > 0 {
			method, path, want = http.MethodPut, path+"/"+id, http.StatusOK
		}
		response := performJSONRequest(t, router, method, path, payload(seconds), cookie)
		var body struct {
			Plan model.DeploymentPlan `json:"deployment_plan"`
		}
		if response.Code != want || json.Unmarshal(response.Body.Bytes(), &body) != nil {
			t.Fatalf("保存方案失败: method=%s status=%d", method, response.Code)
		}
		if body.Plan.ID == "" || (id != "" && body.Plan.ID != id) || body.Plan.DeploymentTargetID != "" {
			t.Fatal("可复用方案的身份或运行目标发生变化")
		}
		id = body.Plan.ID
		if body.Plan.DockerConfig.HealthCheck.StartPeriodSeconds != seconds {
			t.Fatalf("保存响应改写启动宽限: got=%d want=%d", body.Plan.DockerConfig.HealthCheck.StartPeriodSeconds, seconds)
		}
		listed := performJSONRequest(t, router, http.MethodGet, "/api/v1/deployment-plans", nil, cookie)
		var list struct {
			Plans []model.DeploymentPlan `json:"deployment_plans"`
		}
		if listed.Code != http.StatusOK || json.Unmarshal(listed.Body.Bytes(), &list) != nil {
			t.Fatal("回读方案列表失败")
		}
		found := false
		for _, plan := range list.Plans {
			if plan.ID == id {
				found = true
				if plan.DockerConfig.HealthCheck.StartPeriodSeconds != seconds {
					t.Fatalf("数据库回读改写启动宽限: got=%d want=%d", plan.DockerConfig.HealthCheck.StartPeriodSeconds, seconds)
				}
			}
		}
		if !found {
			t.Fatal("正常列表未返回已保存方案")
		}
	}
}
