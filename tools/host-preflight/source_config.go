package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
)

const nativeChinaRegistry = "registry.cn-hangzhou.aliyuncs.com"

type nativeSourceBinding struct {
	InstallationID, ClusterID, HostID, Version, DataDir, NodeName, Digest string
}

type nativeSourceError struct {
	stage string
	code  int
}

func (*nativeSourceError) Error() string {
	return "原生集群镜像源维护未完成，请先核对实际配置、服务及系统组件状态；不会修改原安装任务"
}

func validNativeSourceOptions(o options) error {
	for _, id := range []string{o.nativeInstallationID, o.nativeClusterID} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil || parsed.String() != id {
			return errors.New("请明确提供有效的原生安装任务和集群 UUID")
		}
	}
	if o.backupStopK3d || o.stopOnlyK3d || o.testSudo || o.k3dCluster != "" || o.backup.ContainerID != "" || o.backup.Directory != "" || o.backup.OperationID != "" {
		return errors.New("原生镜像源维护不能与其他运维模式或临时 sudo 授权混用")
	}
	if o.backup.ExpectedRestartingContainerID != "" && !backupContainerID.MatchString(o.backup.ExpectedRestartingContainerID) {
		return errors.New("预存重启容器 ID 无效")
	}
	return nil
}

func loadNativeSourceBinding(ctx context.Context, o options, cfg config.Database, key string, expectedHost model.Host) (nativeSourceBinding, error) {
	var result nativeSourceBinding
	db, err := database.Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return result, &nativeSourceError{stage: "database", code: -1}
	}
	defer database.Close(db)
	tx := db.WithContext(ctx).Begin(&sql.TxOptions{ReadOnly: true})
	if tx.Error != nil {
		return result, &nativeSourceError{stage: "database", code: -1}
	}
	defer tx.Rollback()
	var record model.K3sInstallation
	var cluster model.KubernetesCluster
	var current model.Host
	var capability model.HostCapability
	if tx.Where(&model.K3sInstallation{ID: o.nativeInstallationID}).First(&record).Error != nil || tx.Where(&model.KubernetesCluster{ID: o.nativeClusterID}).First(&cluster).Error != nil || tx.Where(&model.Host{ID: o.hostID}).First(&current).Error != nil || tx.Where("host_id = ? AND kind = ?", o.hostID, model.HostCapabilityKubernetes).First(&capability).Error != nil {
		return result, &nativeSourceError{stage: "binding", code: -1}
	}
	if record.Action != "create" || (record.Status != "failed" && record.Status != "interrupted") || record.ClusterID != cluster.ID || !cluster.IsActive || record.ProjectID != current.ProjectID || record.DepartmentID != current.DepartmentID || cluster.ProjectID != current.ProjectID || cluster.DepartmentID != current.DepartmentID || capability.RuntimeID != cluster.ID || current.SSHCredentialCiphertext != expectedHost.SSHCredentialCiphertext || current.SSHHostKeyFingerprint != o.fingerprint || current.Address != o.address || current.SSHUsername != o.username || current.SSHPort != o.port || !current.IsActive || current.Mode != model.HostModeSSH || current.IsBuiltin {
		return result, &nativeSourceError{stage: "binding", code: -1}
	}
	var locks int64
	if tx.Model(&model.K3sInstallationHostLock{}).Where("host_id = ?", o.hostID).Count(&locks).Error != nil || locks != 0 {
		return result, &nativeSourceError{stage: "busy", code: -1}
	}
	manager, err := secret.New(key)
	if err != nil {
		return result, &nativeSourceError{stage: "snapshot", code: -1}
	}
	plain, err := manager.Decrypt(record.InputCiphertext, []byte("k3s-installation:"+record.ID))
	if err != nil {
		return result, &nativeSourceError{stage: "snapshot", code: -1}
	}
	// 下划线字段显式读取，避免 Go 名称推断意外接受不匹配主机。
	var envelope struct {
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal([]byte(plain), &envelope) != nil {
		return result, &nativeSourceError{stage: "snapshot", code: -1}
	}
	var input struct {
		Action        string         `json:"action"`
		Mode          string         `json:"mode"`
		ServerHostID  string         `json:"server_host_id"`
		Endpoint      string         `json:"endpoint"`
		ServerOptions map[string]any `json:"server_options"`
		Installer     map[string]any `json:"installer"`
	}
	if json.Unmarshal(envelope.Input, &input) != nil {
		return result, &nativeSourceError{stage: "snapshot", code: -1}
	}
	version, _ := input.Installer["INSTALL_K3S_VERSION"].(string)
	dataDir, _ := input.ServerOptions["data-dir"].(string)
	nodeName, _ := input.ServerOptions["node-name"].(string)
	endpoint, parseErr := url.Parse(input.Endpoint)
	if input.Action != "create" || (input.Mode != "" && input.Mode != "native") || input.ServerHostID != o.hostID || !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+\+k3s[0-9]+$`).MatchString(version) || dataDir == "" || !path.IsAbs(dataDir) || path.Clean(dataDir) != dataDir || dataDir == "/" || !regexp.MustCompile(`^/[A-Za-z0-9_./@-]+$`).MatchString(dataDir) || !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,252}$`).MatchString(nodeName) || parseErr != nil || endpoint.Scheme != "https" || endpoint.Hostname() != o.address || endpoint.User != nil || strings.TrimRight(cluster.APIServer, "/") != strings.TrimRight(input.Endpoint, "/") {
		return result, &nativeSourceError{stage: "snapshot", code: -1}
	}
	for name, allowed := range map[string]string{"INSTALL_K3S_BIN_DIR": "/usr/local/bin", "INSTALL_K3S_SYSTEMD_DIR": "/etc/systemd/system", "INSTALL_K3S_NAME": ""} {
		if value, ok := input.Installer[name]; ok && value != "" && value != allowed {
			return result, &nativeSourceError{stage: "snapshot", code: -1}
		}
	}
	encoded, _ := json.Marshal([]any{record.ID, record.Status, record.InputCiphertext, record.ClusterID, record.UpdatedAt, cluster.ID, cluster.APIServer, cluster.KubeconfigCiphertext, cluster.UpdatedAt, current, capability.HostID, capability.Kind, capability.RuntimeID})
	sum := sha256.Sum256(append(encoded, []byte(current.SSHCredentialCiphertext)...))
	return nativeSourceBinding{InstallationID: record.ID, ClusterID: cluster.ID, HostID: current.ID, Version: version, DataDir: dataDir, NodeName: nodeName, Digest: hex.EncodeToString(sum[:])}, nil
}

