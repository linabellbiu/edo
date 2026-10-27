package main

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// 仅停止明确核验的旧测试容器，不归档、不删除，也不安装新集群。
type stopOnlyOptions struct {
	Cluster, ContainerID, ExpectedRestartingContainerID string
}

type k3dStopError struct {
	reason   string
	exitCode int
}

func (*k3dStopError) Error() string {
	return "旧测试集群停止流程未完成，请先核对容器状态；不得继续安装、删除或自动恢复"
}

func validateStopOnlyOptions(o stopOnlyOptions) error {
	if o.Cluster != "edo-host-link" || !backupContainerID.MatchString(o.ContainerID) {
		return errors.New("仅停机模式必须明确指定允许的旧测试集群和完整容器 ID")
	}
	if o.ExpectedRestartingContainerID != "" && (!backupContainerID.MatchString(o.ExpectedRestartingContainerID) || o.ExpectedRestartingContainerID == o.ContainerID) {
		return errors.New("预存重启容器必须明确指定另一个容器的完整 ID")
	}
	return nil
}

func buildStopOnlyScript(o stopOnlyOptions) (string, error) {
	if err := validateStopOnlyOptions(o); err != nil {
		return "", err
	}
	// 三个值均已限制为固定集群名或十六进制 ID，不能注入命令。
	return "edo_stop_cluster='" + o.Cluster + "'\nedo_stop_id='" + o.ContainerID + "'\nedo_expected_restarting_id='" + o.ExpectedRestartingContainerID + "'\n" + stopOnlyScript, nil
}

func validStopOnlyFailure(reason string) bool {
	switch reason {
	case "prerequisites", "container_identity", "cluster_not_exclusive", "target_configuration", "other_containers", "other_snapshot", "restarting_baseline", "changed_identity", "changed_cluster", "changed_target_configuration", "other_containers_changed_before_stop", "stop_failed", "not_stopped", "changed_target_after_stop", "restarting_after", "other_containers_changed", "snapshot_digest", "invalid_receipt":
		return true
	}
	return false
}

func stopOnlyFailure(diagnostics *limitedBuffer, exitCode int) *k3dStopError {
	reason := "unknown"
	value, exceeded := diagnostics.snapshot()
	if !exceeded {
		seen := false
		for _, line := range strings.Split(value, "\n") {
			current, ok := strings.CutPrefix(line, "EDO_K3D_STOP_FAILED=")
			if !ok {
				continue
			}
			if seen || !validStopOnlyFailure(current) {
				return &k3dStopError{reason: "unknown", exitCode: exitCode}
			}
			reason, seen = current, true
		}
	}
	return &k3dStopError{reason: reason, exitCode: exitCode}
}

func writeStopOnlyReport(output io.Writer, buffer, diagnostics *limitedBuffer, commandErr error, o stopOnlyOptions) error {
	if err := validateStopOnlyOptions(o); err != nil {
		return err
	}
	if commandErr != nil {
		return stopOnlyFailure(diagnostics, preflightExitCode(commandErr))
	}
	value, exceeded := buffer.snapshot()
	fields := strings.Split(strings.TrimSuffix(value, "\n"), "\t")
	if exceeded || !strings.HasSuffix(value, "\n") || len(fields) != 4 || fields[0] != "EDO_K3D_STOP_COMPLETE" || fields[1] != o.ContainerID {
		return &k3dStopError{reason: "invalid_receipt", exitCode: 0}
	}
	count, err := strconv.Atoi(fields[2])
	if err != nil || count < 0 || strconv.Itoa(count) != fields[2] || !backupContainerID.MatchString(fields[3]) {
		return &k3dStopError{reason: "invalid_receipt", exitCode: 0}
	}
	_, err = fmt.Fprintf(output, "旧测试集群容器已停止且保留；其他 %d 个容器的身份、配置及运行基线核验一致（明确确认的既存重启容器仅豁免自然运行时变化）。基线 SHA256：%s。未备份、未删除容器/网络/数据卷，未安装新集群。\n", count, fields[3])
	return err
}

