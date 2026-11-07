package dockerengine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNativeConfigurationControlPlanBindsExactContainerAndRevision(t *testing.T) {
	service, db := newMonitorTestService(t)
	id := strings.Repeat("a", 64)
	var currentRevision atomic.Value
	currentRevision.Store(strings.Repeat("1", 64))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("API-Version", "1.54")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/current/json") {
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Name": "/current", "Image": "sha256:" + strings.Repeat("f", 64), "Config": map[string]any{"Image": "example/app:v1", "Env": []string{"TOKEN=never-return-config"}, "Labels": map[string]string{"edo.managed": "true", "edo.deployment.id": "deployment", "edo.deployment.target.id": "target", ConfigurationRevisionLabel: currentRevision.Load().(string)}}, "State": map[string]any{"Status": "running", "Running": true}})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	endpoint := createMonitorEndpoint(t, db, "configuration-native-endpoint", strings.Replace(server.URL, "http://", "tcp://", 1), "")
	plan, err := service.NativeContainerControlPlan(context.Background(), endpoint.ID, "target", "deployment", "current", id, "recreate")
	if err != nil || len(plan.Containers) != 1 || plan.DockerIdentity.ContainerID != id {
		t.Fatal("原生重建未绑定精确身份", err)
	}
	encoded, _ := json.Marshal(plan)
	if strings.Contains(string(encoded), "never-return-config") || strings.Contains(string(encoded), currentRevision.Load().(string)) {
		t.Fatal("确认返回多余配置")
	}
	currentRevision.Store(strings.Repeat("2", 64))
	after, err := service.NativeContainerControlPlan(context.Background(), endpoint.ID, "target", "deployment", "current", id, "recreate")
	if err != nil || after.Digest == plan.Digest {
		t.Fatal("配置代次未绑定确认", err)
	}
	if _, err = service.NativeContainerControlPlan(context.Background(), endpoint.ID, "target", "deployment", "current", strings.Repeat("b", 64), "restart"); !errors.Is(err, ErrContainerControlChanged) {
		t.Fatal("错误 ID 仍可操作", err)
	}
	if _, err = service.NativeContainerControlPlan(context.Background(), endpoint.ID, "another", "deployment", "current", id, "recreate"); !errors.Is(err, ErrContainerControlChanged) {
		t.Fatal("错误目标仍可操作", err)
	}
}