func updateNativeSourceYAML(value []byte, b nativeSourceBinding) ([]byte, bool, error) {
	if len(value) == 0 || len(value) > 64*1024 {
		return nil, false, &nativeSourceError{stage: "config", code: -1}
	}
	decoder := yaml.NewDecoder(bytes.NewReader(value))
	var doc yaml.Node
	if decoder.Decode(&doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, &nativeSourceError{stage: "config", code: -1}
	}
	var extra yaml.Node
	if decoder.Decode(&extra) != io.EOF {
		return nil, false, &nativeSourceError{stage: "config", code: -1}
	}
	root := doc.Content[0]
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || fields[key.Value] != nil {
			return nil, false, &nativeSourceError{stage: "config", code: -1}
		}
		fields[key.Value] = root.Content[i+1]
	}
	for key, want := range map[string]string{"data-dir": b.DataDir, "node-name": b.NodeName} {
		node := fields[key]
		if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" || node.Value != want {
			return nil, false, &nativeSourceError{stage: "ownership", code: -1}
		}
	}
	labels := fields["node-label"]
	if labels == nil || labels.Kind != yaml.SequenceNode {
		return nil, false, &nativeSourceError{stage: "ownership", code: -1}
	}
	installation, host := false, false
	for _, node := range labels.Content {
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
			return nil, false, &nativeSourceError{stage: "ownership", code: -1}
		}
		if strings.HasPrefix(node.Value, "edo.dev/installation=") {
			if installation || node.Value != "edo.dev/installation="+b.InstallationID {
				return nil, false, &nativeSourceError{stage: "ownership", code: -1}
			}
			installation = true
		}
		if strings.HasPrefix(node.Value, "edo.dev/host=") {
			if host || node.Value != "edo.dev/host="+b.HostID {
				return nil, false, &nativeSourceError{stage: "ownership", code: -1}
			}
			host = true
		}
	}
	if !installation || !host {
		return nil, false, &nativeSourceError{stage: "ownership", code: -1}
	}
	if old := fields["system-default-registry"]; old != nil {
		if old.Kind != yaml.ScalarNode || old.Tag != "!!str" || (old.Value != "" && old.Value != "docker.io" && old.Value != nativeChinaRegistry) {
			return nil, false, &nativeSourceError{stage: "existing_registry", code: -1}
		}
		if old.Value == nativeChinaRegistry {
			return append([]byte(nil), value...), false, nil
		}
		old.Value = nativeChinaRegistry
	} else {
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "system-default-registry"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: nativeChinaRegistry})
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if encoder.Encode(&doc) != nil || encoder.Close() != nil || output.Len() > 64*1024 {
		return nil, false, &nativeSourceError{stage: "config", code: -1}
	}
	return output.Bytes(), true, nil
}

