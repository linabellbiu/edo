package dockerengine

import (
	"context"
	"encoding/json"
	"errors"
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
				_ = json.NewEncoder(w).Encode(map[string]any{
					"Id": name, "Name": "/example-" + name + "-1", "Created": "2026-11-06T06:00:00Z", "Image": "sha256:image-id",
					"Config":     map[string]any{"Image": "registry.example/" + name + ":v2", "Labels": l, "Env": []string{"TOKEN=never-return-this-secret"}, "Cmd": []string{"private-command"}},
					"HostConfig": map[string]any{"Init": true, "RestartPolicy": map[string]any{"Name": "unless-stopped"}},
					"Mounts":     []any{map[string]any{"Source": "/private-mount", "Destination": "/data"}},
					"State":      map[string]any{"Status": state, "Running": state == "running", "StartedAt": "2026-11-06T06:00:01Z", "FinishedAt": "0001-01-01T00:00:00Z", "Health": map[string]any{"Status": health}}, "RestartCount": 3,
					"NetworkSettings": map[string]any{"Ports": map[string]any{"80/tcp": []any{map[string]any{"HostIp": "0.0.0.0", "HostPort": "8080"}}, "443/tcp": nil}, "Networks": map[string]any{"example_default": map[string]any{"IPAddress": "172.20.0.2"}}},
				})
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
	if state.RuntimeID != endpoint.ID || len(state.Services[0].Containers) != 1 {
		t.Fatal("未返回经过校验的运行时和容器身份", state)
	}
	detail := state.Services[0].Containers[0]
	if detail.ID != "api" || detail.Name != "example-api-1" || detail.Image != "registry.example/api:v2" || detail.ImageID != "sha256:image-id" || detail.RestartCount != 3 || detail.CreatedAt == nil || detail.StartedAt == nil || detail.FinishedAt != nil || detail.TerminalUnavailableReason != "" {
		t.Fatal("容器基本信息不完整或未清除零时间", detail)
	}
	if len(detail.Ports) != 2 || detail.Ports[0].ContainerPort != "443/tcp" || detail.Ports[1].HostIP != "0.0.0.0" || detail.Ports[1].HostPort != "8080" || len(detail.Networks) != 1 || detail.Networks[0].IPAddress != "172.20.0.2" {
		t.Fatal("容器端口或网络读取不正确", detail)
	}
	raw, _ := json.Marshal(state)
	for _, marker := range []string{"secret", "private-command", "private-mount"} {
		if strings.Contains(string(raw), marker) {
			t.Fatal("运行状态泄漏容器敏感配置", marker)
		}
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

func TestComposeContainerTimeKeepsMissingValuesEmpty(t *testing.T) {
	for _, value := range []string{"", "invalid", "0001-01-01T00:00:00Z"} {
		if composeContainerTime(value) != nil {
			t.Fatal("缺失或无效时间被当作真实时间", value)
		}
	}
}

func TestComposeProjectCompletionTasks(t *testing.T) {
	service, db := newMonitorTestService(t)
	if err := db.Create(&model.Host{ID: "completion-host", Name: "一次性任务测试主机", Mode: model.HostModeSSH, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	labels := func(name string) map[string]string {
		return map[string]string{"com.docker.compose.project": "completion", "com.docker.compose.service": name, "com.docker.compose.project.working_dir": "/srv/completion"}
	}
	var mu sync.Mutex
	apiState, setupState, exitCode, oom, engineError := "running", "exited", 0, false, ""
	writes := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("API-Version", "1.54")
			return
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"Id": "api", "Labels": labels("api")}, map[string]any{"Id": "setup", "Labels": labels("setup")}})
			return
		}
		for _, name := range []string{"api", "setup"} {
			if strings.HasSuffix(r.URL.Path, "/containers/"+name+"/json") {
				state := map[string]any{"Status": apiState, "Running": apiState == "running"}
				if name == "setup" {
					state = map[string]any{"Status": setupState, "Running": setupState == "running" || setupState == "paused", "ExitCode": exitCode, "OOMKilled": oom, "Error": engineError}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": name, "Config": map[string]any{"Labels": labels(name)}, "State": state})
				return
			}
			if r.Method == "POST" && strings.Contains(r.URL.Path, "/containers/"+name+"/") {
				action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				writes = append(writes, name+":"+action)
				if name == "api" {
					apiState = "running"
					if action == "stop" {
						apiState = "exited"
					}
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	endpoint := createMonitorEndpoint(t, db, "completion-endpoint", strings.Replace(server.URL, "http://", "tcp://", 1), "completion-host")
	scope := ComposeProjectScope{HostID: "completion-host", ProjectName: "completion", WorkingDirectory: "/srv/completion", Services: []string{"api", "setup"}, CompletionServices: []string{"setup"}}
	ctx := context.Background()
	for _, tc := range []struct {
		name, task, daemon, wantTask, wantOverall string
		code                                      int
		oom                                       bool
		engineError                               string
	}{
		{name: "完成不冒充停止", task: "exited", daemon: "running", wantTask: "completed", wantOverall: "running"},
		{name: "常驻服务停止", task: "exited", daemon: "exited", wantTask: "completed", wantOverall: "exited"},
		{name: "任务非零退出", task: "exited", daemon: "running", code: 1, wantTask: "failed", wantOverall: "partial"},
		{name: "任务内存终止", task: "exited", daemon: "running", oom: true, wantTask: "failed", wantOverall: "partial"},
		{name: "任务引擎错误", task: "exited", daemon: "running", engineError: "测试错误", wantTask: "failed", wantOverall: "partial"},
		{name: "任务执行中", task: "running", daemon: "running", wantTask: "completing", wantOverall: "partial"},
		{name: "任务已暂停", task: "paused", daemon: "running", wantTask: "paused", wantOverall: "partial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			setupState, apiState, exitCode, oom, engineError = tc.task, tc.daemon, tc.code, tc.oom, tc.engineError
			writes = nil
			mu.Unlock()
			state, err := service.ComposeProjectRuntimeState(ctx, endpoint.ID, scope)
			if err != nil || state.State != tc.wantOverall || state.Services[1].State != tc.wantTask || !state.Services[1].RunToCompletion || state.Running != (tc.wantOverall == "running") {
				t.Fatal("一次性任务状态错误", state, err)
			}
			if tc.wantTask != "completed" {
				for _, action := range []string{"stop", "restart"} {
					if _, err = service.ControlComposeProject(ctx, endpoint.ID, scope, action); !errors.Is(err, ErrRuntimeControlFailed) {
						t.Fatal("未完成任务未阻止整组操作", err)
					}
				}
				mu.Lock()
				defer mu.Unlock()
				if len(writes) != 0 {
					t.Fatal("阻断前已部分操作常驻服务", writes)
				}
			}
		})
	}
	mu.Lock()
	setupState, apiState, exitCode, oom, engineError = "exited", "running", 0, false, ""
	writes = nil
	mu.Unlock()
	for _, action := range []string{"restart", "stop", "restart"} {
		if _, err := service.ControlComposeProject(ctx, endpoint.ID, scope, action); err != nil {
			t.Fatal("常驻服务操作失败", action, err)
		}
	}
	mu.Lock()
	gotWrites := fmt.Sprint(writes)
	mu.Unlock()
	if gotWrites != "[api:restart api:stop api:start]" {
		t.Fatal("操作重跑了已完成任务", gotWrites)
	}
	onlyTask := scope
	onlyTask.Services = []string{"setup"}
	state, err := service.ComposeProjectRuntimeState(ctx, endpoint.ID, onlyTask)
	if err != nil || state.State != "completed" || state.Running || state.Services[0].ReadyCount != 1 {
		t.Fatal("纯一次性任务状态错误", state, err)
	}
	if _, err = service.ControlComposeProject(ctx, endpoint.ID, onlyTask, "restart"); !errors.Is(err, ErrRuntimeControlInvalid) {
		t.Fatal("允许重跑纯一次性任务", err)
	}
	ordinary := scope
	ordinary.CompletionServices = nil
	state, err = service.ComposeProjectRuntimeState(ctx, endpoint.ID, ordinary)
	if err != nil || state.Services[1].State != "exited" || state.Services[1].ReadyCount != 0 || state.Services[1].RunToCompletion {
		t.Fatal("未声明完成条件的零退出服务被误判为完成", state, err)
	}
}
