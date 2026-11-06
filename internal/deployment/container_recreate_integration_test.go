package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"edo/internal/dockerengine"
	"edo/internal/model"
	"github.com/google/uuid"
)

func frozenComposeRecord(content string) *model.DeploymentRecord {
	sum := sha256.Sum256([]byte(content))
	marker := "EDO_COMPOSE_" + hex.EncodeToString(sum[:])
	record := &model.DeploymentRecord{ID: uuid.NewString(), CommandTimeout: 90, DeploymentPlanKind: model.DeploymentPlanComposeStack}
	record.CommandScript = "set -eu\ncat >\"$release/compose.yml\" <<'" + marker + "'\n" + content + "\n" + marker + "\n"
	record.CommandDigest = model.DeploymentPlanExecutionDigest(record.DeploymentPlanKind, record.CommandScript, record.CommandTimeout)
	return record
}

func TestFrozenComposeContentRejectsTamperedSnapshot(t *testing.T) {
	content := "services:\n  api:\n    image: alpine:3.22\n"
	record := frozenComposeRecord(content)
	got, err := frozenComposeContent(record)
	if err != nil || got != content {
		t.Fatal("无法恢复冻结正文", got, err)
	}
	record.CommandScript = strings.Replace(record.CommandScript, "alpine:3.22", "alpine:latest", 1)
	if _, err = frozenComposeContent(record); !errors.Is(err, ErrRuntimeControlUnsupported) {
		t.Fatal("命令摘要失配未拒绝", err)
	}
	record.CommandDigest = model.DeploymentPlanExecutionDigest(record.DeploymentPlanKind, record.CommandScript, record.CommandTimeout)
	if _, err = frozenComposeContent(record); !errors.Is(err, ErrRuntimeControlUnsupported) {
		t.Fatal("正文摘要失配未拒绝", err)
	}
}

func TestComposeContainerActionsIntegration(t *testing.T) {
	if os.Getenv("EDO_TEST_DOCKER_INTEGRATION") != "1" {
		t.Skip("未启用真实 Docker 集成测试")
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := fmt.Sprintf("edo-actions-%d", time.Now().UnixNano())
	content := `services:
  setup:
    image: alpine:3.22
    command: [sh, -c, 'exit 0']
  api:
    image: alpine:3.22
    init: true
    command: [sleep, '300']
    environment:
      CHECK: ${EDO_RECREATE_FIXTURE_VALUE:-original}
    volumes: [data:/data]
    depends_on:
      setup:
        condition: service_completed_successfully
  worker:
    image: alpine:3.22
    init: true
    command: [sleep, '300']
volumes:
  data: {}
`
	record := frozenComposeRecord(content)
	release := filepath.Join(directory, ".edo", "compose", project, record.ID)
	if err = os.MkdirAll(release, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(release, "compose.yml")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(content + "\n")
	compose := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("docker", append([]string{"compose", "--project-name", project, "--project-directory", directory, "--file", file}, args...)...)
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("独立 Compose 操作失败: %v %s", err, data)
		}
		return strings.TrimSpace(string(data))
	}
	t.Cleanup(func() { compose("down", "--volumes", "--remove-orphans") })
	compose("up", "-d", "--pull", "never", "--no-build", "--scale", "api=2", "--wait")
	service, db, endpoint := newDeploymentTestService(t)
	host, err := exec.Command("docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&model.DockerEndpoint{}).Where("uuid = ?", endpoint).Update("host", strings.TrimSpace(string(host))).Error; err != nil {
		t.Fatal(err)
	}
	scope := dockerengine.ComposeProjectScope{HostID: dockerTestHostID, ProjectName: project, WorkingDirectory: directory, Services: []string{"api", "setup", "worker"}, CompletionServices: []string{"setup"}}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	state, err := service.docker.ComposeProjectRuntimeState(ctx, endpoint, scope)
	if err != nil {
		t.Fatal(err)
	}
	apiID, otherID := state.Services[0].Containers[0].ID, state.Services[0].Containers[1].ID
	inspect := func(id string) string {
		t.Helper()
		data, err := exec.Command("docker", "inspect", "--format", "{{.Id}} {{.Image}} {{.State.StartedAt}} {{.State.FinishedAt}}", id).Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	otherBefore := inspect(otherID)
	stop, err := service.docker.ComposeContainerControlPlan(ctx, endpoint, scope, apiID, "stop")
	if err != nil {
		t.Fatal(err)
	}
	if len(stop.Containers) != 1 {
		t.Fatal("单容器停止扩大范围")
	}
	if _, err = service.docker.ControlComposeContainer(ctx, endpoint, scope, apiID, "stop", stop.Digest, nil); err != nil {
		t.Fatal(err)
	}
	restart, err := service.docker.ComposeContainerControlPlan(ctx, endpoint, scope, apiID, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.docker.ControlComposeContainer(ctx, endpoint, scope, apiID, "restart", restart.Digest, nil); err != nil {
		t.Fatal(err)
	}
	if inspect(otherID) != otherBefore {
		t.Fatal("单容器停止/重启影响其他副本")
	}
	if _, err = service.docker.ControlComposeContainer(ctx, endpoint, scope, apiID, "restart", restart.Digest, nil); !errors.Is(err, dockerengine.ErrContainerControlChanged) {
		t.Fatal("旧确认可被重放", err)
	}
	plan, err := service.docker.ComposeContainerControlPlan(ctx, endpoint, scope, apiID, "recreate")
	if err != nil || len(plan.Containers) != 2 || len(plan.Others) != 2 {
		t.Fatal("重建没有列出准确副本", plan, err)
	}
	script, err := composeRecreateScript(record.ID, scope, plan, content)
	if err != nil {
		t.Fatal(err)
	}
	run := func(env ...string) error {
		cmd := exec.Command("sh", "-se")
		cmd.Dir = directory
		cmd.Stdin = strings.NewReader(script)
		cmd.Env = append(os.Environ(), env...)
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("受控拒绝或失败: %v %s", err, data)
		}
		return err
	}
	write(content + "\n# changed\n")
	if run() == nil {
		t.Fatal("冻结文件变化仍执行重建")
	}
	write(content + "\n")
	if run("EDO_RECREATE_FIXTURE_VALUE=changed") == nil {
		t.Fatal("变量配置变化仍执行重建")
	}
	for _, container := range plan.Containers {
		if inspect(container.ID) == "" {
			t.Fatal("只读拒绝改变了容器")
		}
	}
	for _, other := range plan.Others {
		if inspect(other.ID) == "" {
			t.Fatal("只读拒绝改变了依赖")
		}
	}
	if data, err := exec.Command("docker", "exec", apiID, "sh", "-c", "printf preserved >/data/marker").CombinedOutput(); err != nil {
		t.Fatal(err, string(data))
	}
	if err = run(); err != nil {
		log, _ := os.ReadFile(filepath.Join(release, "container-recreate.log"))
		t.Fatal(err, string(log))
	}
	after, err := service.docker.VerifyComposeRecreated(ctx, endpoint, scope, plan)
	if err != nil || !after.Running {
		t.Fatal("真实重建未通过验收", after, err)
	}
	for _, container := range after.Services[0].Containers {
		if container.ID == apiID || container.ID == otherID {
			t.Fatal("未重建全部副本")
		}
	}
	newID := after.Services[0].Containers[0].ID
	data, err := exec.Command("docker", "exec", newID, "cat", "/data/marker").Output()
	if err != nil || string(data) != "preserved" {
		t.Fatal("重建未保留命名卷", err, string(data))
	}
}