func nativeSession(client *ssh.Client, command, prefix, script string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", &nativeSourceError{stage: "channel", code: -1}
	}
	defer session.Close()
	var stdout, stderr limitedBuffer
	session.Stdin = strings.NewReader(prefix + script)
	session.Stdout = &stdout
	session.Stderr = &stderr
	err = session.Run(command)
	if err != nil {
		stage := "remote"
		value, exceeded := stderr.snapshot()
		if !exceeded {
			for _, line := range strings.Split(value, "\n") {
				if candidate, ok := strings.CutPrefix(line, "EDO_NATIVE_SOURCE_FAILED="); ok {
					switch candidate {
					case "prerequisites", "service", "service_unit", "service_process", "service_path", "service_permissions", "version", "config_identity", "config_changed", "registry_connectivity", "guard", "write", "restart", "postcheck":
						stage = candidate
					}
				}
			}
		}
		return "", &nativeSourceError{stage: stage, code: preflightExitCode(err)}
	}
	value, exceeded := stdout.snapshot()
	if exceeded {
		return "", &nativeSourceError{stage: "output_limit", code: 0}
	}
	return value, nil
}

func nativeSourcePrefix(b nativeSourceBinding, restarting string) string {
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
	return "edo_version=" + quote(b.Version) + "\nedo_data_dir=" + quote(b.DataDir) + "\nedo_restarting=" + quote(restarting) + "\n"
}

func configureNativeChinaImages(ctx context.Context, o options, cfg config.Database, key string, host model.Host, client *ssh.Client, command, prefix string, output io.Writer, stage *string) error {
	*stage = "只读核对原生安装记录及集群绑定"
	binding, err := loadNativeSourceBinding(ctx, o, cfg, key, host)
	if err != nil {
		return err
	}
	*stage = "私下读取自有原生集群配置与运行基线"
	value, err := nativeSession(client, command, prefix, nativeSourcePrefix(binding, o.backup.ExpectedRestartingContainerID)+nativeSourceCommon+nativeSourceRead)
	if err != nil {
		return err
	}
	fields := strings.Split(strings.TrimSuffix(value, "\n"), "\t")
	if len(fields) != 6 || fields[0] != "EDO_NATIVE_SOURCE_READ" || !regexp.MustCompile(`^[0-9]+:[0-9]+$`).MatchString(fields[1]) || !backupContainerID.MatchString(fields[2]) || !backupContainerID.MatchString(fields[3]) || !regexp.MustCompile(`^[0-9]{1,14}$`).MatchString(fields[4]) {
		return &nativeSourceError{stage: "read_receipt", code: 0}
	}
	plain, err := base64.StdEncoding.DecodeString(fields[5])
	if err != nil {
		return &nativeSourceError{stage: "config", code: 0}
	}
	originalSum := sha256.Sum256(plain)
	if hex.EncodeToString(originalSum[:]) != fields[2] {
		return &nativeSourceError{stage: "read_receipt", code: 0}
	}
	updated, changed, err := updateNativeSourceYAML(plain, binding)
	if err != nil {
		return err
	}
	if !changed {
		_, err = fmt.Fprintln(output, "原生集群已使用指定国内系统镜像仓库；未写配置或重启服务。系统组件实际就绪仍需独立验收，原安装任务状态未修改。")
		return err
	}
	*stage = "写入前复核原生安装记录及目标"
	current, err := loadNativeSourceBinding(ctx, o, cfg, key, host)
	if err != nil {
		return err
	}
	if current != binding {
		return &nativeSourceError{stage: "binding_changed", code: -1}
	}
	sum := sha256.Sum256(updated)
	expectedHash := hex.EncodeToString(sum[:])
	script := nativeSourcePrefix(binding, o.backup.ExpectedRestartingContainerID) + "edo_expected_identity='" + fields[1] + "'\nedo_expected_hash='" + fields[2] + "'\nedo_expected_guard='" + fields[3] + "'\nedo_min_restart=" + fields[4] + "\nedo_updated_hash='" + expectedHash + "'\nedo_updated_config='" + base64.StdEncoding.EncodeToString(updated) + "'\n" + nativeSourceCommon + nativeSourceWrite
	*stage = "原子更新自有原生配置并仅重启 K3s 服务"
	receipt, err := nativeSession(client, command, prefix, script)
	if err != nil {
		return err
	}
	if receipt != "EDO_NATIVE_SOURCE_UPDATED\t"+expectedHash+"\n" {
		return &nativeSourceError{stage: "write_receipt", code: 0}
	}
	_, err = fmt.Fprintln(output, "已更新原生集群系统镜像仓库，K3s 服务、固定版本、配置摘要及其他服务/容器基线已核验。仅确认配置与服务更新；镜像层拉取、API/UI 与系统 Pod Ready 仍需独立验收。原安装任务状态未修改。")
	return err
}

