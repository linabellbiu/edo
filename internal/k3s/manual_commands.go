package k3s

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const manualNamePlaceholder = "__EDO_K3D_NAME__"

type ManualUninstallExecution struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	Instruction   string `json:"instruction"`
	EnterCommand  string `json:"enter_command,omitempty"`
	ListCommand   string `json:"list_command"`
	DeleteCommand string `json:"delete_command"`
}

// 只读取非敏感的连接位置；不导出安装会话环境、认证内容或临时证书。
type manualDockerConnection interface {
	ManualCLIConnection(context.Context) (host, certificateDirectory, tlsVerify string, err error)
}

func manualExecution(id, label, instruction, setup string) ManualUninstallExecution {
	command := func(arguments string) string {
		script := "set -eu\numask 077\n" + setup + `
edo_manual_home=$(mktemp -d "${TMPDIR:-/tmp}/edo-k3d-manual.XXXXXXXX")
trap 'rm -rf -- "$edo_manual_home"' EXIT
: > "$edo_manual_home/config"
env -i PATH="$PATH" HOME="$edo_manual_home" XDG_CONFIG_HOME="$edo_manual_home" DOCKER_HOST="$DOCKER_HOST" DOCKER_TLS_VERIFY="${DOCKER_TLS_VERIFY:-}" DOCKER_CERT_PATH="${DOCKER_CERT_PATH:-}" KUBECONFIG="$edo_manual_home/config" "$edo_k3d" cluster ` + arguments
		if id == "remote" {
			return "sh -c " + shellQuote(`if [ "$(id -u)" = 0 ]; then exec sh -c "$1"; else exec sudo sh -c "$1"; fi`) + " sh " + shellQuote(script)
		}
		return "sh -c " + shellQuote(script)
	}
	return ManualUninstallExecution{ID: id, Label: label, Instruction: instruction, ListCommand: command("list"), DeleteCommand: command("delete " + shellQuote(manualNamePlaceholder))}
}

func (s *Service) manualUninstallExecutions(ctx context.Context) []ManualUninstallExecution {
	// 复用远程安装的工具定位和摘要核验，始终调用已有官方二进制，不下载或安装工具。
	remote := manualExecution("remote", "SSH 主机", "登录实际安装主机的新地址，使用 root 或 sudo 执行以下命令。", k3dLocalEnvironment+k3dToolSelection+`edo_tool_valid || { printf '%s\n' 'EDO 工具目录中的官方 k3d 不可用，请核对原安装环境。' >&2; exit 1; }
unset DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH
export DOCKER_HOST=unix:///var/run/docker.sock HOME=/var/lib/edo/k3d`)
	executions := []ManualUninstallExecution{remote}
	if s.localK3d == nil || s.localK3d.tool == nil || runtime.GOOS == "windows" {
		return executions
	}
	connection, ok := s.localK3d.docker.(manualDockerConnection)
	if !ok {
		return executions
	}
	host, cert, verify, err := connection.ManualCLIConnection(ctx)
	if err != nil {
		slog.Warn("读取本地手动卸载连接位置失败", "operation", "k3d_manual_uninstall_connection", "err", err)
		return executions
	}
	tool := s.localK3d.tool
	tool.resolveMu.Lock()
	path := tool.path
	if path == "" && tool.cache != "" {
		path = filepath.Join(tool.cache, "edo", "k3d", k3dVersion, "k3d-"+strings.ReplaceAll(tool.platform, "/", "-"))
	}
	tool.resolveMu.Unlock()
	if path == "" {
		return executions
	}
	setup := "edo_k3d=" + shellQuote(path) + `
[ -f "$edo_k3d" ] && [ ! -L "$edo_k3d" ] && [ -x "$edo_k3d" ] || { printf '%s\n' '当前环境中未找到 EDO 已下载的官方 k3d，请进入原 EDO 后端运行环境。' >&2; exit 1; }
unset DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH
export DOCKER_HOST=` + shellQuote(host)
	if cert != "" {
		setup += "\nexport DOCKER_TLS_VERIFY=" + shellQuote(verify) + " DOCKER_CERT_PATH=" + shellQuote(cert)
	}
	local := manualExecution("local", "EDO 本地 Docker", "在 EDO 后端运行环境中执行。Docker Compose 部署可先在 EDO 部署目录执行下方命令进入后端；直接运行 EDO 时，使用启动后端的同一账号。", setup)
	if _, err := os.Stat("/.dockerenv"); err == nil {
		local.EnterCommand = "docker compose exec api sh"
	}
	return append(executions, local)
}
