package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/k3s"
	"edo/internal/model"
)

func TestK3sHTTPStandaloneCheckReturnsReportWithoutCreatingInstallation(t *testing.T) {
	for _, scenario := range []struct {
		name, mode, failed string
	}{
		{"原生满足已检查条件", "native", ""},
		{"原生不满足安装条件", "native", "snapshotter"},
		{"容器模式满足已检查条件", "k3d", ""},
		{"容器模式不满足安装条件", "k3d", "docker"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			transport := &k3sHTTPTestTransport{statuses: map[string]string{}, extraOutput: "private-remote-output password=private-remote-password\n"}
			if scenario.failed != "" {
				transport.statuses[scenario.failed] = "failed"
			}
			router, cleanup, db := newK3sHTTPTestRouterAndDB(t, transport)
			defer cleanup()
			admin := loginForDepartmentTest(t, router, "admin")
			before := k3sHTTPPreflightState(t, db)
			input := map[string]any{"action": "create", "mode": "native", "profile": "default", "name": "安装前检测", "server_host_id": "httpapi-deployment-host"}
			if scenario.mode == "k3d" {
				input = k3dHTTPInput("容器安装前检测")
			}
			response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/preflight", input, admin)
			var report k3s.PreflightReport
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &report) != nil || report.CheckedAt.IsZero() || len(report.Hosts) != 1 || report.Passed != (scenario.failed == "") {
				t.Fatalf("检测没有返回准确报告: status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" || transport.reads.Load() != 1 || transport.runs.Load() != 0 {
				t.Fatal("检测必须只读且不可缓存")
			}
			for name, count := range k3sHTTPPreflightState(t, db) {
				if count != before[name] {
					t.Fatalf("独立检测创建了%s", name)
				}
			}
			var body map[string]any
			_ = json.Unmarshal(response.Body.Bytes(), &body)
			for _, key := range []string{"id", "expires_at", "commands", "server_config", "agent_config", "server_token"} {
				if _, exists := body[key]; exists {
					t.Fatalf("检测返回了安装计划字段%s", key)
				}
			}
			for _, private := range []string{"private-remote-output", "private-remote-password", "test-only-ssh-password", "11223344556677889900aabbccddeeff"} {
				if strings.Contains(response.Body.String(), private) {
					t.Fatal("检测响应泄露远端输出或凭据")
				}
			}
		})
	}
}

func TestK3sHTTPStandaloneCheckRequiresPermissionsBeforeSSH(t *testing.T) {
	for _, missing := range []string{access.PermissionClusterRead, access.PermissionClusterCreate, access.PermissionClusterUpdate, access.PermissionClusterExecute} {
		t.Run(missing, func(t *testing.T) {
			transport := &k3sHTTPTestTransport{}
			router, cleanup, db := newK3sHTTPTestRouterAndDB(t, transport)
			defer cleanup()
			admin := loginForDepartmentTest(t, router, "admin")
			var permissions []string
			for _, permission := range []string{access.PermissionClusterRead, access.PermissionClusterCreate, access.PermissionClusterUpdate, access.PermissionClusterExecute} {
				if permission != missing {
					permissions = append(permissions, permission)
				}
			}
			role := createRoleForHTTPTest(t, router, admin, "preflight-check-limited", permissions)
			createUserForHTTPTest(t, router, admin, "preflight-check-limited", "", []string{role})
			actor := loginForDepartmentTest(t, router, "preflight-check-limited")
			before := k3sHTTPPreflightState(t, db)
			response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/preflight", k3dHTTPInput("权限检测"), actor)
			if response.Code != http.StatusForbidden || transport.reads.Load() != 0 || transport.runs.Load() != 0 {
				t.Fatalf("权限不足仍连接了主机: %d", response.Code)
			}
			for name, count := range k3sHTTPPreflightState(t, db) {
				if count != before[name] {
					t.Fatalf("拒绝的检测创建了%s", name)
				}
			}
		})
	}
}

func TestK3sHTTPStandaloneCheckRejectsOtherProjectHostWithoutProbe(t *testing.T) {
	transport := &k3sHTTPTestTransport{}
	router, cleanup, db := newK3sHTTPTestRouterAndDB(t, transport)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	other := model.Project{ID: "check-other-project", DepartmentID: database.DefaultDepartmentID, Name: "另一个检测项目", IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Host{}).Where("uuid = ?", "httpapi-deployment-host").Update("project_id", other.ID).Error; err != nil {
		t.Fatal(err)
	}
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/preflight", k3dHTTPInput("项目检测"), admin)
	if response.Code != http.StatusConflict || transport.reads.Load() != 0 || transport.runs.Load() != 0 {
		t.Fatalf("跨项目主机被检测: %d", response.Code)
	}
}

func TestK3sHTTPStandaloneCheckKeepsRealServerDeadline(t *testing.T) {
	transport := &k3sHTTPTestTransport{delay: 100 * time.Millisecond}
	router, cleanup := newK3sHTTPTestRouter(t, transport)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	server := httptest.NewUnstartedServer(router)
	server.Config.WriteTimeout = 50 * time.Millisecond
	server.Start()
	defer server.Close()
	payload, _ := json.Marshal(k3dHTTPInput("检测响应时限"))
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/kubernetes/k3s/preflight", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-EDO-Project-ID", database.DefaultProjectID(database.DefaultDepartmentID))
	request.AddCookie(admin)
	client := server.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("普通 HTTP 写时限不应截断安装前检测", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	var report k3s.PreflightReport
	if err != nil || response.StatusCode != http.StatusOK || json.Unmarshal(body, &report) != nil || !report.Passed {
		t.Fatalf("检测报告未完整返回: %d %s", response.StatusCode, body)
	}
}