const nativeSourceCommon = `set -eu
umask 077
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
edo_fail(){ printf 'EDO_NATIVE_SOURCE_FAILED=%s\n' "$1" >&2;exit 82; }
[ "$(id -u)" = 0 ] || edo_fail prerequisites
for tool in stat readlink sha256sum awk sort base64 tr curl timeout systemctl ps cat mktemp chmod mv rm dirname;do command -v "$tool" >/dev/null 2>&1 || edo_fail prerequisites;done
` + nativeSourceServiceIdentity + `
edo_check(){
  systemctl is-active --quiet k3s.service || edo_fail service
  edo_service_identity
  for directory in /etc /etc/rancher /etc/rancher/k3s;do [ -d "$directory" ] && [ ! -L "$directory" ] && [ "$(stat -c '%u' "$directory")" = 0 ] || edo_fail config_identity;done
  [ "$(stat -c '%a' /etc/rancher/k3s)" = 700 ] || edo_fail config_identity
  [ -f /etc/rancher/k3s/config.yaml ] && [ ! -L /etc/rancher/k3s/config.yaml ] && [ "$(stat -c '%u:%g:%a:%h' /etc/rancher/k3s/config.yaml)" = 0:0:600:1 ] || edo_fail config_identity
  [ "$(readlink -f /etc/rancher/k3s/config.yaml)" = /etc/rancher/k3s/config.yaml ] || edo_fail config_identity
  [ "$(stat -c '%s' /etc/rancher/k3s/config.yaml)" -le 65536 ] || edo_fail config_identity
  [ ! -e /etc/rancher/k3s/config.yaml.d ] && [ ! -L /etc/rancher/k3s/config.yaml.d ] || edo_fail config_identity
  [ ! -e /run/edo-k3s-install.lock ] || edo_fail config_identity
}
edo_hash(){ sha256sum "$1" | awk '{print $1}'; }
edo_docker(){ env -i PATH="$PATH" HOME=/var/empty DOCKER_HOST=unix:///var/run/docker.sock docker --host=unix:///var/run/docker.sock "$@" 2>/dev/null; }
edo_guard(){
  cat /proc/sys/kernel/random/boot_id || return 1
  edo_processes=$(ps -eo pid=,comm= | awk '$2 ~ /^(dockerd|nginx|gitea|act_runner|Runner.Listener|Runner.Worker|Runner.Plugins)$/ {print $1}') || return 1
  for pid in $edo_processes;do
    edo_stat=$(cat "/proc/$pid/stat") || return 1
    edo_rest=${edo_stat##*) };set -- $edo_rest
    [ "$#" -ge 20 ] || return 1
    shift 19
    printf 'P|%s|%s|%s\n' "$pid" "$(cat "/proc/$pid/comm")" "$1"
  done
  edo_ids=$(edo_docker ps -a --no-trunc --quiet) || return 1
  edo_ids=$(printf '%s\n' "$edo_ids" | LC_ALL=C sort) || return 1
  edo_seen=0
  while IFS= read -r id;do
    [ -n "$id" ] || continue
    [ "${#id}" = 64 ] || return 1
    case "$id" in *[!0-9a-f]*)return 1;;esac
    value=$(edo_docker inspect --type container --format '{{.Id}}|{{json .Name}}|{{.State.Pid}}|{{.State.StartedAt}}|{{.RestartCount}}|{{.State.Status}}|{{json .HostConfig.NetworkMode}}|{{range $name,$network := .NetworkSettings.Networks}}{{printf "%q:%s," $name $network.NetworkID}}{{end}}{{"\n"}}{{range .Mounts}}{{json .}}{{"\n"}}{{end}}' "$id") || return 1
    header=$(printf '%s\n' "$value" | awk 'NR==1{print}') || return 1
    mounts=$(printf '%s\n' "$value" | awk 'NR>1{print}') || return 1
    mounts=$(printf '%s\n' "$mounts" | LC_ALL=C sort) || return 1
    mounts=$(printf '%s\n' "$mounts" | awk 'BEGIN{printf "["}NF{if(n++)printf ",";printf "%s",$0}END{printf "]"}') || return 1
    if [ "$id" = "$edo_restarting" ];then edo_seen=$((edo_seen+1));fi
    printf 'C|%s|%s\n' "$header" "$mounts" | awk -F '|' -v OFS='|' -v expected="$edo_restarting" -v minimum="$edo_min_restart" -v phase="$edo_phase" '
      NF<10{exit 1}
      $2==expected && expected!=""{if($6!~/^[0-9]+$/ || $6+0<1 || $6+0<minimum+0 || (phase=="before" && $7!="restarting") || (phase!="before" && $7!="restarting" && $7!="running"))exit 1;$4="expected-restarting";$5="expected-restarting";$6="expected-restarting";$7="expected-restarting"}
      $2!=expected && $7=="restarting" && phase=="before"{exit 1}
      {print}' || return 1
  done <<EDO_CONTAINERS
$edo_ids
EDO_CONTAINERS
  if [ -n "$edo_restarting" ];then [ "$edo_seen" = 1 ] || return 1;fi
}
edo_guard_hash(){ guard=$(edo_guard) || return 1;guard=$(printf '%s\n' "$guard" | LC_ALL=C sort) || return 1;printf '%s\n' "$guard" | sha256sum | awk '{print $1}'; }
edo_check
`

