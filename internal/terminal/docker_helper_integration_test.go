package terminal

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/client"

	"edo/internal/composeconfig"
	"edo/internal/dockerengine"
)

func TestDockerHelperRealInstallationAndControlledRelease(t *testing.T) {
	if os.Getenv("EDO_TEST_DOCKER_INTEGRATION") != "1" {
		t.Skip("未选择真实 Docker 终端集成")
	}
	directory := os.Getenv("EDO_TEST_TERMINAL_TOOLS")
	if directory == "" {
		t.Fatal("真实终端集成缺少双架构工具目录")
	}
	docker := func(tb testing.TB, args ...string) string {
		tb.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			tb.Fatalf("隔离 Docker 操作失败：%v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		host = docker(t, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	}
	architecture := docker(t, "info", "--format", "{{.Architecture}}")
	switch architecture {
	case "aarch64":
		architecture = "arm64"
	case "x86_64":
		architecture = "amd64"
	}
	if architecture != "amd64" && architecture != "arm64" {
		t.Fatal("隔离测试 Docker 架构不受支持")
	}
	for _, scenario := range []struct{ nonroot, readonly bool }{{}, {nonroot: true}, {readonly: true}, {nonroot: true, readonly: true}} {
		t.Run(fmt.Sprintf("nonroot=%v/readonly=%v", scenario.nonroot, scenario.readonly), func(t *testing.T) {
			nonroot := scenario.nonroot
			name := "edo-terminal-install-" + uuid.NewString()[:8]
			args := []string{"run", "-d", "--init", "--network", "none", "--memory", "128m", "--cpus", "0.5", "--label", "edo.test=terminal-default-install", "--platform", "linux/" + architecture, "--pull", "never", "--name", name}
			if nonroot {
				args = append(args, "--user", "10001:10001")
			}
			args = append(args, "alpine:3.22", "sleep", "600")
			var id string
			if scenario.readonly {
				user := "0:0"
				if nonroot {
					user = "10001:10001"
				}
				content := fmt.Sprintf("services:\n  fixture:\n    image: alpine:3.22\n    platform: linux/%s\n    user: '%s'\n    command: [sleep, '600']\n    network_mode: none\n    mem_limit: 128m\n    cpus: 0.5\n    read_only: true\n    cap_drop: [ALL]\n    security_opt: ['no-new-privileges:true']\n", architecture, user)
				content, _, err := composeconfig.WithDefaultInit(content)
				if err != nil {
					t.Fatal(err)
				}
				content, _, err = composeconfig.WithTerminalTools(content)
				if err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(t.TempDir(), "compose.yaml")
				if err := os.WriteFile(file, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
				compose := []string{"compose", "-p", name, "-f", file}
				t.Cleanup(func() { docker(t, append(compose, "down", "--volumes", "--remove-orphans")...) })
				docker(t, append(compose, "up", "-d", "--no-build", "--pull", "never", "--wait")...)
				id = docker(t, "inspect", "--format", "{{.Id}}", docker(t, append(compose, "ps", "-q", "fixture")...))
			} else {
				id = docker(t, args...)
				t.Cleanup(func() {
					if out, err := exec.Command("docker", "rm", "-f", name).CombinedOutput(); err != nil {
						t.Errorf("隔离容器清理失败：%v %s", err, out)
					}
				})
			}
			service, endpoint := dockerSDKService(t, host)
			if os.Getenv("EDO_TEST_TERMINAL_SSH_PROXY") == "1" {
				address, fingerprint, password := terminalDockerSSHProxy(t, host)
				remote, err := service.docker.Create(t.Context(), "fixture", dockerengine.Input{Name: "终端 SSH 隔离目标", Host: "ssh://fixture@" + address, SSH: &dockerengine.SSHBundle{Password: password}, SSHHostKeyFingerprint: fingerprint})
				if err != nil {
					t.Fatal("创建隔离 SSH 目标失败", err)
				}
				endpoint = remote.ID
			}
			service.helperDirectory = directory
			ctx, cancel := context.WithTimeout(WithExecutionAuthorization(t.Context(), func() bool { return true }), 40*time.Second)
			defer cancel()
			var installed []DockerPreparation
			ctx = WithDockerPreparationObserver(ctx, func(event DockerPreparation) { installed = append(installed, event) })
			session, err := service.OpenDocker(ctx, endpoint, id, "sh", Size{Columns: 100, Rows: 24})
			if err != nil {
				t.Fatal("默认安装后未能进入真实终端", err)
			}
			t.Cleanup(func() { _ = session.Close() })
			if len(installed) != 1 || !installed[0].Succeeded || installed[0].Architecture != architecture {
				t.Fatal("未实际自动安装目标架构工具")
			}
			if err := session.Resize(ctx, Size{Columns: 110, Rows: 30}); err != nil {
				t.Fatal("真实终端调整尺寸失败", err)
			}
			marker := "edo-terminal-fixture-" + uuid.NewString()
			// 拆开标记，避免 PTY 命令回显被误认为实际执行结果。
			if _, err := io.WriteString(session, "sleep 60 &\nprintf '\\n%s%s\\n' "+marker[:20]+" "+marker[20:]+"\n"); err != nil {
				t.Fatal(err)
			}
			read := make(chan error, 1)
			go func() {
				var output strings.Builder
				b := make([]byte, 4096)
				for {
					n, err := session.Read(b)
					if n > 0 {
						output.Write(b[:n])
						if strings.Contains(output.String(), marker) {
							read <- nil
							return
						}
					}
					if err != nil {
						read <- err
						return
					}
					if output.Len() > 65536 {
						read <- fmt.Errorf("验收输出超限")
						return
					}
				}
			}()
			select {
			case err := <-read:
				if err != nil {
					t.Fatal("未收到真实终端输出", err)
				}
			case <-ctx.Done():
				t.Fatal("真实终端输出超时")
			}
			if nonroot {
				cancel()
			}
			if err := session.Close(); err != nil {
				t.Fatal("真实受控会话未确认回收", err)
			}
			probe, stopProbe := context.WithTimeout(t.Context(), 10*time.Second)
			defer stopProbe()
			api, err := service.docker.Client(probe, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			defer api.Close()
			state, err := api.ContainerInspect(probe, id, client.ContainerInspectOptions{})
			if err != nil || !state.Container.State.Running || state.Container.RestartCount != 0 {
				t.Fatal("关闭终端影响业务容器", err)
			}
			if scenario.readonly {
				if !state.Container.HostConfig.ReadonlyRootfs || len(state.Container.HostConfig.CapDrop) != 1 || len(state.Container.HostConfig.SecurityOpt) != 1 || installed[0].Volume == "" {
					t.Fatal("只读业务容器的安全配置被改变，或未使用工具卷")
				}
				remaining := docker(t, "ps", "-aq", "--filter", "label="+helperPreparationLabel)
				if remaining != "" {
					t.Fatal("安装后残留了终端工具准备容器")
				}
			}
			top := docker(t, "top", id, "-eo", "pid,args")
			if strings.Contains(top, "edo-terminal-session") {
				t.Fatal("关闭后残留本会话工具或后台进程")
			}
			for _, line := range strings.Split(top, "\n") {
				fields := strings.Fields(line)
				if len(fields) > 1 && strings.Join(fields[1:], " ") == "sleep 60" {
					t.Fatal("关闭后残留本会话后台进程")
				}
			}
		})
	}
}
