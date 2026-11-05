package dockerengine

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/envtext"
	"edo/internal/model"
)

func TestComposeEnvFilesRejectPaths(t *testing.T) {
	if _, _, err := composeWithEnvFiles("services:\n  api: {}\n", "api", "/tmp", map[string]string{"../.env": "TOKEN=value"}); err == nil {
		t.Fatal("接受了任意文件路径")
	}
}

func TestComposeEnvFilesIntegration(t *testing.T) {
	if os.Getenv("EDO_TEST_DOCKER_INTEGRATION") != "1" {
		t.Skip("未启用真实 Docker 集成测试")
	}
	host, err := exec.Command("docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"local-cli", "ssh-script"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			project := fmt.Sprintf("edo-env-single-%d", time.Now().UnixNano())
			input := ComposeDeployInput{ServiceName: "api", Image: "alpine:3.22", YAML: "services:\n  api:\n    image: ${EDO_IMAGE}\n    command: [sleep, '300']\n", Timeout: 30 * time.Second, Stdout: io.Discard, Stderr: io.Discard, EnvFiles: map[string]string{".prod.env": envtext.Render(map[string]string{"FILE_TOKEN": "literal$token=tail'quote\\", "EMPTY": ""})}}
			compose := func(args ...string) *exec.Cmd {
				cmd := exec.CommandContext(ctx, "docker", append([]string{"compose", "--project-name", project, "--file", "-"}, args...)...)
				cmd.Stdin, cmd.Env = strings.NewReader(input.YAML), append(os.Environ(), "EDO_IMAGE="+input.Image)
				return cmd
			}
			t.Cleanup(func() {
				cmd := exec.Command("docker", "compose", "--project-name", project, "--file", "-", "down", "--remove-orphans")
				cmd.Stdin = strings.NewReader(input.YAML)
				cmd.Env = append(os.Environ(), "EDO_IMAGE="+input.Image)
				if err := cmd.Run(); err != nil {
					t.Errorf("清理本次单镜像测试失败: %v", err)
				}
			})
			if mode == "local-cli" {
				service := NewService(nil, nil, config.Runtime{DockerBuilderHost: strings.TrimSpace(string(host))})
				err = service.runComposeCLI(ctx, &model.DockerEndpoint{ID: LocalEndpointID}, project, input)
			} else {
				script, scriptErr := composeSSHEnvFiles("env EDO_IMAGE=alpine:3.22 docker compose --project-name "+shellQuote(project)+" --file - up -d --no-build --pull never --wait api", input)
				if scriptErr != nil {
					t.Fatal(scriptErr)
				}
				cmd := exec.CommandContext(ctx, "sh", "-se")
				cmd.Stdin = strings.NewReader(script)
				err = cmd.Run()
			}
			if err != nil {
				t.Fatalf("官方 Compose 文件部署失败: %v", err)
			}
			data, err := compose("exec", "-T", "api", "sh", "-c", "printf '%s\\n' \"$FILE_TOKEN\" \"${EMPTY+x}:$EMPTY\"").Output()
			if err != nil || string(data) != "literal$token=tail'quote\\\nx:\n" {
				t.Fatal("单镜像容器未收到准确文件值")
			}
		})
	}
}