// 官方启动器会切换到 data-dir 下的嵌入二进制并隐藏参数，仍需同时固定 unit、路径、权限和版本。
const nativeSourceServiceIdentity = `edo_root_path(){
  edo_path=$1
  [ "$(readlink -f "$edo_path")" = "$edo_path" ] || edo_fail service_path
  while :;do
    [ ! -L "$edo_path" ] && [ "$(stat -c '%u' "$edo_path")" = 0 ] || edo_fail service_permissions
    edo_mode=$(stat -c '%a' "$edo_path") || edo_fail service_permissions
    case "$edo_mode" in ''|*[!0-7]*)edo_fail service_permissions;;esac
    [ "${#edo_mode}" -le 4 ] && [ "$((0$edo_mode & 022))" = 0 ] || edo_fail service_permissions
    [ "$edo_path" != / ] || break
    edo_path=$(dirname "$edo_path") || edo_fail service_path
  done
}
edo_service_identity(){
  edo_unit=$(systemctl show k3s.service --property=ExecStart --value) || edo_fail service_unit
  printf '%s\n' "$edo_unit" | awk -F ' ; ' '
    NR!=1 || NF<4 || $1!="{ path=/usr/local/bin/k3s" || $2!="argv[]=/usr/local/bin/k3s server --config /etc/rancher/k3s/config.yaml" || $3!="ignore_errors=no"{exit 1}
    {for(i=4;i<=NF;i++)if($i ~ /argv\[\]=|path=|\{/)exit 1}
    END{if(NR!=1)exit 1}' || edo_fail service_unit
  edo_pid=$(systemctl show k3s.service --property=ExecMainPID --value) || edo_fail service_process
  case "$edo_pid" in ''|0|*[!0-9]*)edo_fail service_process;;esac
  edo_exe=$(readlink -f "/proc/$edo_pid/exe") || edo_fail service_path
  case "$edo_exe" in "$edo_data_dir"/data/*/bin/k3s):;;*)edo_fail service_path;;esac
  edo_asset=${edo_exe#"$edo_data_dir"/data/}
  edo_asset=${edo_asset%/bin/k3s}
  [ "${#edo_asset}" = 64 ] || edo_fail service_path
  case "$edo_asset" in *[!0-9a-f]*)edo_fail service_path;;esac
  [ -f "$edo_exe" ] && [ -x "$edo_exe" ] && [ -f /usr/local/bin/k3s ] && [ -x /usr/local/bin/k3s ] || edo_fail service_permissions
  edo_root_path /usr/local/bin/k3s
  edo_root_path "$edo_exe"
  edo_exe_identity=$(stat -c '%d:%i' "$edo_exe") || edo_fail service_process
  [ "$(stat -Lc '%d:%i' "/proc/$edo_pid/exe")" = "$edo_exe_identity" ] || edo_fail service_process
  edo_command=$(tr '\000' '\n' < "/proc/$edo_pid/cmdline") || edo_fail service_process
  case "$edo_command" in '/usr/local/bin/k3s server'|'k3s server'):;;*)
    [ "$edo_command" = "$(printf '/usr/local/bin/k3s\nserver\n--config\n/etc/rancher/k3s/config.yaml')" ] || edo_fail service_process;;esac
  for edo_binary in /usr/local/bin/k3s "$edo_exe";do
    edo_binary_version=$(timeout 3 "$edo_binary" --version 2>/dev/null) || edo_fail version
    [ "$(printf '%s\n' "$edo_binary_version" | awk 'NR==1{print $3}')" = "$edo_version" ] || edo_fail version
  done
  [ "$(readlink -f "/proc/$edo_pid/exe")" = "$edo_exe" ] && [ "$(stat -Lc '%d:%i' "/proc/$edo_pid/exe")" = "$edo_exe_identity" ] && [ "$(systemctl show k3s.service --property=ExecMainPID --value)" = "$edo_pid" ] || edo_fail service_process
}
`

