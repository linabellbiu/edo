package main

import (
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// 该模式仅用于明确授权的旧测试集群停机备份，不是通用容器运维入口。
type backupStopOptions struct {
	Cluster, ContainerID, Directory, OperationID string
	ExpectedRestartingContainerID                string
}

var (
	backupContainerID = regexp.MustCompile(`^[0-9a-f]{64}$`)
	backupPath        = regexp.MustCompile(`^/[A-Za-z0-9_.@-]+(/[A-Za-z0-9_.@-]+)*$`)
	backupLeafPrefix  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

func validateBackupStopOptions(o backupStopOptions) error {
	operation, err := uuid.Parse(o.OperationID)
	if err != nil || operation == uuid.Nil || operation.String() != o.OperationID || o.Cluster != "edo-host-link" || !backupContainerID.MatchString(o.ContainerID) {
		return errors.New("停机备份必须明确指定允许的旧测试集群、完整容器 ID 和操作 UUID")
	}
	if o.ExpectedRestartingContainerID != "" && (!backupContainerID.MatchString(o.ExpectedRestartingContainerID) || o.ExpectedRestartingContainerID == o.ContainerID) {
		return errors.New("预存重启容器必须明确指定另一个容器的完整 ID")
	}
	parent := path.Dir(o.Directory)
	mount := path.Dir(parent)
	prefix, found := strings.CutSuffix(path.Base(o.Directory), "-"+o.OperationID)
	if len(o.Directory) > 1024 || !backupPath.MatchString(o.Directory) || path.Clean(o.Directory) != o.Directory || path.Base(parent) != "edo-k3s-backups" || mount == "/" || !found || !backupLeafPrefix.MatchString(prefix) {
		return errors.New("备份目录必须是独立持久挂载点下 edo-k3s-backups 中以操作 UUID 结尾的新目录")
	}
	for _, forbidden := range []string{"/tmp", "/private/tmp", "/var/tmp", "/run", "/var/run", "/dev", "/proc", "/sys"} {
		if mount == forbidden || strings.HasPrefix(mount, forbidden+"/") {
			return errors.New("备份目录不能位于临时目录或系统虚拟文件系统")
		}
	}
	return nil
}

func buildBackupStopScript(o backupStopOptions) (string, error) {
	if err := validateBackupStopOptions(o); err != nil {
		return "", err
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	return "edo_backup_cluster=" + quote(o.Cluster) + "\nedo_backup_id=" + quote(o.ContainerID) + "\nedo_backup_directory=" + quote(o.Directory) + "\nedo_backup_operation=" + quote(o.OperationID) + "\nedo_expected_restarting_id=" + quote(o.ExpectedRestartingContainerID) + "\nedo_backup_mount=" + quote(path.Dir(path.Dir(o.Directory))) + "\n" + backupStopScript, nil
}

func writeBackupStopReport(output io.Writer, buffer *limitedBuffer, commandErr error, o backupStopOptions) error {
	if err := validateBackupStopOptions(o); err != nil {
		return err
	}
	if commandErr != nil {
		return errors.New("旧测试集群停机备份未完成，已保留现场；请勿继续安装或自动恢复")
	}
	value, exceeded := buffer.snapshot()
	if exceeded || value != "EDO_K3D_BACKUP_COMPLETE\t"+o.OperationID+"\t"+o.ContainerID+"\n" {
		return errors.New("旧测试集群备份完成凭据缺失或无效，已保留现场；请勿继续安装")
	}
	_, err := fmt.Fprintf(output, "旧容器已停止且保留；6 份归档（根文件系统 1 份、数据卷 5 份）均已通过 SHA256 校验：%s；未删除容器、网络或数据卷。\n", o.Directory)
	return err
}

// Docker export 不包含卷；停止同一容器后另行归档全部五个独占本地卷，
// Rancher 数据库与 token 保存在同一份静止卷归档中。全量元数据仅写入私有文件。
const backupStopScript = `set -eu
umask 077
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
[ "$(id -u)" = 0 ] || exit 81
for edo_tool in docker findmnt readlink stat flock du df awk sort cmp tar sha256sum sync grep mkdir chmod mv; do command -v "$edo_tool" >/dev/null 2>&1 || exit 81; done
edo_tar_binary=$(command -v tar)
edo_tar() { env -i PATH="$PATH" HOME=/var/empty "$edo_tar_binary" "$@"; }
edo_tar --version 2>/dev/null | grep -q 'GNU tar' || exit 81
edo_docker_binary=$(command -v docker)
[ -S /var/run/docker.sock ] || exit 81
edo_docker() { env -i PATH="$PATH" HOME=/var/empty DOCKER_HOST=unix:///var/run/docker.sock "$edo_docker_binary" --host=unix:///var/run/docker.sock "$@" 2>/dev/null; }
edo_fail() { printf 'EDO_K3D_BACKUP_FAILED=%s\n' "$1" >&2; exit 82; }
edo_number() { case "$1" in ''|*[!0-9]*) return 1;; esac; [ "${#1}" -le 14 ]; }
edo_safe_path() { case "$1" in /*) :;; *) return 1;; esac; case "$1" in *[!A-Za-z0-9_./@-]*|*/../*|*/./*|*/..|*/.|*//*) return 1;; esac; }
edo_safe_name() { case "$1" in ''|*[!A-Za-z0-9_.-]*|.*|-*) return 1;; esac; [ "${#1}" -le 128 ]; }
[ "$(readlink -f "$edo_backup_mount")" = "$edo_backup_mount" ] || edo_fail mount_path
[ "$(findmnt --mountpoint "$edo_backup_mount" --noheadings --output TARGET 2>/dev/null)" = "$edo_backup_mount" ] || edo_fail persistent_mount
edo_fs=$(findmnt --mountpoint "$edo_backup_mount" --noheadings --output FSTYPE 2>/dev/null) || edo_fail persistent_mount
case "$edo_fs" in ext4|xfs|btrfs|zfs) :;; *) edo_fail persistent_filesystem;; esac
edo_backup_parent=${edo_backup_directory%/*}
[ ! -L "$edo_backup_parent" ] || edo_fail backup_parent
if [ ! -e "$edo_backup_parent" ]; then mkdir -m 700 -- "$edo_backup_parent"; fi
[ -d "$edo_backup_parent" ] && [ "$(readlink -f "$edo_backup_parent")" = "$edo_backup_parent" ] && [ "$(stat -c '%u:%a' "$edo_backup_parent")" = 0:700 ] || edo_fail backup_parent
[ ! -e "$edo_backup_directory" ] && [ ! -L "$edo_backup_directory" ] || edo_fail existing_backup
[ ! -L "$edo_backup_parent/.edo-k3d-backup.lock" ] || edo_fail backup_lock
exec 9>"$edo_backup_parent/.edo-k3d-backup.lock"
flock -n 9 || edo_fail backup_busy

edo_target_name="k3d-$edo_backup_cluster-server-0"
edo_target_template='{{.Id}}|{{.Name}}|{{index .Config.Labels "app"}}|{{index .Config.Labels "k3d.cluster"}}|{{index .Config.Labels "k3d.role"}}|{{.State.Running}}|{{.HostConfig.AutoRemove}}'
edo_expected_identity="$edo_backup_id|/$edo_target_name|k3d|$edo_backup_cluster|server|true|false"
[ "$(edo_docker inspect --type container --format "$edo_target_template" "$edo_backup_id")" = "$edo_expected_identity" ] || edo_fail container_identity
[ "$(edo_docker ps -a --no-trunc --quiet --filter "label=k3d.cluster=$edo_backup_cluster")" = "$edo_backup_id" ] || edo_fail cluster_not_exclusive
edo_network=$(edo_docker inspect --type container --format '{{range $name,$network := .NetworkSettings.Networks}}{{printf "%s|%s\n" $name $network.NetworkID}}{{end}}' "$edo_backup_id") || edo_fail network_identity
edo_network_name=${edo_network%%|*}
edo_network_id=${edo_network#*|}
[ "$edo_network_name" = "k3d-$edo_backup_cluster" ] && [ "${#edo_network_id}" = 64 ] || edo_fail network_identity
case "$edo_network_id" in *[!0-9a-f]*) edo_fail network_identity;; esac
[ "$(edo_docker network inspect --format '{{.Id}}|{{.Name}}' "$edo_network_id")" = "$edo_network_id|$edo_network_name" ] || edo_fail network_identity
[ "$(edo_docker network inspect --format '{{range $id,$container := .Containers}}{{printf "%s\n" $id}}{{end}}' "$edo_network_id")" = "$edo_backup_id" ] || edo_fail shared_network
edo_docker_root=$(edo_docker info --format '{{.DockerRootDir}}') || edo_fail docker_root
edo_safe_path "$edo_docker_root" && [ "$edo_docker_root" != / ] && [ "$(readlink -f "$edo_docker_root")" = "$edo_docker_root" ] || edo_fail docker_root
edo_mount_template='{{range .Mounts}}{{printf "%s|%s|%s|%s|%t\n" .Type .Name .Source .Destination .RW}}{{end}}'
edo_mounts=$(edo_docker inspect --type container --format "$edo_mount_template" "$edo_backup_id") || edo_fail mounts
edo_seen='|'
edo_volume_records=
edo_volume_count=0
edo_bind_count=0
edo_rancher_source=
while IFS='|' read -r edo_type edo_name edo_source edo_destination edo_rw; do
  case "$edo_type" in
    bind)
      [ -z "$edo_name" ] && [ "$edo_rw" = false ] || edo_fail unexpected_bind
      case "$edo_destination" in
        /sys/devices/system/edac) edo_bind_file=edac;;
        /proc/meminfo) edo_bind_file=meminfo;;
        *) edo_fail unexpected_bind;;
      esac
      [ "$edo_source" = "/root/.config/k3d/.$edo_target_name/$edo_bind_file" ] || edo_fail unexpected_bind
      case "$edo_seen" in *"|$edo_destination|"*) edo_fail duplicate_mount;; esac
      edo_seen="$edo_seen$edo_destination|"
      edo_bind_count=$((edo_bind_count + 1));;
    volume)
      edo_safe_name "$edo_name" && [ "$edo_rw" = true ] || edo_fail volume_name
      case "$edo_destination" in /var/lib/rancher/k3s|/var/lib/cni|/var/lib/kubelet|/var/log|/k3d/images) :;; *) edo_fail unexpected_volume;; esac
      case "$edo_seen" in *"|$edo_destination|"*|*"|$edo_name|"*) edo_fail duplicate_mount;; esac
      edo_seen="$edo_seen$edo_destination|$edo_name|"
      [ "$edo_source" = "$edo_docker_root/volumes/$edo_name/_data" ] && [ -d "$edo_source" ] && [ "$(readlink -f "$edo_source")" = "$edo_source" ] || edo_fail volume_source
      edo_volume_identity=$(edo_docker volume inspect --format '{{.Name}}|{{.Driver}}|{{.Scope}}|{{.Mountpoint}}|{{json .Options}}' "$edo_name") || edo_fail volume_identity
      case "$edo_volume_identity" in "$edo_name|local|local|$edo_source|null"|"$edo_name|local|local|$edo_source|{}") :;; *) edo_fail volume_identity;; esac
      [ "$(edo_docker ps -a --no-trunc --quiet --filter "volume=$edo_name")" = "$edo_backup_id" ] || edo_fail shared_volume
      edo_volume_records="$edo_volume_records$edo_name|$edo_source|$edo_destination
"
      [ "$edo_destination" != /var/lib/rancher/k3s ] || edo_rancher_source=$edo_source
      edo_volume_count=$((edo_volume_count + 1));;
    *) edo_fail unexpected_mount;;
  esac
done <<EDO_MOUNTS
$edo_mounts
EDO_MOUNTS
[ "$edo_volume_count" = 5 ] && [ "$edo_bind_count" = 2 ] || edo_fail mount_count

edo_other_ids() {
  edo_all_ids=$(edo_docker ps -a --no-trunc --quiet) || return 1
  edo_sorted_ids=$(printf '%s\n' "$edo_all_ids" | sort) || return 1
  while IFS= read -r edo_other_id; do [ "$edo_other_id" = "$edo_backup_id" ] || printf '%s\n' "$edo_other_id"; done <<EDO_SORTED_IDS
$edo_sorted_ids
EDO_SORTED_IDS
}
edo_check_other_mounts() {
edo_others=$(edo_other_ids) || edo_fail other_containers
while IFS= read -r edo_other_id; do
  [ -n "$edo_other_id" ] || continue
  [ "${#edo_other_id}" = 64 ] || edo_fail other_containers
  case "$edo_other_id" in *[!0-9a-f]*) edo_fail other_containers;; esac
  edo_other_networks=$(edo_docker inspect --type container --format '{{range $name,$network := .NetworkSettings.Networks}}{{printf "%s|%s\n" $name $network.NetworkID}}{{end}}' "$edo_other_id") || edo_fail other_networks
  while IFS='|' read -r edo_other_network_name edo_other_network_id; do
    [ "$edo_other_network_name" != "$edo_network_name" ] && [ "$edo_other_network_id" != "$edo_network_id" ] || edo_fail shared_network_configuration
  done <<EDO_OTHER_NETWORKS
$edo_other_networks
EDO_OTHER_NETWORKS
  edo_other_sources=$(edo_docker inspect --type container --format '{{range .Mounts}}{{printf "%s\n" .Source}}{{end}}' "$edo_other_id") || edo_fail other_mounts
  while IFS= read -r edo_other_source; do
    [ -n "$edo_other_source" ] || continue
    edo_other_source=$(readlink -f "$edo_other_source") || edo_fail other_mounts
    while IFS='|' read -r edo_name edo_source edo_destination; do
      [ -n "$edo_name" ] || continue
      case "$edo_source/" in "$edo_other_source/"*) edo_fail shared_volume_source;; esac
      case "$edo_other_source/" in "$edo_source/"*) edo_fail shared_volume_source;; esac
    done <<EDO_VOLUMES
$edo_volume_records
EDO_VOLUMES
  done <<EDO_OTHER_SOURCES
$edo_other_sources
EDO_OTHER_SOURCES
done <<EDO_OTHERS
$edo_others
EDO_OTHERS
}
edo_check_other_mounts

edo_check_rancher_data() {
  [ -n "$edo_rancher_source" ] || edo_fail rancher_source
  for edo_relative in server server/db; do
    edo_required_path="$edo_rancher_source/$edo_relative"
    [ -d "$edo_required_path" ] && [ ! -L "$edo_required_path" ] && [ "$(readlink -f "$edo_required_path")" = "$edo_required_path" ] || edo_fail rancher_directory
  done
  for edo_relative in server/token server/db/state.db; do
    edo_required_path="$edo_rancher_source/$edo_relative"
    [ -f "$edo_required_path" ] && [ ! -L "$edo_required_path" ] && [ -s "$edo_required_path" ] && [ "$(readlink -f "$edo_required_path")" = "$edo_required_path" ] && [ "$(stat -c '%h' "$edo_required_path")" = 1 ] || edo_fail rancher_file
  done
  for edo_relative in server/db/state.db-wal server/db/state.db-shm; do
    edo_required_path="$edo_rancher_source/$edo_relative"
    if [ -e "$edo_required_path" ] || [ -L "$edo_required_path" ]; then
      [ -f "$edo_required_path" ] && [ ! -L "$edo_required_path" ] && [ "$(readlink -f "$edo_required_path")" = "$edo_required_path" ] && [ "$(stat -c '%h' "$edo_required_path")" = 1 ] || edo_fail rancher_file
    fi
  done
}
edo_check_rancher_data
edo_no_nested_mounts() {
  edo_mount_targets=$(findmnt --list --noheadings --output TARGET) || edo_fail nested_mounts
  while IFS= read -r edo_mount_target; do
    case "$edo_mount_target/" in "$1/"*) edo_fail nested_mounts;; esac
  done <<EDO_MOUNT_TARGETS
$edo_mount_targets
EDO_MOUNT_TARGETS
}
edo_size=$(edo_docker inspect --size --type container --format '{{.SizeRootFs}}' "$edo_backup_id") || edo_fail container_size
edo_number "$edo_size" || edo_fail container_size
edo_required=$((edo_size / 1024 + 1))
while IFS='|' read -r edo_name edo_source edo_destination; do
  [ -n "$edo_name" ] || continue
  edo_no_nested_mounts "$edo_source"
  edo_du=$(du -sk -- "$edo_source" 2>/dev/null) || edo_fail volume_size
  edo_size=$(printf '%s\n' "$edo_du" | awk 'NR == 1 { print $1 }') || edo_fail volume_size
  edo_number "$edo_size" || edo_fail volume_size
  edo_required=$((edo_required + edo_size))
done <<EDO_VOLUMES
$edo_volume_records
EDO_VOLUMES
edo_required=$((edo_required * 2 + 262144))
edo_df=$(df -Pk -- "$edo_backup_mount") || edo_fail disk_space
edo_available=$(printf '%s\n' "$edo_df" | awk 'NR == 2 { print $4 }') || edo_fail disk_space
edo_number "$edo_available" && [ "$edo_available" -ge "$edo_required" ] || edo_fail disk_space

mkdir -m 700 -- "$edo_backup_directory"
exec 2>"$edo_backup_directory/private-errors.log"
edo_docker inspect --size --type container "$edo_backup_id" > "$edo_backup_directory/container-before.json"
edo_docker network inspect "$edo_network_id" > "$edo_backup_directory/network.json"
edo_docker version --format '{{json .}}' > "$edo_backup_directory/docker-version.json"
printf 'operation=%s\ncluster=%s\ncontainer=%s\nnetwork=%s\n' "$edo_backup_operation" "$edo_backup_cluster" "$edo_backup_id" "$edo_network_id" > "$edo_backup_directory/identity.txt"
printf '%s' "$edo_volume_records" > "$edo_backup_directory/volume-map.txt"
edo_other_snapshot() {
  edo_snapshot_ids=$(edo_other_ids) || return 1
  while IFS= read -r edo_other_id; do
    [ -n "$edo_other_id" ] || continue
    edo_docker inspect --type container --format '{{.Id}}|{{json .Name}}|{{.State.Pid}}|{{.State.StartedAt}}|{{.RestartCount}}|{{.State.Status}}|{{json .HostConfig.NetworkMode}}|{{json .Mounts}}|{{range $name,$network := .NetworkSettings.Networks}}{{printf "%q," $name}}{{end}}' "$edo_other_id" || exit 83
  done <<EDO_SNAPSHOT_IDS
$edo_snapshot_ids
EDO_SNAPSHOT_IDS
}
edo_other_snapshot > "$edo_backup_directory/other-containers-before.txt"
# 只对操作者明确确认且初始正在重启的同一 ID 豁免自然运行时变化；
# 原始快照保留，身份、存在性及卷/网络配置仍在规范化快照中严格比较。
edo_expected_restart_count=0
if [ -n "$edo_expected_restarting_id" ]; then
  edo_expected_restart_count=$(awk -F '|' -v expected="$edo_expected_restarting_id" '$1 == expected {print $5}' "$edo_backup_directory/other-containers-before.txt") || edo_fail restarting_baseline
  edo_number "$edo_expected_restart_count" && [ "$edo_expected_restart_count" -gt 0 ] || edo_fail restarting_baseline
fi
edo_normalize_other_snapshot() {
  awk -F '|' -v OFS='|' -v expected="$edo_expected_restarting_id" -v phase="$1" -v minimum="$edo_expected_restart_count" '
    $1 == expected && expected != "" {
      seen++
      if (NF < 9 || $3 !~ /^[0-9]+$/ || $5 !~ /^[0-9]+$/ || $5 + 0 < minimum + 0) exit 1
      if (phase == "before" && $6 != "restarting") exit 1
      if (phase == "after" && $6 != "restarting" && $6 != "running") exit 1
      $3 = "expected-restarting"; $4 = "expected-restarting"; $5 = "expected-restarting"; $6 = "expected-restarting"
    }
    { print }
    END { if (expected != "" && seen != 1) exit 1 }
  ' "$2"
}
edo_normalize_other_snapshot before "$edo_backup_directory/other-containers-before.txt" > "$edo_backup_directory/other-containers-guard-before.txt" || edo_fail restarting_baseline
while IFS='|' read -r edo_name edo_source edo_destination; do
  [ -n "$edo_name" ] || continue
  edo_docker volume inspect "$edo_name" > "$edo_backup_directory/volume-$edo_name.json"
done <<EDO_VOLUMES
$edo_volume_records
EDO_VOLUMES

# 紧邻停机前再次核对身份、挂载及网络独占性；只停止明确的完整 ID。
[ "$(edo_docker inspect --type container --format "$edo_target_template" "$edo_backup_id")" = "$edo_expected_identity" ] || edo_fail changed_identity
[ "$(edo_docker inspect --type container --format "$edo_mount_template" "$edo_backup_id")" = "$edo_mounts" ] || edo_fail changed_mounts
[ "$(edo_docker ps -a --no-trunc --quiet --filter "label=k3d.cluster=$edo_backup_cluster")" = "$edo_backup_id" ] || edo_fail changed_cluster
[ "$(edo_docker network inspect --format '{{range $id,$container := .Containers}}{{printf "%s\n" $id}}{{end}}' "$edo_network_id")" = "$edo_backup_id" ] || edo_fail changed_network
edo_check_other_mounts
edo_check_rancher_data
edo_docker stop --time 60 "$edo_backup_id" > "$edo_backup_directory/stop-result.txt"
[ "$(edo_docker inspect --type container --format '{{.Id}}|{{.State.Running}}|{{.State.Pid}}|{{.HostConfig.AutoRemove}}' "$edo_backup_id")" = "$edo_backup_id|false|0|false" ] || edo_fail not_stopped
edo_docker inspect --size --type container "$edo_backup_id" > "$edo_backup_directory/container-stopped.json"
edo_check_other_mounts
edo_check_rancher_data
edo_docker export --output "$edo_backup_directory/container-rootfs.tar" "$edo_backup_id"
edo_tar --list --file "$edo_backup_directory/container-rootfs.tar" > /dev/null
while IFS='|' read -r edo_name edo_source edo_destination; do
  [ -n "$edo_name" ] || continue
  [ "$(edo_docker ps -a --no-trunc --quiet --filter "volume=$edo_name")" = "$edo_backup_id" ] || edo_fail changed_volume_owner
  [ "$(readlink -f "$edo_source")" = "$edo_source" ] || edo_fail changed_volume_source
  edo_no_nested_mounts "$edo_source"
  edo_tar --acls --xattrs --numeric-owner --sparse --create --file "$edo_backup_directory/volume-$edo_name.tar" --directory "$edo_source" .
  edo_tar --list --file "$edo_backup_directory/volume-$edo_name.tar" > /dev/null
  edo_tar --acls --xattrs --numeric-owner --compare --file "$edo_backup_directory/volume-$edo_name.tar" --directory "$edo_source" .
  if [ "$edo_destination" = /var/lib/rancher/k3s ]; then
    edo_tar --list --file "$edo_backup_directory/volume-$edo_name.tar" > "$edo_backup_directory/rancher-archive-entries.txt"
    grep -F -x './server/token' "$edo_backup_directory/rancher-archive-entries.txt" > /dev/null || edo_fail rancher_archive_token
    grep -F -x './server/db/state.db' "$edo_backup_directory/rancher-archive-entries.txt" > /dev/null || edo_fail rancher_archive_database
    edo_tar --acls --xattrs --numeric-owner --compare --file "$edo_backup_directory/volume-$edo_name.tar" --directory "$edo_source" -- ./server/token ./server/db/state.db
  fi
done <<EDO_VOLUMES
$edo_volume_records
EDO_VOLUMES
[ "$(edo_docker inspect --type container --format '{{.Id}}|{{.State.Running}}|{{.State.Pid}}|{{.HostConfig.AutoRemove}}' "$edo_backup_id")" = "$edo_backup_id|false|0|false" ] || edo_fail restarted_target
edo_other_snapshot > "$edo_backup_directory/other-containers-after.txt"
edo_normalize_other_snapshot after "$edo_backup_directory/other-containers-after.txt" > "$edo_backup_directory/other-containers-guard-after.txt" || edo_fail restarting_after
cmp -s "$edo_backup_directory/other-containers-guard-before.txt" "$edo_backup_directory/other-containers-guard-after.txt" || edo_fail other_containers_changed
cd -- "$edo_backup_directory"
sha256sum -- *.tar *.json *.txt > SHA256SUMS
sha256sum --check --status SHA256SUMS
chmod 600 ./*
sync
printf 'operation=%s\ncontainer=%s\nstatus=complete\narchives=6\nsha256=verified\n' "$edo_backup_operation" "$edo_backup_id" > COMPLETE.pending
chmod 600 COMPLETE.pending
sync
mv -- COMPLETE.pending COMPLETE
sync
printf 'EDO_K3D_BACKUP_COMPLETE\t%s\t%s\n' "$edo_backup_operation" "$edo_backup_id"
`
