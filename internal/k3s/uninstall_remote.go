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
type uninstallProof struct{ Script, Killall, Config, Unit, Environment string }

var uninstallFileProof = regexp.MustCompile(`^[0-9]+:[0-9]+:0:[0-9]+:[0-7]{3,4}:1:[0-9]+:[0-9]+:[0-9]+:[a-f0-9]{64}$`)

func parseUninstallProof(output string) (uninstallProof, error) {
	var result uninstallProof
	values := map[string]*string{"script": &result.Script, "killall": &result.Killall, "config": &result.Config, "unit": &result.Unit, "environment": &result.Environment}
	lines := strings.Split(output, "\n")
	if len(output) > 4096 || len(lines) != 7 || lines[5] != "EDO_K3S_UNINSTALL_PROOF_END" || lines[6] != "" {
		return result, ErrUninstallUnsupported
	}
	for _, line := range lines[:5] {
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

func (s *Service) probeUninstall(ctx context.Context, host model.Host, bundle sshclient.Bundle, summary UninstallSummary, configDigest string) (uninstallProof, error) {
	output, err := s.transport.Read(ctx, host, bundle, uninstallProbeScript(summary, configDigest))
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
	script, unit := officialUninstallHashes(summary.Role)
	return "edo_script=" + shellQuote(summary.ScriptPath) + "\nedo_service=" + shellQuote(summary.ServiceName) + "\nedo_data=" + shellQuote(summary.DataDir) + "\nedo_source=" + shellQuote(summary.SourceInstallationID) + "\nedo_host=" + shellQuote(summary.HostID) + "\nedo_official_script=" + shellQuote(script) + "\nedo_official_script_cn=" + shellQuote(officialCNUninstallHash(summary.Role)) + "\nedo_official_unit=" + shellQuote(unit) + "\nedo_official_killall=" + shellQuote(officialKillallSHA256) + "\n"
}

// 不创建锁、不写配置、不停止服务。缺少工具、读取失败及未知协议均阻断。
const uninstallReadOnlyGuard = `
[ "$(id -u)" = 0 ] || exit 80
[ ! -e /run/edo-k3s-install.lock ] && [ ! -L /run/edo-k3s-install.lock ] || exit 80
for edo_tool in stat sha256sum readlink findmnt ps awk grep sed systemctl ip iptables-save ip6tables-save; do command -v "$edo_tool" >/dev/null 2>&1 || exit 80; done
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
  [ "$edo_uid" = 0 ] && [ "$edo_links" = 1 ] && [ "$edo_size" -ge 0 ] && [ "$edo_size" -le 1048576 ] && [ "$((0$edo_mode & 022))" = 0 ] || return 1
  [ "$edo_size" -gt 0 ] || [ "$1" = "$edo_environment" ] || return 1
  edo_hash=$(sha256sum -- "$1") || return 1
  edo_hash=${edo_hash%% *}
  printf '%s:%s' "$edo_meta" "$edo_hash"
}
safe_path "$edo_data" || exit 80
[ -d "$edo_data" ] || exit 80
edo_real_data=$(readlink -f -- "$edo_data") || exit 80
[ "$edo_real_data" = "$edo_data" ] || exit 80
[ -d /run/systemd/system ] && [ ! -e /etc/init.d/k3s ] && [ ! -e /etc/init.d/k3s-agent ] || exit 80
for edo_init_service in /etc/init.d/k3s*; do
  [ ! -e "$edo_init_service" ] && [ ! -L "$edo_init_service" ] || exit 80
done
edo_unit=/etc/systemd/system/$edo_service.service
edo_environment=$edo_unit.env
[ ! -e /etc/rancher/k3s/config.yaml.d ] && [ ! -L /etc/rancher/k3s/config.yaml.d ] || exit 80
for edo_cli in kubectl crictl ctr; do
  if [ -L /usr/local/bin/$edo_cli ]; then
    edo_cli_target=$(readlink -- /usr/local/bin/$edo_cli) || exit 80
    case "$edo_cli_target" in k3s|/usr/local/bin/k3s) ;; *) exit 80;; esac
  fi
done
edo_fragment=$(systemctl show "$edo_service.service" --property=FragmentPath --value) || exit 80
[ "$edo_fragment" = "$edo_unit" ] || exit 80
edo_dropins=$(systemctl show "$edo_service.service" --property=DropInPaths --value) || exit 80
[ -z "$edo_dropins" ] || exit 80
for edo_dropin_dir in /etc/systemd/system/$edo_service.service.d /run/systemd/system/$edo_service.service.d /usr/lib/systemd/system/$edo_service.service.d; do
  [ ! -e "$edo_dropin_dir" ] && [ ! -L "$edo_dropin_dir" ] || exit 80
done
edo_units=$(systemctl list-unit-files --type=service --no-legend --no-pager) || exit 80
if printf '%s\n' "$edo_units" | awk -v own="$edo_service.service" '$1 ~ /^(k3s|rke2|kubelet|snap.microk8s)/ && $1 != own { found=1 } END { exit !found }'; then exit 80; else edo_status=$?; [ "$edo_status" = 1 ] || exit 80; fi
for edo_candidate in /etc/systemd/system/k3s*.service /usr/local/bin/k3s*-uninstall.sh; do
  if [ -e "$edo_candidate" ] || [ -L "$edo_candidate" ]; then
    [ "$edo_candidate" = "$edo_unit" ] || [ "$edo_candidate" = "$edo_script" ] || exit 80
  fi
done
for edo_other in /etc/rancher/rke2 /var/lib/rancher/rke2 /etc/kubernetes /var/snap/microk8s; do [ ! -e "$edo_other" ] || exit 80; done
# 官方 killall 会修改 Tailscale 路由，首版不处理这类共享网络主机。
if command -v tailscale >/dev/null 2>&1; then exit 80; fi
edo_processes=$(ps -eo comm=) || exit 80
if printf '%s\n' "$edo_processes" | grep -Eq '^(kubelet|rke2|microk8s)'; then exit 80; else edo_status=$?; [ "$edo_status" = 1 ] || exit 80; fi
edo_arguments=$(ps -eo args=) || exit 80
if printf '%s\n' "$edo_arguments" | grep -E '[/]k3s(-agent)?-(uninstall|killall)\.sh|[/]edo-k3s-install[^ ]*/install\.sh|systemctl (start|restart) k3s' >/dev/null; then exit 80; else edo_status=$?; [ "$edo_status" = 1 ] || exit 80; fi
edo_mounts=$(findmnt -rn -o TARGET) || exit 80
[ -n "$edo_mounts" ] || exit 80
overlaps() { case "$1/" in "$2/"*) return 0;; esac; case "$2/" in "$1/"*) return 0;; esac; return 1; }
protected_mount_path() {
  edo_source_path=$1
  case "$edo_source_path" in /*) ;; *) return 1;; esac
  for edo_purge in "$edo_data" /etc/rancher/k3s /run/k3s /run/flannel /var/lib/kubelet /var/lib/cni; do
    if overlaps "$edo_source_path" "$edo_purge"; then return 1; fi
  done
  for edo_prefix in /run/k3s /var/lib/kubelet/pods /var/lib/kubelet/plugins /run/netns/cni-; do
    case "$edo_source_path" in "$edo_prefix"*) return 1;; esac
    case "$edo_prefix/" in "$edo_source_path/"*) return 1;; esac
  done
}
protected_mount() {
  # 官方删除可能破坏原始 Source 的路径本身，不能只保护符号链接的解析目标。
  protected_mount_path "$1" || return 1
  edo_resolved_source=$(readlink -f -- "$1") || return 1
  protected_mount_path "$edo_resolved_source" || return 1
}
# 数据目录与卸载范围内部存在挂载时无法证明独占，绝不让官方脚本跨挂载清理。
printf '%s\n' "$edo_mounts" | while IFS= read -r edo_mount; do
  case "$edo_mount" in *'\'*|*' '*) exit 80;; esac
  for edo_purge in "$edo_data" /etc/rancher/k3s /run/k3s /run/flannel /var/lib/kubelet /var/lib/cni; do
    case "$edo_mount/" in "$edo_purge/"*) exit 80;; esac
  done
  for edo_prefix in /run/k3s /var/lib/kubelet/pods /var/lib/kubelet/plugins /run/netns/cni-; do
    case "$edo_mount" in "$edo_prefix"*) exit 80;; esac
  done
done || exit 80
# 旧安装没有网络基线，不能把其他消费者的 CNI 误认作本集群所有。
[ ! -e /var/lib/cni ] && [ ! -L /var/lib/cni ] || exit 80
edo_links=$(ip -o link show) || exit 80
if printf '%s\n' "$edo_links" | grep -Ei 'cni0|flannel|kube-ipvs0|master cni0' >/dev/null; then exit 80; else edo_status=$?; [ "$edo_status" = 1 ] || exit 80; fi
edo_netns=$(ip netns list) || exit 80
if printf '%s\n' "$edo_netns" | grep -F 'cni-' >/dev/null; then exit 80; else edo_status=$?; [ "$edo_status" = 1 ] || exit 80; fi
for edo_xtables in iptables-save ip6tables-save; do
  edo_rules=$($edo_xtables) || exit 80
  if printf '%s\n' "$edo_rules" | grep -Ei 'KUBE-|CNI-|flannel' >/dev/null; then exit 80; else edo_status=$?; [ "$edo_status" = 1 ] || exit 80; fi
done
if [ -S /var/run/docker.sock ]; then
  command -v docker >/dev/null 2>&1 || exit 80
  edo_docker_root=$(env -u DOCKER_HOST -u DOCKER_CONTEXT docker --host unix:///var/run/docker.sock info --format '{{.DockerRootDir}}') || exit 80
  protected_mount "$edo_docker_root" || exit 80
  edo_containers=$(env -u DOCKER_HOST -u DOCKER_CONTEXT docker --host unix:///var/run/docker.sock ps -aq --no-trunc) || exit 80
  for edo_container in $edo_containers; do
    case "$edo_container" in *[!a-f0-9]*|'') exit 80;; esac
    [ "${#edo_container}" = 64 ] || exit 80
    edo_sources=$(env -u DOCKER_HOST -u DOCKER_CONTEXT docker --host unix:///var/run/docker.sock inspect --format '{{range .Mounts}}{{println .Source}}{{end}}' "$edo_container") || exit 80
    printf '%s\n' "$edo_sources" | while IFS= read -r edo_mount; do [ -z "$edo_mount" ] || protected_mount "$edo_mount" || exit 80; done || exit 80
  done
elif printf '%s\n' "$edo_processes" | grep -Eq '(^|/)(dockerd|docker-proxy)$'; then exit 80
else edo_status=$?; [ "$edo_status" = 1 ] || exit 80
fi
edo_script_proof=$(file_proof "$edo_script") || exit 80
edo_killall_proof=$(file_proof /usr/local/bin/k3s-killall.sh) || exit 80
edo_config_proof=$(file_proof /etc/rancher/k3s/config.yaml) || exit 80
edo_unit_proof=$(file_proof "$edo_unit") || exit 80
edo_environment_proof=$(file_proof "$edo_environment") || exit 80
[ "${edo_script_proof##*:}" = "$edo_official_script" ] || [ "${edo_script_proof##*:}" = "$edo_official_script_cn" ] || exit 80
[ "${edo_killall_proof##*:}" = "$edo_official_killall" ] || exit 80
[ "${edo_unit_proof##*:}" = "$edo_official_unit" ] || exit 80
[ "${edo_environment_proof##*:}" = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 ] || exit 80
for edo_optional_env in /etc/default/$edo_service /etc/sysconfig/$edo_service /etc/default/$edo_service.service /etc/sysconfig/$edo_service.service; do
  safe_path "$edo_optional_env" || exit 80
  [ ! -e "$edo_optional_env" ] && [ ! -L "$edo_optional_env" ] || exit 80
done
grep -F -- "edo.dev/installation=$edo_source" /etc/rancher/k3s/config.yaml >/dev/null || exit 80
grep -F -- "edo.dev/host=$edo_host" /etc/rancher/k3s/config.yaml >/dev/null || exit 80
# 配置须匹配来源计划，只有后台生成的单行 token 不参与归属摘要。
edo_config_without_token=$(sed '/^token:/d' /etc/rancher/k3s/config.yaml) || exit 80
edo_normalized_digest=$(printf '%s\n' "$edo_config_without_token" | sha256sum) || exit 80
edo_normalized_digest=${edo_normalized_digest%% *}
[ "$edo_normalized_digest" = "$edo_expected_config" ] || exit 80
grep -F -- /etc/rancher/k3s/config.yaml "$edo_unit" >/dev/null || exit 80
grep -F -- /usr/local/bin/k3s-killall.sh "$edo_script" >/dev/null || exit 80
grep -F -- K3S_DATA_DIR "$edo_script" >/dev/null || exit 80
grep -F -- "$edo_unit" "$edo_script" >/dev/null || exit 80
grep -F -- /etc/rancher/k3s "$edo_script" >/dev/null || exit 80
grep -F -- 'remove_uninstall' "$edo_script" >/dev/null || exit 80
grep -F -- 'KUBE-' /usr/local/bin/k3s-killall.sh >/dev/null || exit 80
grep -F -- 'containerd-shim' /usr/local/bin/k3s-killall.sh >/dev/null || exit 80
`

func uninstallProbeScript(summary UninstallSummary, configDigest string) string {
	return "set -eu\n" + uninstallVariables(summary) + "edo_expected_config=" + shellQuote(configDigest) + "\n" + uninstallReadOnlyGuard + `
printf 'EDO_K3S_UNINSTALL_PROOF\tscript\t%s\n' "$edo_script_proof"
printf 'EDO_K3S_UNINSTALL_PROOF\tkillall\t%s\n' "$edo_killall_proof"
printf 'EDO_K3S_UNINSTALL_PROOF\tconfig\t%s\n' "$edo_config_proof"
printf 'EDO_K3S_UNINSTALL_PROOF\tunit\t%s\n' "$edo_unit_proof"
printf 'EDO_K3S_UNINSTALL_PROOF\tenvironment\t%s\n' "$edo_environment_proof"
printf 'EDO_K3S_UNINSTALL_PROOF_END\n'
`
}

func uninstallExecutionScript(snapshot savedUninstall) string {
	return "set -eu\n" + uninstallVariables(snapshot.Summary) + "edo_expected_config=" + shellQuote(snapshot.ConfigDigest) + "\n" + uninstallReadOnlyGuard +
		"[ \"$edo_script_proof\" = " + shellQuote(snapshot.Proof.Script) + " ] || exit 81\n" +
		"[ \"$edo_killall_proof\" = " + shellQuote(snapshot.Proof.Killall) + " ] || exit 81\n" +
		"[ \"$edo_config_proof\" = " + shellQuote(snapshot.Proof.Config) + " ] || exit 81\n" +
		"[ \"$edo_unit_proof\" = " + shellQuote(snapshot.Proof.Unit) + " ] || exit 81\n" +
		"[ \"$edo_environment_proof\" = " + shellQuote(snapshot.Proof.Environment) + " ] || exit 81\n" +
		// 固定路径、原样运行官方生成脚本，不拼装自己的停止服务或删除流程。
		"env K3S_DATA_DIR=\"$edo_data\" sh -- \"$edo_script\"\n"
}

func uninstallVerificationScript(summary UninstallSummary) string {
	return "set -eu\n" + uninstallVariables(summary) + `
[ "$(id -u)" = 0 ] || exit 82
for edo_tool in ps systemctl; do command -v "$edo_tool" >/dev/null 2>&1 || exit 82; done
for edo_marker in "$edo_script" /usr/local/bin/k3s /usr/local/bin/k3s-killall.sh /etc/rancher/k3s "$edo_data" /run/k3s /run/edo-k3s-install.lock /etc/systemd/system/k3s.service /etc/systemd/system/k3s-agent.service /etc/systemd/system/k3s.service.env /etc/systemd/system/k3s-agent.service.env; do
  [ ! -e "$edo_marker" ] && [ ! -L "$edo_marker" ] || exit 82
done
edo_units=$(systemctl list-unit-files --type=service --no-legend --no-pager) || exit 82
if printf '%s\n' "$edo_units" | awk '$1 ~ /^k3s/ { found=1 } END { exit !found }'; then exit 82; else edo_status=$?; [ "$edo_status" = 1 ] || exit 82; fi
edo_processes=$(ps -eo comm=) || exit 82
if printf '%s\n' "$edo_processes" | grep -Eq '^(k3s|k3s-server|k3s-agent)$'; then exit 82; else edo_status=$?; [ "$edo_status" = 1 ] || exit 82; fi
edo_arguments=$(ps -eo args=) || exit 82
if printf '%s\n' "$edo_arguments" | grep -F -- "$edo_data" >/dev/null; then exit 82; else edo_status=$?; [ "$edo_status" = 1 ] || exit 82; fi
edo_state=$(systemctl show "$edo_service.service" --property=LoadState --value) || exit 82
[ "$edo_state" = not-found ] || exit 82
printf 'EDO_K3S_UNINSTALL_VERIFIED\n'
`
}
