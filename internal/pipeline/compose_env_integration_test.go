package pipeline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"edo/internal/envtext"
)

func TestComposeEnvTextIntegration(t *testing.T) {
	if os.Getenv("EDO_TEST_DOCKER_INTEGRATION") != "1" {
		t.Skip("未启用真实 Docker 集成测试")
	}
	directory := t.TempDir()
	project := fmt.Sprintf("edo-env-%d", time.Now().UnixNano())
	values := map[string]string{"TOKEN": "literal$token=tail'quote\\", "EMPTY": ""}
	fileValues := map[string]string{"FILE_TOKEN": "file$literal=tail'quote\\"}
	files := map[string]string{".prod.env": envtext.Render(fileValues)}
	content, writes, err := composeStackVariables("services:\n  api:\n    image: alpine:3.22\n    command: [sleep, '300']\n    environment: [KEEP=original, TOKEN=old]\n  worker:\n    image: alpine:3.22\n    command: [sleep, '300']\n", project, directory, "integration/deploy", values, files)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(directory, ".edo", "compose", project), 0700); err != nil {
		t.Fatal(err)
	}
	write := exec.Command("sh", "-se")
	write.Dir, write.Stdin, write.Env = directory, strings.NewReader("umask 077\n"+writes), append(os.Environ(), envtext.TransportKey(".prod.env")+"="+files[".prod.env"])
	if err = write.Run(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "compose.yml")
	if err = os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	compose := func(args ...string) *exec.Cmd {
		cmd := exec.Command("docker", append([]string{"compose", "--project-name", project, "--file", file}, args...)...)
		cmd.Env = append(os.Environ(), "TOKEN="+values["TOKEN"], "EMPTY=")
		return cmd
	}
	t.Cleanup(func() {
		if err := compose("down", "--remove-orphans").Run(); err != nil {
			t.Errorf("清理本次独立 Compose 失败: %v", err)
		}
	})
	if data, err := compose("up", "-d", "--no-build", "--pull", "never", "--wait").CombinedOutput(); err != nil {
		t.Fatalf("真实 Compose 启动失败: %v %s", err, data)
	}
	for _, service := range []string{"api", "worker"} {
		cmd := compose("exec", "-T", service, "sh", "-c", "printf '%s\\n' \"$TOKEN\" \"$FILE_TOKEN\" \"${EMPTY+x}:$EMPTY\"")
		data, err := cmd.Output()
		if err != nil || string(data) != values["TOKEN"]+"\n"+fileValues["FILE_TOKEN"]+"\nx:\n" {
			t.Fatal("真实容器中的变量内容与输入不一致")
		}
	}
}
