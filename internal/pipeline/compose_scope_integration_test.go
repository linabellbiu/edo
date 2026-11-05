package pipeline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"edo/internal/model"
)

func TestComposeServiceScopeIntegration(t *testing.T) {
	if os.Getenv("EDO_TEST_DOCKER_INTEGRATION") != "1" {
		t.Skip("未启用真实 Docker 集成测试")
	}
	directory := t.TempDir()
	project := fmt.Sprintf("edo-scope-%d", time.Now().UnixNano())
	config := model.WorkflowNodeConfig{Compose: &model.ComposeDeploymentSettings{ProjectName: project, TagVariable: "IMAGE_TAG"}}
	compose := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("docker", append([]string{"compose", "--project-name", project, "--file", filepath.Join(directory, ".edo", "compose", project, "current.yml")}, args...)...)
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("独立 Compose 操作失败: %v %s", err, data)
		}
		return strings.TrimSpace(string(data))
	}
	t.Cleanup(func() { compose("down", "--remove-orphans") })
	source := "services:\n  api:\n    image: alpine:3.22\n    command: [sleep, '300']\n    depends_on: [db]\n  db:\n    image: alpine:3.22\n    command: [sleep, '300']\n  worker:\n    image: alpine:3.22\n    command: [sleep, '300']\n"
	deploy := func(id, content string) {
		t.Helper()
		script, err := composeStackScript(content, config, directory, 120, "")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-se")
		cmd.Dir, cmd.Stdin = directory, strings.NewReader(script)
		cmd.Env = append(os.Environ(), "EDO_DEPLOYMENT_ID="+id, "EDO_DEPLOYMENT_TARGET_ID=target", "EDO_COMMIT_SHA=test")
		if data, err := cmd.CombinedOutput(); err != nil {
			log, _ := os.ReadFile(filepath.Join(directory, ".edo", "compose", project, id, "compose.log"))
			t.Fatalf("真实部署脚本失败: %v %s %s", err, data, log)
		}
	}
	deploy("first", source)
	worker, api := compose("ps", "-q", "worker"), compose("ps", "-q", "api")
	if worker == "" || api == "" || compose("ps", "-q", "db") == "" {
		t.Fatal("留空没有启动全部服务及依赖")
	}
	config.Compose.Services = []string{"api"}
	deploy("second", strings.ReplaceAll(source, "command: [sleep, '300']", "command: [sleep, '301']"))
	if compose("ps", "-q", "worker") != worker || compose("ps", "-q", "api") == api {
		t.Fatal("指定服务更新没有保持未选服务实例")
	}
}