const stopOnlyScript = `set -eu
umask 077
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
edo_fail() { printf 'EDO_K3D_STOP_FAILED=%s\n' "$1" >&2; exit 82; }
[ "$(id -u)" = 0 ] || edo_fail prerequisites
for edo_tool in docker sort awk sha256sum; do command -v "$edo_tool" >/dev/null 2>&1 || edo_fail prerequisites; done
[ -S /var/run/docker.sock ] || edo_fail prerequisites
edo_docker_binary=$(command -v docker)
edo_docker() { env -i PATH="$PATH" HOME=/var/empty DOCKER_HOST=unix:///var/run/docker.sock "$edo_docker_binary" --host=unix:///var/run/docker.sock "$@" 2>/dev/null; }
edo_valid_id() { [ "${#1}" = 64 ] || return 1; case "$1" in *[!0-9a-f]*) return 1;; esac; }
edo_target_template='{{.Id}}|{{.Name}}|{{index .Config.Labels "app"}}|{{index .Config.Labels "k3d.cluster"}}|{{index .Config.Labels "k3d.role"}}|{{.State.Running}}|{{.HostConfig.AutoRemove}}'
edo_running_identity="$edo_stop_id|/k3d-$edo_stop_cluster-server-0|k3d|$edo_stop_cluster|server|true|false"
edo_stopped_identity="$edo_stop_id|/k3d-$edo_stop_cluster-server-0|k3d|$edo_stop_cluster|server|false|false"
[ "$(edo_docker inspect --type container --format "$edo_target_template" "$edo_stop_id")" = "$edo_running_identity" ] || edo_fail container_identity
[ "$(edo_docker ps -a --no-trunc --quiet --filter "label=k3d.cluster=$edo_stop_cluster")" = "$edo_stop_id" ] || edo_fail cluster_not_exclusive
edo_configuration_template='{{.Id}}|{{json .Name}}|{{json .Config.Labels}}|{{.HostConfig.AutoRemove}}|{{json .HostConfig.RestartPolicy}}|{{json .HostConfig.NetworkMode}}|{{json .Mounts}}'
edo_target_configuration=$(edo_docker inspect --type container --format "$edo_configuration_template" "$edo_stop_id") || edo_fail target_configuration
edo_other_ids() {
  edo_ids=$(edo_docker ps -a --no-trunc --quiet) || return 1
  edo_sorted_ids=$(printf '%s\n' "$edo_ids" | sort) || return 1
  edo_seen_target=0
  while IFS= read -r edo_id; do
    [ -n "$edo_id" ] || continue
    edo_valid_id "$edo_id" || return 1
    if [ "$edo_id" = "$edo_stop_id" ]; then edo_seen_target=$((edo_seen_target + 1)); else printf '%s\n' "$edo_id"; fi
  done <<EDO_IDS
$edo_sorted_ids
EDO_IDS
  [ "$edo_seen_target" = 1 ]
}
edo_other_snapshot() {
  edo_snapshot_ids=$(edo_other_ids) || return 1
  while IFS= read -r edo_other_id; do
    [ -n "$edo_other_id" ] || continue
    edo_docker inspect --type container --format '{{.Id}}|{{json .Name}}|{{.State.Pid}}|{{.State.StartedAt}}|{{.RestartCount}}|{{.State.Status}}|{{json .HostConfig.NetworkMode}}|{{json .Mounts}}|{{range $name,$network := .NetworkSettings.Networks}}{{printf "%q:%s," $name $network.NetworkID}}{{end}}' "$edo_other_id" || return 1
  done <<EDO_OTHER_IDS
$edo_snapshot_ids
EDO_OTHER_IDS
}
edo_normalize_snapshot() {
  awk -F '|' -v OFS='|' -v expected="$edo_expected_restarting_id" -v phase="$1" -v minimum="$edo_expected_restart_count" '
    NF {
      if (NF < 9 || length($1) != 64 || $1 !~ /^[0-9a-f]+$/ || $3 !~ /^[0-9]+$/ || $5 !~ /^[0-9]+$/) exit 1
      if (phase == "before" && $6 == "restarting" && $1 != expected) exit 1
    }
    $1 == expected && expected != "" {
      seen++
      if ($5 + 0 < minimum + 0 || $5 + 0 < 1) exit 1
      if (phase == "before" && $6 != "restarting") exit 1
      if (phase != "before" && $6 != "restarting" && $6 != "running") exit 1
      $3 = "expected-restarting"; $4 = "expected-restarting"; $5 = "expected-restarting"; $6 = "expected-restarting"
    }
    NF {print}
    END {if (expected != "" && seen != 1) exit 1}
  '
}
edo_other_raw_before=$(edo_other_snapshot) || edo_fail other_snapshot
edo_expected_restart_count=0
if [ -n "$edo_expected_restarting_id" ]; then
  edo_expected_restart_count=$(printf '%s\n' "$edo_other_raw_before" | awk -F '|' -v expected="$edo_expected_restarting_id" '$1 == expected {print $5}') || edo_fail restarting_baseline
  case "$edo_expected_restart_count" in ''|*[!0-9]*) edo_fail restarting_baseline;; esac
fi
edo_other_before=$(printf '%s\n' "$edo_other_raw_before" | edo_normalize_snapshot before) || edo_fail restarting_baseline
# 紧邻唯一一次停机再次核验，其他容器的原始投影只保存在当前 Shell 内存。
[ "$(edo_docker inspect --type container --format "$edo_target_template" "$edo_stop_id")" = "$edo_running_identity" ] || edo_fail changed_identity
[ "$(edo_docker ps -a --no-trunc --quiet --filter "label=k3d.cluster=$edo_stop_cluster")" = "$edo_stop_id" ] || edo_fail changed_cluster
[ "$(edo_docker inspect --type container --format "$edo_configuration_template" "$edo_stop_id")" = "$edo_target_configuration" ] || edo_fail changed_target_configuration
edo_other_raw_recheck=$(edo_other_snapshot) || edo_fail other_snapshot
edo_other_recheck=$(printf '%s\n' "$edo_other_raw_recheck" | edo_normalize_snapshot recheck) || edo_fail restarting_baseline
[ "$edo_other_recheck" = "$edo_other_before" ] || edo_fail other_containers_changed_before_stop
edo_docker stop --time 60 "$edo_stop_id" >/dev/null || edo_fail stop_failed
[ "$(edo_docker inspect --type container --format "$edo_target_template" "$edo_stop_id")" = "$edo_stopped_identity" ] || edo_fail not_stopped
[ "$(edo_docker inspect --type container --format '{{.State.Pid}}' "$edo_stop_id")" = 0 ] || edo_fail not_stopped
[ "$(edo_docker inspect --type container --format "$edo_configuration_template" "$edo_stop_id")" = "$edo_target_configuration" ] || edo_fail changed_target_after_stop
[ "$(edo_docker ps -a --no-trunc --quiet --filter "label=k3d.cluster=$edo_stop_cluster")" = "$edo_stop_id" ] || edo_fail changed_cluster
edo_other_raw_after=$(edo_other_snapshot) || edo_fail other_snapshot
edo_other_after=$(printf '%s\n' "$edo_other_raw_after" | edo_normalize_snapshot after) || edo_fail restarting_after
[ "$edo_other_after" = "$edo_other_before" ] || edo_fail other_containers_changed
edo_count=$(printf '%s\n' "$edo_other_after" | awk 'NF {count++} END {print count+0}') || edo_fail snapshot_digest
edo_digest_line=$(printf '%s\n' "$edo_other_after" | sha256sum) || edo_fail snapshot_digest
edo_digest=${edo_digest_line%% *}
edo_valid_id "$edo_digest" || edo_fail snapshot_digest
printf 'EDO_K3D_STOP_COMPLETE\t%s\t%s\t%s\n' "$edo_stop_id" "$edo_count" "$edo_digest"
`
