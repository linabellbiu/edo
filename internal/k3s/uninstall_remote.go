package k3s

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"edo/internal/model"
	"edo/internal/sshclient"
)

// 现场摘要只包含固定文件的身份和摘要，不回传配置、环境变量或脚本内容。
type uninstallProof struct{ Script, Killall string }

var uninstallFileProof = regexp.MustCompile(`^[0-9]+:[0-9]+:0:[0-9]+:[0-7]{3,4}:1:[0-9]+:[0-9]+:[0-9]+:[a-f0-9]{64}$`)

func parseUninstallProof(output string) (uninstallProof, error) {
	var result uninstallProof
	values := map[string]*string{"script": &result.Script, "killall": &result.Killall}
	lines := strings.Split(output, "\n")
	if len(output) > 2048 || len(lines) != 4 || lines[2] != "EDO_K3S_UNINSTALL_PROOF_END" || lines[3] != "" {
		return result, ErrUninstallUnsupported
	}
	for _, line := range lines[:2] {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 || parts[0] != "EDO_K3S_UNINSTALL_PROOF" || values[parts[1]] == nil || !uninstallFileProof.MatchString(parts[2]) {
			return uninstallProof{}, ErrUninstallUnsupported
		}
		*values[parts[1]] = parts[2]
		delete(values, parts[1])
	}
	if len(values) != 0 {
		return uninstallProof{}, ErrUninstallUnsupported
	}
	return result, nil
}

func (s *Service) probeUninstall(ctx context.Context, host model.Host, bundle sshclient.Bundle, summary UninstallSummary) (uninstallProof, error) {
	output, err := s.transport.Read(ctx, host, bundle, uninstallProbeScript(summary))
	if err != nil {
		slog.WarnContext(ctx, "K3s 卸载现场检查未通过", "operation", "k3s_uninstall_probe", "host_id", host.ID, "err", err)
		if ctx.Err() != nil {
			return uninstallProof{}, ctx.Err()
		}
		return uninstallProof{}, ErrUninstallUnsupported
	}
	proof, err := parseUninstallProof(output)
	if err != nil {
		slog.WarnContext(ctx, "K3s 卸载现场摘要无效", "operation", "k3s_uninstall_probe_protocol", "host_id", host.ID, "err", err)
	}
	return proof, err
}

func uninstallVariables(summary UninstallSummary) string {
	return "edo_script=" + shellQuote(summary.ScriptPath) + "\n"
}

// 只核对已选主机上官方生成的两个脚本文件，不检查或处理挂载、网络和 K3s 配置。
const uninstallReadOnlyGuard = `
[ "$(id -u)" = 0 ] || exit 80
[ ! -e /run/edo-k3s-install.lock ] && [ ! -L /run/edo-k3s-install.lock ] || exit 80
safe_path() {
  edo_path=$1
  while [ "$edo_path" != / ]; do
    [ ! -L "$edo_path" ] || return 1
    if [ -d "$edo_path" ]; then
      edo_parent_uid=$(stat -c '%u' -- "$edo_path") || return 1
      edo_parent_mode=$(stat -c '%a' -- "$edo_path") || return 1
      [ "$edo_parent_uid" = 0 ] && [ "$((0$edo_parent_mode & 022))" = 0 ] || return 1
    fi
    edo_path=${edo_path%/*}; [ -n "$edo_path" ] || edo_path=/
  done
}
file_proof() {
  safe_path "$1" && [ -f "$1" ] && [ ! -L "$1" ] || return 1
  edo_meta=$(stat -c '%d:%i:%u:%g:%a:%h:%s:%Y:%Z' -- "$1") || return 1
  edo_uid=$(stat -c '%u' -- "$1") || return 1
  edo_mode=$(stat -c '%a' -- "$1") || return 1
  edo_links=$(stat -c '%h' -- "$1") || return 1
  edo_size=$(stat -c '%s' -- "$1") || return 1
  [ "$edo_uid" = 0 ] && [ "$edo_links" = 1 ] && [ "$edo_size" -gt 0 ] && [ "$edo_size" -le 1048576 ] && [ "$((0$edo_mode & 022))" = 0 ] || return 1
  edo_hash=$(sha256sum -- "$1") || return 1
  edo_hash=${edo_hash%% *}
  printf '%s:%s' "$edo_meta" "$edo_hash"
}
edo_script_proof=$(file_proof "$edo_script") || exit 80
edo_killall_proof=$(file_proof /usr/local/bin/k3s-killall.sh) || exit 80
`

func uninstallProbeScript(summary UninstallSummary) string {
	return "set -eu\n" + uninstallVariables(summary) + uninstallReadOnlyGuard + `
printf 'EDO_K3S_UNINSTALL_PROOF\tscript\t%s\n' "$edo_script_proof"
printf 'EDO_K3S_UNINSTALL_PROOF\tkillall\t%s\n' "$edo_killall_proof"
printf 'EDO_K3S_UNINSTALL_PROOF_END\n'
`
}

func uninstallExecutionScript(snapshot savedUninstall) string {
	return "set -eu\n" + uninstallVariables(snapshot.Summary) + uninstallReadOnlyGuard +
		"[ \"$edo_script_proof\" = " + shellQuote(snapshot.Proof.Script) + " ] || exit 81\n" +
		"[ \"$edo_killall_proof\" = " + shellQuote(snapshot.Proof.Killall) + " ] || exit 81\n" +
		// 原样执行官方生成的卸载脚本，不覆盖其环境，不另外停止服务或清理资源。
		"sh -- \"$edo_script\"\n"
}

func uninstallVerificationScript(summary UninstallSummary) string {
	return "set -eu\n" + uninstallVariables(summary) + `
[ "$(id -u)" = 0 ] || exit 82
[ ! -e "$edo_script" ] && [ ! -L "$edo_script" ] || exit 82
printf 'EDO_K3S_UNINSTALL_VERIFIED\n'
`
}
