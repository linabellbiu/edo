package dockerengine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"edo/internal/model"
)

func TestComposeProjectScopeAndControls(t *testing.T) {
	service, db := newMonitorTestService(t)
	if err := db.Create(&model.Host{ID: "compose-host", Name: "Compose 测试主机", Mode: model.HostModeSSH, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	labels := func(name string) map[string]string {
		return map[string]string{"com.docker.compose.project": "example", "com.docker.compose.service": name, "com.docker.compose.project.working_dir": "/srv/example", "com.docker.compose.oneoff": "False"}
	}
	mu := sync.Mutex{}
	states := map[string]string{"api": "running", "db": "running", "worker": "running"}
	writes := []string{}
	health, wrongDirectory := "healthy", false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("API-Version", "1.54")
			w.WriteHeader(200)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			if !strings.Contains(r.URL.Query().Get("filters"), "com.docker.compose.project=example") || r.URL.Query().Get("all") != "1" {
				t.Error("未按固定 Compose 项目读取全部状态")
			}
			items := []any{}
			for _, name := range []string{"api", "db", "worker"} {
				if states[name] != "missing" {
					items = append(items, map[string]any{"Id": name, "Labels": labels(name)})
				}
			}
			_ = json.NewEncoder(w).Encode(items)
			return
		}
		for name, state := range states {
			if strings.HasSuffix(r.URL.Path, "/containers/"+name+"/json") {
				l := labels(name)
				if wrongDirectory {
					l["com.docker.compose.project.working_dir"] = "/srv/other"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": name, "Config": map[string]any{"Labels": l, "Env": []string{"TOKEN=never-return-this-secret"}}, "State": map[string]any{"Status": state, "Running": state == "running", "Health": map[string]any{"Status": health}}, "RestartCount": 0})
				return
			}
			if r.Method == "POST" && strings.Contains(r.URL.Path, "/containers/"+name+"/") {
				action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				writes = append(writes, name+":"+action)
				if action == "stop" {
					states[name] = "exited"
				} else {
					states[name] = "running"
				}
				w.WriteHeader(204)
				return
			}
		}
		http.Error(w, `{"message":"未登记的测试请求"}`, 404)
	}))
	t.Cleanup(server.Close)
	endpoint := createMonitorEndpoint(t, db, "compose-endpoint", strings.Replace(server.URL, "http://", "tcp://", 1), "compose-host")
	scope := ComposeProjectScope{HostID: "compose-host", ProjectName: "example", WorkingDirectory: "/srv/example", Services: []string{"api", "db"}}
	ctx := context.Background()
	state, err := service.ComposeProjectRuntimeState(ctx, endpoint.ID, scope)
	if err != nil || !state.Running || state.Count != 2 || len(state.Services) != 2 {
		t.Fatal("Compose 实时状态不正确", state, err)
	}
	raw, _ := json.Marshal(state)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("运行状态泄漏容器环境")
	}
	if _, err = service.ControlComposeProject(ctx, endpoint.ID, scope, "stop"); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(writes) != "[api:stop db:stop]" || states["worker"] != "running" {
		t.Fatal("停止误操作未选服务", writes)
	}
	if state, err = service.ControlComposeProject(ctx, endpoint.ID, scope, "restart"); err != nil || !state.Running {
		t.Fatal("停止后重启失败", state, err)
	}
	mu.Lock()
	health = "unhealthy"
	mu.Unlock()
	state, err = service.ComposeProjectRuntimeState(ctx, endpoint.ID, scope)
	if err != nil || state.Running || state.State != "unhealthy" {
		t.Fatal("不健康服务被标绿", state, err)
	}
	mu.Lock()
	health = "healthy"
	states["db"] = "missing"
	before := len(writes)
	mu.Unlock()
	state, err = service.ComposeProjectRuntimeState(ctx, endpoint.ID, scope)
	if err != nil || state.Running || state.State != "partial" || state.Services[1].State != "missing" {
		t.Fatal("缺失服务未展示", state, err)
	}
	if _, err = service.ControlComposeProject(ctx, endpoint.ID, scope, "stop"); err == nil || len(writes) != before {
		t.Fatal("缺失服务时部分执行了整组操作")
	}
	mu.Lock()
	states["db"] = "running"
	wrongDirectory = true
	mu.Unlock()
	if _, err = service.ComposeProjectRuntimeState(ctx, endpoint.ID, scope); err == nil {
		t.Fatal("接受了其他目录的同名项目")
	}
	wrongHost := scope
	wrongHost.HostID = "other-host"
	if _, err = service.ComposeProjectRuntimeState(ctx, endpoint.ID, wrongHost); err == nil {
		t.Fatal("接受了其他主机")
	}
}
