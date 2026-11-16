package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/k3s"
	"edo/internal/model"
)

func k3dHTTPInput(name string) map[string]any {
	return map[string]any{"action": "create", "mode": "k3d", "profile": "default", "name": name, "server_host_id": "httpapi-deployment-host", "k3d": map[string]any{"image_source": "dockerhub", "image": "docker.io/rancher/k3s:v1.37.1-k3s1", "agents": 0, "api_port": 16443, "bind_address": "192.0.2.241"}}
}

func TestK3dHTTPPreviewMetadataSingleConsumptionAndOwnerBoundary(t *testing.T) {
	transport := &k3sHTTPTestTransport{}
	router, cleanup := newK3sHTTPTestRouter(t, transport)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	input := k3dHTTPInput("k3d HTTP")
	input["k3d"].(map[string]any)["server_resources"] = map[string]any{"cpu_millis": 1750, "memory_mib": 3072}
	input["k3d"].(map[string]any)["worker_resources"] = map[string]any{"cpu_millis": 625, "memory_mib": 768}
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/plans", input, admin)
	var plan k3s.Plan
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &plan) != nil || plan.Mode != "k3d" || plan.K3d == nil || plan.K3d.Agents == nil || *plan.K3d.Agents != 0 || plan.K3d.APIPort != 16443 || plan.K3d.BindAddress != "192.0.2.241" || plan.Version != "v1.37.1+k3s1" || len(plan.Hosts) != 1 || plan.Hosts[0].Role != "host" {
		t.Fatalf("k3d 预览元数据错误: %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || plan.Preflight == nil || !plan.Preflight.Passed || len(plan.Commands) != 1 || !strings.Contains(plan.Commands[0], "--agents 0") || strings.Contains(response.Body.String(), "test-only-ssh-password") {
		t.Fatal("k3d 计划未保持只读、安全或显式零 Worker")
	}
	if plan.K3d.ServerResources == nil || *plan.K3d.ServerResources != (k3s.K3dNodeResources{CPUMillis: 1750, MemoryMiB: 3072}) || plan.K3d.WorkerResources == nil || *plan.K3d.WorkerResources != (k3s.K3dNodeResources{CPUMillis: 625, MemoryMiB: 768}) || len(plan.Warnings) != 0 {
		t.Fatal("HTTP 计划资源配置未冻结或仍返回已删除说明")
	}
	role := createRoleForHTTPTest(t, router, admin, "k3d-other-owner", []string{access.PermissionClusterRead, access.PermissionClusterCreate, access.PermissionClusterUpdate, access.PermissionClusterExecute})
	createUserForHTTPTest(t, router, admin, "k3d-other-owner", "", []string{role})
	other := loginForDepartmentTest(t, router, "k3d-other-owner")
	for _, request := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/kubernetes/k3s/installations/" + plan.ID, nil},
		{http.MethodPost, "/api/v1/kubernetes/k3s/installations", map[string]any{"plan_id": plan.ID}},
	} {
		result := performJSONRequest(t, router, request.method, request.path, request.body, other)
		if result.Code != http.StatusNotFound {
			t.Fatalf("他人可读取或执行计划: %d %s", result.Code, result.Body.String())
		}
	}
	queued := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/installations", map[string]any{"plan_id": plan.ID}, admin)
	var installation k3s.Installation
	if queued.Code != http.StatusAccepted || json.Unmarshal(queued.Body.Bytes(), &installation) != nil || installation.Mode != "k3d" || installation.K3d == nil || installation.Status != "pending" {
		t.Fatalf("k3d 未异步排队: %d %s", queued.Code, queued.Body.String())
	}
	if installation.K3d.ServerResources == nil || *installation.K3d.ServerResources != *plan.K3d.ServerResources || installation.K3d.WorkerResources == nil || *installation.K3d.WorkerResources != *plan.K3d.WorkerResources {
		t.Fatal("已排队任务没有沿用已确认的资源配置")
	}
	repeated := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/installations", map[string]any{"plan_id": plan.ID}, admin)
	if repeated.Code != http.StatusConflict || !strings.Contains(repeated.Body.String(), "k3s_plan_consumed") {
		t.Fatalf("计划可重复消费: %d %s", repeated.Code, repeated.Body.String())
	}
	detail := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/installations/"+plan.ID, nil, admin)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"mode":"k3d"`) || transport.runs.Load() != 0 {
		t.Fatalf("详情丢失模式或 HTTP 执行了远端安装: %d %s", detail.Code, detail.Body.String())
	}
}

