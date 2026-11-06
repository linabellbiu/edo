package dockerengine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"edo/internal/model"
)

func TestComposeProjectRuntimeIntegration(t *testing.T) {
	if os.Getenv("EDO_TEST_DOCKER_INTEGRATION") != "1" {
		t.Skip("未启用真实 Docker 集成测试")
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "compose.yml")
	if err = os.WriteFile(file, []byte("services:\n  setup:\n    image: alpine:3.22\n    command: [sh, -c, 'exit 0']\n  api:\n    image: alpine:3.22\n    command: [sleep, '300']\n    depends_on:\n      setup:\n        condition: service_completed_successfully\n  worker:\n    image: alpine:3.22\n    command: [sleep, '300']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	project := fmt.Sprintf("edo-runtime-%d", time.Now().UnixNano())
	compose := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("docker", append([]string{"compose", "--project-name", project, "--project-directory", directory, "--file", file}, args...)...)
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("独立测试项目执行失败: %v %s", err, data)
		}
		return strings.TrimSpace(string(data))
	}
	t.Cleanup(func() { compose("down", "--remove-orphans") })
	compose("up", "-d", "--no-build", "--pull", "never", "--scale", "api=2", "--wait")
	worker := compose("ps", "-q", "worker")
	setup := compose("ps", "-a", "-q", "setup")
	inspect := func(id string) string {
		t.Helper()
		data, err := exec.Command("docker", "inspect", "--format", "{{.Id}} {{.Image}} {{.State.StartedAt}} {{.State.FinishedAt}}", id).Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	workerBefore := inspect(worker)
	setupBefore := inspect(setup)
	host, err := exec.Command("docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	service, db := newMonitorTestService(t)
	service.config.RequestTimeout = 30 * time.Second
	if err = db.Create(&model.Host{ID: "runtime-integration-host", Name: "独立测试主机", Mode: model.HostModeSSH, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	endpoint := createMonitorEndpoint(t, db, "runtime-integration-endpoint", strings.TrimSpace(string(host)), "runtime-integration-host")
	scope := ComposeProjectScope{HostID: "runtime-integration-host", ProjectName: project, WorkingDirectory: directory, Services: []string{"api", "setup"}, CompletionServices: []string{"setup"}}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	state, err := service.ComposeProjectRuntimeState(ctx, endpoint.ID, scope)
	if err != nil || !state.Running || state.Count != 3 || state.Services[1].State != "completed" || len(state.Services[0].Containers) != 2 {
		t.Fatal("实际 Compose 状态读取失败", state, err)
	}
	for _, container := range state.Services[0].Containers {
		if len(container.ID) != 64 || container.Name == "" || container.CreatedAt == nil || container.StartedAt == nil || container.RestartCount != 0 || container.Image != "alpine:3.22" || container.TerminalUnavailableReason == "" {
			t.Fatal("多副本真实容器信息验收失败", container)
		}
	}
	state, err = service.ControlComposeProject(ctx, endpoint.ID, scope, "stop")
	if err != nil || state.Running || state.State != "exited" {
		t.Fatal("实际停止验收失败", state, err)
	}
	state, err = service.ControlComposeProject(ctx, endpoint.ID, scope, "restart")
	if err != nil || !state.Running {
		t.Fatal("实际重启验收失败", state, err)
	}
	if inspect(worker) != workerBefore {
		t.Fatal("操作影响了未选择的 worker")
	}
	if inspect(setup) != setupBefore {
		t.Fatal("操作重跑或替换了已完成的一次性任务")
	}
}
