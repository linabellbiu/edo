package dockerengine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInspectDiagnosticContainerVerifiesOwnershipAndOmitsSecrets(t *testing.T) {
	for _, test := range []struct {
		name, service string
		labels        map[string]string
		valid         bool
	}{
		{name: "托管实例", labels: map[string]string{"edo.managed": "true", "edo.deployment.target.id": "target", "edo.deployment.id": "deployment"}, valid: true},
		{name: "其他部署", labels: map[string]string{"edo.managed": "true", "edo.deployment.target.id": "target", "edo.deployment.id": "other"}},
		{name: "外部容器", labels: map[string]string{}},
		{name: "Compose服务", service: "api", labels: map[string]string{"com.docker.compose.project": composeProjectName("target"), "com.docker.compose.service": "api"}, valid: true},
		{name: "其他Compose项目", service: "api", labels: map[string]string{"com.docker.compose.project": "other", "com.docker.compose.service": "api"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead && r.URL.Path == "/_ping" {
					w.Header().Set("API-Version", "1.47")
					return
				}
				requests++
				if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/containers/container-full-id/json") {
					t.Errorf("排障发出了非只读请求: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": "container-full-id", "Name": "/api", "Image": "sha256:123", "Config": map[string]any{"Labels": test.labels, "Env": []string{"PASSWORD=private-env"}, "Cmd": []string{"private-command"}}, "State": map[string]any{"Status": "exited", "OOMKilled": true, "ExitCode": 137}, "RestartCount": 3})
			}))
			defer server.Close()
			service, db := newMonitorTestService(t)
			endpoint := createMonitorEndpoint(t, db, "diagnostic-endpoint", strings.Replace(server.URL, "http://", "tcp://", 1), "")
			result, err := service.InspectManagedContainer(context.Background(), endpoint.ID, "container-full-id", "target", "deployment", test.service)
			if !test.valid {
				if !errors.Is(err, ErrDiagnosticIdentity) {
					t.Fatalf("其他实例未被拒绝: %v", err)
				}
				return
			}
			if err != nil || result.ExitCode != 137 || !result.OOMKilled || result.RestartCount != 3 || requests != 1 {
				t.Fatalf("实例诊断错误: %+v %v", result, err)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "private-") {
				t.Fatal("诊断泄露了环境变量或命令")
			}
		})
	}
}
