package k3s

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type manualConnectionFixture struct{ host, cert string }

func (f manualConnectionFixture) ManualCLIConnection(context.Context) (string, string, string, error) {
	return f.host, f.cert, "1", nil
}
func (f manualConnectionFixture) LocalCLIEnvironment(context.Context) ([]string, string, func(), error) {
	panic("指引不得创建安装会话")
}

const manualToolFixture = `#!/bin/sh
if [ "$1" = version ]; then printf 'k3d version v5.9.0\n'; exit; fi
if [ "${3:-}" = fail-on-purpose ]; then printf 'fixture terminal stderr\n' >&2; exit 37; fi
printf 'args=%s/%s/%s\n' "$1" "$2" "${3:-}"
printf 'docker=%s\ntls=%s\ncert=%s\n' "$DOCKER_HOST" "${DOCKER_TLS_VERIFY:-}" "${DOCKER_CERT_PATH:-}"
[ -z "${K3D_RUNTIME:-}" ] && [ -z "${DOCKER_CONTEXT:-}" ] || exit 91
[ "$KUBECONFIG" = "$HOME/config" ] && [ "$XDG_CONFIG_HOME" = "$HOME" ] || exit 92
[ -f "$KUBECONFIG" ] || exit 93
printf '%s\n' "$HOME"
`

func runManualFixture(t *testing.T, command string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "K3D_RUNTIME=must-not-inherit", "DOCKER_CONTEXT=must-not-inherit", "DOCKER_HOST=unix:///wrong-docker.sock", "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH=/wrong-certs")
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func TestManualCommandsReuseCachedLocalToolAndConfiguredDockerWithoutDownloading(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("此命令面向已有 POSIX shell 的运行环境")
	}
	path := filepath.Join(t.TempDir(), "cached tool's directory", "k3d")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(manualToolFixture), 0755); err != nil {
		t.Fatal(err)
	}
	for _, cert := range []string{"", "/fixture/cert directory's client"} {
		s := &Service{localK3d: &localK3dRuntime{docker: manualConnectionFixture{"unix:///actual-configured-daemon.sock", cert}, tool: &localK3dTool{path: path}}}
		choices := s.manualUninstallExecutions(context.Background())
		if len(choices) != 2 {
			t.Fatal("没有安装记录时也应生成本地执行方式")
		}
		for _, command := range []string{choices[1].ListCommand, strings.Replace(choices[1].DeleteCommand, manualNamePlaceholder, "actual-cluster", 1)} {
			output, err := runManualFixture(t, command)
			if err != nil || !strings.Contains(output, "docker=unix:///actual-configured-daemon.sock") || !strings.Contains(output, "cert="+cert+"\n") {
				t.Fatal("未复用现有官方工具和实际连接", output, err)
			}
			if strings.Contains(command, "delete") && !strings.Contains(output, "args=cluster/delete/actual-cluster") {
				t.Fatal("必须精确传入单个集群名称", output)
			}
			lines := strings.Split(strings.TrimSpace(output), "\n")
			if _, err := os.Stat(lines[len(lines)-1]); !os.IsNotExist(err) {
				t.Fatal("仅应清理本命令创建的临时配置目录", err)
			}
		}
		output, err := runManualFixture(t, strings.Replace(choices[1].DeleteCommand, manualNamePlaceholder, "fail-on-purpose", 1))
		failure, ok := err.(*exec.ExitError)
		if !ok || failure.ExitCode() != 37 || !strings.Contains(output, "fixture terminal stderr") {
			t.Fatal("必须保留官方程序的原始终端错误和退出码", output, err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s := &Service{localK3d: &localK3dRuntime{docker: manualConnectionFixture{"unix:///fixture.sock", ""}, tool: &localK3dTool{path: path}}}
	output, err := runManualFixture(t, s.manualUninstallExecutions(context.Background())[1].ListCommand)
	if err == nil || !strings.Contains(output, "未找到 EDO 已下载的官方 k3d") {
		t.Fatal("工具不存在时必须明确停止，不安装或调用其他全局命令", output, err)
	}
}

func TestManualRemoteCommandsUseInstallationToolSelectionWithRootOrSudo(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("远程工具验证使用 Linux 的 sha256sum 和 timeout；Linux CI 执行此覆盖")
	}
	for _, uid := range []string{"0", "1000"} {
		t.Run(uid, func(t *testing.T) {
			directory := t.TempDir()
			arch := runtime.GOARCH
			path := filepath.Join(directory, k3dVersion, "k3d-linux-"+arch)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(manualToolFixture), 0755); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(directory, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			for name, script := range map[string]string{"id": "#!/bin/sh\nprintf '" + uid + "\\n'\n", "sudo": "#!/bin/sh\nexec \"$@\"\n"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			choice := (&Service{}).manualUninstallExecutions(context.Background())[0]
			hash := sha256.Sum256([]byte(manualToolFixture))
			rewrite := strings.NewReplacer("/var/lib/edo/k3d", directory, "edo_path=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "edo_path="+shellQuote(os.Getenv("PATH")), "06d8f25bc3a971c4eb29e0ff08429b180402db0f4dec838c9eac427e296800a0", fmt.Sprintf("%x", hash), "03cde5cf23e6e8e67de5a039ecf26e5b85aca82fba3e5d13dadf904cd218a250", fmt.Sprintf("%x", hash))
			command := rewrite.Replace(strings.Replace(choice.DeleteCommand, manualNamePlaceholder, "one-cluster", 1))
			output, err := runManualFixture(t, command)
			if err != nil || !strings.Contains(output, "args=cluster/delete/one-cluster") || !strings.Contains(output, "docker=unix:///var/run/docker.sock") {
				t.Fatal("未复用远程安装工具与 Docker 目标", output, err)
			}
		})
	}
}