func TestK3dHTTPRequiresEveryInstallationPermissionBeforeSSH(t *testing.T) {
	for _, missing := range []string{access.PermissionClusterRead, access.PermissionClusterCreate, access.PermissionClusterUpdate, access.PermissionClusterExecute} {
		t.Run(missing, func(t *testing.T) {
			transport := &k3sHTTPTestTransport{}
			router, cleanup := newK3sHTTPTestRouter(t, transport)
			defer cleanup()
			admin := loginForDepartmentTest(t, router, "admin")
			var permissions []string
			for _, permission := range []string{access.PermissionClusterRead, access.PermissionClusterCreate, access.PermissionClusterUpdate, access.PermissionClusterExecute} {
				if permission != missing {
					permissions = append(permissions, permission)
				}
			}
			role := createRoleForHTTPTest(t, router, admin, "k3d-missing-permission", permissions)
			createUserForHTTPTest(t, router, admin, "k3d-permission-user", "", []string{role})
			actor := loginForDepartmentTest(t, router, "k3d-permission-user")
			response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/plans", k3dHTTPInput("k3d forbidden"), actor)
			if response.Code != http.StatusForbidden || transport.reads.Load() != 0 || transport.runs.Load() != 0 {
				t.Fatalf("缺失 %s 仍执行 SSH: %d %s", missing, response.Code, response.Body.String())
			}
			versions := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/k3d/images?source=invalid", nil, actor)
			if versions.Code != http.StatusForbidden {
				t.Fatalf("镜像版本目录绕过动作权限: %d %s", versions.Code, versions.Body.String())
			}
		})
	}
}

func TestK3dHTTPImageCatalogRejectsArbitrarySourcesWithoutSSH(t *testing.T) {
	transport := &k3sHTTPTestTransport{}
	router, cleanup := newK3sHTTPTestRouter(t, transport)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	result := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/k3d/images?source=https%3A%2F%2Fprivate.invalid", nil, admin)
	if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), "请选择国内源或 Docker Hub") || strings.Contains(result.Body.String(), "private.invalid") || transport.reads.Load() != 0 || transport.runs.Load() != 0 {
		t.Fatalf("版本读取接受任意来源或产生主机副作用: %d %s", result.Code, result.Body.String())
	}
}

func TestK3dHTTPRejectsInvalidOrCrossProjectHostsBeforeSSH(t *testing.T) {
	for _, scenario := range []string{"native-options", "extra-host", "join", "wildcard", "invalid-server-resources", "invalid-worker-resources", "fractional-cpu-millis", "cross-project", "preflight-failed"} {
		t.Run(scenario, func(t *testing.T) {
			transport := &k3sHTTPTestTransport{}
			router, cleanup, db := newK3sHTTPTestRouterAndDB(t, transport)
			defer cleanup()
			admin := loginForDepartmentTest(t, router, "admin")
			input := k3dHTTPInput("k3d invalid")
			want := http.StatusBadRequest
			switch scenario {
			case "native-options":
				input["server_options"] = map[string]any{"docker": true}
			case "extra-host":
				input["agent_host_ids"] = []string{"other"}
			case "join":
				input["action"] = "join"
			case "wildcard":
				input["k3d"] = map[string]any{"bind_address": "0.0.0.0"}
			case "invalid-server-resources":
				input["k3d"].(map[string]any)["server_resources"] = map[string]any{"cpu_millis": 0, "memory_mib": 1024}
			case "invalid-worker-resources":
				input["k3d"].(map[string]any)["worker_resources"] = map[string]any{"cpu_millis": 1000, "memory_mib": 5}
			case "fractional-cpu-millis":
				input["k3d"].(map[string]any)["server_resources"] = map[string]any{"cpu_millis": 10.5, "memory_mib": 1024}
			case "cross-project":
				other := model.Project{ID: "k3d-other-project", DepartmentID: database.DefaultDepartmentID, Name: "k3d other", IsActive: true}
				if err := db.Create(&other).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&model.Host{}).Where(&model.Host{ID: "httpapi-deployment-host"}).Update("project_id", other.ID).Error; err != nil {
					t.Fatal(err)
				}
				want = http.StatusConflict
			case "preflight-failed":
				transport.statuses = map[string]string{"docker": "failed"}
				want = http.StatusConflict
			}
			before := k3sHTTPPreflightState(t, db)
			response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/plans", input, admin)
			if response.Code != want || transport.runs.Load() != 0 {
				t.Fatalf("无效请求结果错误: %d %s", response.Code, response.Body.String())
			}
			if scenario != "preflight-failed" && transport.reads.Load() != 0 {
				t.Fatal("无效请求执行了 SSH")
			}
			if scenario == "preflight-failed" && !strings.Contains(response.Body.String(), `"role":"host"`) {
				t.Fatal("失败报告没有保留安装宿主角色")
			}
			for name, count := range k3sHTTPPreflightState(t, db) {
				if count != before[name] {
					t.Fatalf("失败请求创建 %s", name)
				}
			}
		})
	}
}