const nativeSourceRead = `edo_phase=before
edo_min_restart=0
if [ -n "$edo_restarting" ];then edo_min_restart=$(edo_docker inspect --type container --format '{{.RestartCount}}' "$edo_restarting") || edo_fail guard;case "$edo_min_restart" in ''|*[!0-9]*)edo_fail guard;;esac;fi
identity=$(stat -c '%d:%i' /etc/rancher/k3s/config.yaml) || edo_fail config_identity
hash=$(edo_hash /etc/rancher/k3s/config.yaml) || edo_fail config_identity
guard=$(edo_guard_hash) || edo_fail guard
encoded=$(base64 < /etc/rancher/k3s/config.yaml | tr -d '\n') || edo_fail config_identity
[ "$(stat -c '%d:%i' /etc/rancher/k3s/config.yaml)" = "$identity" ] && [ "$(edo_hash /etc/rancher/k3s/config.yaml)" = "$hash" ] || edo_fail config_changed
printf 'EDO_NATIVE_SOURCE_READ\t%s\t%s\t%s\t%s\t%s\n' "$identity" "$hash" "$guard" "$edo_min_restart" "$encoded"
`

const nativeSourceWrite = `edo_phase=after
[ "$(stat -c '%d:%i' /etc/rancher/k3s/config.yaml)" = "$edo_expected_identity" ] && [ "$(edo_hash /etc/rancher/k3s/config.yaml)" = "$edo_expected_hash" ] || edo_fail config_changed
code=$(env -i PATH="$PATH" HOME=/var/empty curl --silent --show-error --head --proto '=https' --connect-timeout 5 --max-time 15 --output /dev/null --write-out '%{http_code}' https://registry.cn-hangzhou.aliyuncs.com/v2/ 2>/dev/null) || edo_fail registry_connectivity
case "$code" in 200|401):;;*)edo_fail registry_connectivity;;esac
[ "$(edo_guard_hash)" = "$edo_expected_guard" ] || edo_fail guard
temporary=$(mktemp /etc/rancher/k3s/.edo-source.XXXXXXXX) || edo_fail write
trap 'rm -f -- "$temporary"' EXIT HUP INT TERM
printf '%s' "$edo_updated_config" | base64 -d > "$temporary" || edo_fail write
chmod 600 "$temporary" || edo_fail write
[ "$(edo_hash "$temporary")" = "$edo_updated_hash" ] || edo_fail write
edo_check
[ "$(stat -c '%d:%i' /etc/rancher/k3s/config.yaml)" = "$edo_expected_identity" ] && [ "$(edo_hash /etc/rancher/k3s/config.yaml)" = "$edo_expected_hash" ] || edo_fail config_changed
mv -- "$temporary" /etc/rancher/k3s/config.yaml || edo_fail write
systemctl restart k3s.service >/dev/null 2>&1 || edo_fail restart
edo_check
[ "$(edo_hash /etc/rancher/k3s/config.yaml)" = "$edo_updated_hash" ] || edo_fail postcheck
[ "$(edo_guard_hash)" = "$edo_expected_guard" ] || edo_fail guard
printf 'EDO_NATIVE_SOURCE_UPDATED\t%s\n' "$edo_updated_hash"
`
