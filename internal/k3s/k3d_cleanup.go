package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"edo/internal/database"
	"edo/internal/kube"
	"edo/internal/model"
)

// 只有创建前确认名称未占用，并持久化此证明的任务，才能补偿删除。
// 证明保存在安装快照密文中；旧任务不能根据现有标签补造证明。
type k3dCleanupProof struct {
	Version          int                `json:"version"`
	InstallationID   string             `json:"installation_id"`
	HostID           string             `json:"host_id"`
	HostDigest       string             `json:"host_digest"`
	DaemonID         string             `json:"daemon_id"`
	LocalRevision    string             `json:"local_revision,omitempty"`
	PreparedAt       time.Time          `json:"prepared_at"`
	ExistingVolumes  []string           `json:"existing_volumes"`
	AnonymousVolumes []k3dCleanupVolume `json:"anonymous_volumes,omitempty"`
	ToolPath         string             `json:"tool_path,omitempty"`
	ToolHash         string             `json:"tool_hash,omitempty"`
	ToolPlatform     string             `json:"tool_platform,omitempty"`
}

type k3dCleanupContainer struct {
	ID, Name, App, Cluster, Installation, Role string
	Network, NetworkID, NetworkExternal        string
	ImageVolume                                string
	Volumes                                    []string
}

type k3dCleanupNetwork struct {
	ID, Name, App string
	Containers    map[string]json.RawMessage
}

type k3dCleanupVolume struct {
	Name, App, Cluster, Driver, CreatedAt string
	LabelCount                            int
	AnonymousLabel                        bool
}

type k3dCleanupInventory struct {
	DaemonID         string
	Containers       []k3dCleanupContainer
	Networks         []k3dCleanupNetwork
	Volumes          []k3dCleanupVolume
	AnonymousVolumes []k3dCleanupVolume
	AllVolumeNames   []string
	VolumeUsers      map[string][]string
}

// 不能输出整个 Config.Labels：官方标签还包含集群加入令牌。
const k3dCleanupContainerFormat = `{"ID":{{json .Id}},"Name":{{json .Name}},"App":{{json (index .Config.Labels "app")}},"Cluster":{{json (index .Config.Labels "k3d.cluster")}},"Installation":{{json (index .Config.Labels "edo.dev/installation")}},"Role":{{json (index .Config.Labels "k3d.role")}},"Network":{{json (index .Config.Labels "k3d.cluster.network")}},"NetworkID":{{json (index .Config.Labels "k3d.cluster.network.id")}},"NetworkExternal":{{json (index .Config.Labels "k3d.cluster.network.external")}},"ImageVolume":{{json (index .Config.Labels "k3d.cluster.imageVolume")}},"Volumes":[{{range $i,$mount := .Mounts}}{{if $i}},{{end}}{{json $mount.Name}}{{end}}]}`
const k3dCleanupNetworkFormat = `{"ID":{{json .Id}},"Name":{{json .Name}},"App":{{json (index .Labels "app")}},"Containers":{{json .Containers}}}`
const k3dCleanupVolumeFormat = `{{$anonymous := false}}{{range $key,$value := .Labels}}{{if and (eq $key "com.docker.volume.anonymous") (eq $value "")}}{{$anonymous = true}}{{end}}{{end}}{"Name":{{json .Name}},"App":{{json (index .Labels "app")}},"Cluster":{{json (index .Labels "k3d.cluster")}},"Driver":{{json .Driver}},"CreatedAt":{{json .CreatedAt}},"LabelCount":{{len .Labels}},"AnonymousLabel":{{$anonymous}}}`

type k3dCleanupRead func(context.Context, ...string) (string, error)

func k3dCleanupIDs(raw string, volume bool) ([]string, error) {
	var ids []string
	for _, value := range strings.Fields(raw) {
		if volume {
			if len(value) > 255 || strings.ContainsAny(value, "/\\\"'\x00") || strings.HasPrefix(value, "-") {
				return nil, ErrHostChanged
			}
		} else if !k3dDockerID(value) {
			return nil, ErrHostChanged
		}
		ids = append(ids, value)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

func k3dDockerID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if !(ch >= 'a' && ch <= 'f' || ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

func readK3dCleanupInventory(ctx context.Context, id string, read k3dCleanupRead, tracked ...k3dCleanupVolume) (*k3dCleanupInventory, error) {
	name := k3dResourceName(id)
	if name == "" {
		return nil, ErrInvalidInput
	}
	result := &k3dCleanupInventory{VolumeUsers: map[string][]string{}}
	raw, err := read(ctx, "info", "--format", "{{json .ID}}")
	if err != nil {
		return nil, err
	}
	if json.Unmarshal([]byte(raw), &result.DaemonID) != nil || strings.TrimSpace(result.DaemonID) == "" {
		return nil, ErrHostChanged
	}
	for _, kind := range []string{"container", "network", "volume"} {
		var ids []string
		nameFilter := "name=^k3d-" + name + "$"
		if kind == "container" {
			nameFilter = "name=^/k3d-" + name + "(-|$)"
		} else if kind == "volume" {
			nameFilter = "name=^" + k3dImageVolumeName(id) + "$"
		}
		filters := []string{"label=k3d.cluster=" + name, nameFilter}
		if kind == "network" {
			for _, node := range result.Containers {
				if node.NetworkID != "" {
					if !k3dDockerID(node.NetworkID) {
						return nil, ErrHostChanged
					}
					filters = append(filters, "id="+node.NetworkID)
				}
			}
		}
		for _, filter := range filters {
			args := []string{kind, "ls", "--quiet", "--filter", filter}
			if kind == "container" {
				args = append(args, "--all", "--no-trunc")
			} else if kind == "network" {
				args = append(args, "--no-trunc")
			}
			raw, err := read(ctx, args...)
			if err != nil {
				return nil, err
			}
			found, err := k3dCleanupIDs(raw, kind == "volume")
			if err != nil {
				return nil, err
			}
			ids = append(ids, found...)
		}
		if kind == "volume" {
			raw, err := read(ctx, "volume", "ls", "--quiet")
			if err != nil {
				return nil, err
			}
			result.AllVolumeNames, err = k3dCleanupIDs(raw, true)
			if err != nil {
				return nil, err
			}
			for _, node := range result.Containers {
				for _, volume := range node.Volumes {
					if volume != "" {
						if !slices.Contains(result.AllVolumeNames, volume) {
							return nil, ErrHostChanged
						}
						ids = append(ids, volume)
					}
				}
			}
			for _, volume := range tracked {
				if slices.Contains(result.AllVolumeNames, volume.Name) {
					ids = append(ids, volume.Name)
				}
			}
		}
		slices.Sort(ids)
		ids = slices.Compact(ids)
		if len(ids) == 0 {
			continue
		}
		format := k3dCleanupContainerFormat
		if kind == "network" {
			format = k3dCleanupNetworkFormat
		} else if kind == "volume" {
			format = k3dCleanupVolumeFormat
		}
		raw, err := read(ctx, append([]string{kind, "inspect", "--format", format}, ids...)...)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimSpace(raw), "\n")
		if len(lines) != len(ids) {
			return nil, ErrHostChanged
		}
		for i, line := range lines {
			switch kind {
			case "container":
				var item k3dCleanupContainer
				if json.Unmarshal([]byte(line), &item) != nil || item.ID != ids[i] {
					return nil, ErrHostChanged
				}
				result.Containers = append(result.Containers, item)
			case "network":
				var item k3dCleanupNetwork
				if json.Unmarshal([]byte(line), &item) != nil || item.ID != ids[i] {
					return nil, ErrHostChanged
				}
				result.Networks = append(result.Networks, item)
			case "volume":
				var item k3dCleanupVolume
				if json.Unmarshal([]byte(line), &item) != nil || item.Name != ids[i] {
					return nil, ErrHostChanged
				}
				if item.Name == k3dImageVolumeName(id) || item.Cluster == name {
					result.Volumes = append(result.Volumes, item)
				} else {
					result.AnonymousVolumes = append(result.AnonymousVolumes, item)
				}
			}
		}
	}
	for _, volume := range append(slices.Clone(result.Volumes), result.AnonymousVolumes...) {
		raw, err := read(ctx, "container", "ls", "--quiet", "--all", "--no-trunc", "--filter", "volume="+volume.Name)
		if err != nil {
			return nil, err
		}
		users, err := k3dCleanupIDs(raw, false)
		if err != nil {
			return nil, err
		}
		result.VolumeUsers[volume.Name] = users
	}
	return result, nil
}

func (i *k3dCleanupInventory) empty() bool {
	return len(i.Containers)+len(i.Networks)+len(i.Volumes)+len(i.AnonymousVolumes) == 0
}

func validateK3dCleanupInventory(input Input, id string, inventory *k3dCleanupInventory) error {
	name := k3dResourceName(id)
	if name == "" || inventory == nil {
		return ErrInvalidInput
	}
	networkName, volumeName := "k3d-"+name, k3dImageVolumeName(id)
	allowed := map[string]bool{}
	for _, nodeName := range localK3dNodeNames(input, id) {
		allowed[nodeName] = true
	}
	containers := map[string]bool{}
	for _, item := range inventory.Containers {
		nodeName := strings.TrimPrefix(item.Name, "/")
		tools := nodeName == networkName+"-tools"
		if !k3dDockerID(item.ID) || item.App != "k3d" || item.Cluster != name || !allowed[nodeName] && !tools {
			return ErrHostChanged
		}
		if tools {
			// 官方 tools 容器不继承 --runtime-label，只以本次未占用名称和官方标签核对。
			if item.Installation != "" && item.Installation != id || item.Network != "" || item.NetworkID != "" || item.ImageVolume != "" || item.Role != "noRole" {
				return ErrHostChanged
			}
		} else if item.Installation != id || item.Network != networkName || item.NetworkExternal != "false" || item.ImageVolume != volumeName || !k3dDockerID(item.NetworkID) || item.Role != "server" && item.Role != "agent" {
			return ErrHostChanged
		}
		containers[item.ID] = true
		for _, volume := range item.Volumes {
			if volume != "" && volume != volumeName && !k3dDockerID(volume) {
				return ErrHostChanged
			}
		}
	}
	for _, item := range inventory.Networks {
		if item.Name != networkName || item.App != "k3d" || !k3dDockerID(item.ID) {
			return ErrHostChanged
		}
		for id := range item.Containers {
			if !containers[id] {
				return ErrHostChanged
			}
		}
		for _, node := range inventory.Containers {
			if node.NetworkID != "" && node.NetworkID != item.ID {
				return ErrHostChanged
			}
		}
	}
	for _, item := range inventory.Volumes {
		if item.Name != volumeName || item.App != "k3d" || item.Cluster != name {
			return ErrHostChanged
		}
		for _, id := range inventory.VolumeUsers[item.Name] {
			if !containers[id] {
				return ErrHostChanged
			}
		}
	}
	return nil
}

func validateK3dCleanupAnonymous(proof *k3dCleanupProof, inventory *k3dCleanupInventory) error {
	containers := map[string]bool{}
	for _, node := range inventory.Containers {
		containers[node.ID] = true
	}
	for _, volume := range inventory.AnonymousVolumes {
		// Moby 自动生成匿名卷时可附加标准空值标记，不能误当成第三方卷。
		labelsAllowed := volume.LabelCount == 0 || volume.LabelCount == 1 && volume.AnonymousLabel
		if !k3dDockerID(volume.Name) || !labelsAllowed || volume.Driver != "local" || volume.CreatedAt == "" || slices.Contains(proof.ExistingVolumes, volume.Name) {
			return ErrHostChanged
		}
		known := false
		for _, saved := range proof.AnonymousVolumes {
			if saved.Name == volume.Name {
				if saved != volume {
					return ErrHostChanged
				}
				known = true
			}
		}
		users := inventory.VolumeUsers[volume.Name]
		if len(users) == 0 && !known {
			return ErrHostChanged
		}
		for _, user := range users {
			if !containers[user] {
				return ErrHostChanged
			}
		}
	}
	return nil
}

func (s *Service) checkK3dCleanupLease(ctx context.Context, record *model.K3sInstallation, hostID string) error {
	if record == nil || record.ID == "" || s.owner == "" {
		return ErrHostChanged
	}
	var count int64
	err := s.db.WithContext(ctx).Model(&model.K3sInstallation{}).
		Where("uuid = ? AND project_id = ? AND department_id = ? AND action = ? AND status IN ? AND cluster_id = ? AND lease_owner = ? AND lease_expires_at > ?", record.ID, record.ProjectID, record.DepartmentID, "create", []string{"running", "failed"}, "", s.owner, time.Now().UTC()).Count(&count).Error
	if err != nil || count != 1 {
		slog.ErrorContext(ctx, "复核 k3d 失败清理租约失败", "operation", "k3d_cleanup_lease", "installation_id", record.ID, "err", err)
		return ErrHostChanged
	}
	err = s.db.WithContext(ctx).Model(&model.K3sInstallationHostLock{}).
		Where("installation_id = ? AND host_id = ? AND project_id = ? AND department_id = ? AND expires_at > ?", record.ID, hostID, record.ProjectID, record.DepartmentID, time.Now().UTC()).Count(&count).Error
	if err != nil || count != 1 {
		slog.ErrorContext(ctx, "复核 k3d 失败清理主机锁失败", "operation", "k3d_cleanup_host_lock", "installation_id", record.ID, "host_id", hostID, "err", err)
		return ErrHostChanged
	}
	return nil
}

func (s *Service) checkK3dCleanupConnections(ctx context.Context, snapshot savedInput) error {
	if snapshot.Input.Endpoint == "" {
		return ErrHostChanged
	}
	var count int64
	endpoint := strings.ToLower(strings.TrimRight(snapshot.Input.Endpoint, "/"))
	// 只取得存在性，不能因为连接属于其他项目就删除已被接入的实际集群。
	// 此内部补偿已经核对任务租约；只在这两项只读检查中使用系统范围，不继承请求者项目过滤。
	systemCtx := database.WithDepartmentScope(ctx, database.DepartmentScope{AllDepartments: true})
	err := s.db.WithContext(systemCtx).Model(&model.KubernetesCluster{}).Where("LOWER(api_server) IN ?", []string{endpoint, endpoint + "/"}).Count(&count).Error
	if err != nil || count != 0 {
		slog.ErrorContext(ctx, "复核失败 k3d 的已有接入关系失败", "operation", "k3d_cleanup_connections", "host_id", snapshot.Input.ServerHostID, "err", err)
		return &ValidationError{Message: "目标集群已有接入记录或归属无法确认，未自动清理"}
	}
	return kube.RequireHostClusterAvailable(systemCtx, s.db, []string{snapshot.Input.ServerHostID}, "")
}

type k3dCleanupCommand struct {
	args []string
	raw  string
}

// SSH 创建和清理共用同一个文件锁，进程中断后也不能与尚在远端执行的创建交叉。
func k3dCleanupLockScript(id string) string {
	name := k3dResourceName(id)
	if name == "" {
		return "exit 44\n"
	}
	return "command -v flock >/dev/null 2>&1\nexec 9>" + shellQuote("/run/edo-k3d-"+name+".lock") + "\nflock -w 20 9\n"
}

func k3dCleanupDockerCommand(args []string) string {
	parts := []string{"edo_docker"}
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func (s *Service) withK3dCleanupRuntime(ctx context.Context, record *model.K3sInstallation, snapshot savedInput, operation func(context.Context, k3dCleanupRead, func(context.Context, []k3dCleanupCommand, io.Writer) error) error) error {
	id := snapshot.Input.ServerHostID
	if snapshot.Input.Mode != "k3d" || snapshot.Input.K3d == nil || len(snapshot.HostDigests) != 1 || snapshot.HostDigests[id] == "" {
		return ErrHostChanged
	}
	host, bundle, err := s.loadInstallationHost(ctx, "k3d", id)
	if err != nil {
		return err
	}
	if hostDigest(host) != snapshot.HostDigests[id] {
		return ErrHostChanged
	}
	checkTarget := func(ctx context.Context) error {
		current, _, err := s.loadInstallationHost(ctx, "k3d", id)
		if err != nil {
			return err
		}
		if hostDigest(current) != snapshot.HostDigests[id] {
			return ErrHostChanged
		}
		if snapshot.LocalK3d != nil {
			return s.localK3d.checkRevision(ctx, snapshot.LocalK3d)
		}
		return nil
	}
	if snapshot.LocalK3d != nil {
		if id != model.BuiltinLocalHostID || s.localK3d == nil {
			return ErrHostChanged
		}
		ctx = context.WithValue(ctx, localK3dRevisionKey{}, snapshot.LocalK3d)
		return s.localK3d.withCLI(ctx, func(ctx context.Context) error {
			read := func(ctx context.Context, args ...string) (string, error) {
				ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				raw, _, err := s.localK3d.read(ctx, "docker", args...)
				return raw, err
			}
			remove := func(ctx context.Context, checks []k3dCleanupCommand, out io.Writer) error {
				proof := snapshot.K3dCleanup
				if proof == nil || !filepath.IsAbs(proof.ToolPath) || !k3dDockerID(proof.ToolHash) || proof.ToolPlatform != runtime.GOOS+"/"+runtime.GOARCH {
					return ErrHostChanged
				}
				// 复用创建时已验证的官方资产；恢复清理无需再访问发布站或下载工具。
				tool := &localK3dTool{path: proof.ToolPath, hash: proof.ToolHash}
				valid, err := tool.valid()
				if err != nil || !valid {
					return ErrRemote
				}
				for _, check := range checks {
					raw, err := read(ctx, check.args...)
					if err != nil {
						return err
					}
					if strings.TrimSpace(raw) != strings.TrimSpace(check.raw) {
						return ErrHostChanged
					}
				}
				if err := s.checkK3dCleanupLease(ctx, record, id); err != nil {
					return err
				}
				if err := s.checkK3dCleanupConnections(ctx, snapshot); err != nil {
					return err
				}
				if err := checkTarget(ctx); err != nil {
					return err
				}
				directory, err := os.MkdirTemp("", "edo-k3d-cleanup-")
				if err != nil {
					return err
				}
				defer os.RemoveAll(directory)
				session := *ctx.Value(localK3dSessionKey{}).(*localK3dSession)
				session.environment = slices.DeleteFunc(slices.Clone(session.environment), func(value string) bool {
					return strings.HasPrefix(value, "KUBECONFIG=") || strings.HasPrefix(value, "HOME=") || strings.HasPrefix(value, "XDG_CONFIG_HOME=")
				})
				session.environment = append(session.environment, "HOME="+directory, "XDG_CONFIG_HOME="+directory, "KUBECONFIG="+filepath.Join(directory, "config"))
				isolated := context.WithValue(ctx, localK3dSessionKey{}, &session)
				_, _ = fmt.Fprintf(out, "$ k3d cluster delete %s\n", k3dResourceName(record.ID))
				return s.localK3d.execute(isolated, tool.path, []string{"cluster", "delete", k3dResourceName(record.ID)}, out)
			}
			return operation(ctx, read, remove)
		})
	}
	if id == model.BuiltinLocalHostID {
		return ErrHostChanged
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
	lockProbe, probeErr := s.transport.Read(probeCtx, host, bundle, "set -eu\nif command -v flock >/dev/null 2>&1; then printf 'EDO_K3D_FLOCK_READY\\n'; else printf 'EDO_K3D_FLOCK_MISSING\\n'; fi\n")
	probeCancel()
	if probeErr != nil || (strings.TrimSpace(lockProbe) != "EDO_K3D_FLOCK_READY" && strings.TrimSpace(lockProbe) != "EDO_K3D_FLOCK_MISSING") {
		slog.ErrorContext(ctx, "读取 k3d 主机互斥锁能力失败", "operation", "k3d_cleanup_lock_probe", "host_id", id, "err", probeErr)
		return &ValidationError{Message: "无法确认目标主机的互斥锁能力，请检查主机连接后重试"}
	}
	if strings.TrimSpace(lockProbe) == "EDO_K3D_FLOCK_MISSING" {
		slog.ErrorContext(ctx, "k3d 主机缺少互斥锁能力", "operation", "k3d_cleanup_lock_probe", "host_id", id, "err", ErrRemote)
		return &ValidationError{Message: "目标主机缺少 flock 命令，无法安全协调 k3d 创建与失败清理"}
	}
	read := func(ctx context.Context, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return s.transport.Read(ctx, host, bundle, "set -eu\n"+k3dLocalEnvironment+k3dCleanupDockerCommand(args)+"\n")
	}
	remove := func(ctx context.Context, checks []k3dCleanupCommand, out io.Writer) error {
		if err := s.checkK3dCleanupLease(ctx, record, id); err != nil {
			return err
		}
		if err := s.checkK3dCleanupConnections(ctx, snapshot); err != nil {
			return err
		}
		if err := checkTarget(ctx); err != nil {
			return err
		}
		var script strings.Builder
		script.WriteString("set -eu\n" + k3dCleanupLockScript(record.ID) + k3dLocalEnvironment + k3dToolSelection + "edo_tool_valid\n")
		for _, check := range checks {
			script.WriteString("edo_check=$(" + k3dCleanupDockerCommand(check.args) + ")\n[ \"$edo_check\" = " + shellQuote(strings.TrimRight(check.raw, "\n")) + " ] || exit 44\n")
		}
		script.WriteString("edo_cleanup_home=$(mktemp -d /var/lib/edo/k3d/.cleanup.XXXXXX)\ntrap 'rm -rf -- \"$edo_cleanup_home\"' EXIT\n")
		script.WriteString("printf '%s\\n' " + shellQuote("$ k3d cluster delete "+k3dResourceName(record.ID)) + "\n")
		script.WriteString("env -i PATH=\"$edo_path\" HOME=\"$edo_cleanup_home\" XDG_CONFIG_HOME=\"$edo_cleanup_home\" KUBECONFIG=\"$edo_cleanup_home/config\" DOCKER_HOST=unix:///var/run/docker.sock \"$edo_k3d\" cluster delete " + shellQuote(k3dResourceName(record.ID)) + "\n")
		return s.transport.Run(ctx, host, bundle, script.String(), out)
	}
	return operation(ctx, read, remove)
}

func (s *Service) prepareK3dCleanup(ctx context.Context, record *model.K3sInstallation, snapshot savedInput) (*k3dCleanupProof, error) {
	if err := s.checkK3dCleanupLease(ctx, record, snapshot.Input.ServerHostID); err != nil {
		return nil, err
	}
	var proof *k3dCleanupProof
	err := s.withK3dCleanupRuntime(ctx, record, snapshot, func(ctx context.Context, read k3dCleanupRead, _ func(context.Context, []k3dCleanupCommand, io.Writer) error) error {
		inventory, err := readK3dCleanupInventory(ctx, record.ID, read)
		if err != nil {
			return err
		}
		if !inventory.empty() {
			return &ValidationError{Message: "本次 k3d 资源名称已被占用，请重新生成安装计划"}
		}
		proof = &k3dCleanupProof{Version: 1, InstallationID: record.ID, HostID: snapshot.Input.ServerHostID, HostDigest: snapshot.HostDigests[snapshot.Input.ServerHostID], DaemonID: inventory.DaemonID, PreparedAt: time.Now().UTC(), ExistingVolumes: inventory.AllVolumeNames}
		if snapshot.LocalK3d != nil {
			if inventory.DaemonID != snapshot.LocalK3d.DaemonID {
				return ErrHostChanged
			}
			proof.LocalRevision = snapshot.LocalK3d.Revision
			if s.localK3d.tool == nil {
				return ErrRemote
			}
			valid, err := s.localK3d.tool.valid()
			if err != nil || !valid {
				return ErrRemote
			}
			proof.ToolPath, proof.ToolHash, proof.ToolPlatform = s.localK3d.tool.path, s.localK3d.tool.hash, runtime.GOOS+"/"+runtime.GOARCH
		}
		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, "准备 k3d 失败清理证明失败", "operation", "k3d_cleanup_prepare", "installation_id", record.ID, "err", err)
		return nil, err
	}
	return proof, nil
}

func (s *Service) cleanupFailedK3d(ctx context.Context, record *model.K3sInstallation, snapshot savedInput, proof *k3dCleanupProof, out io.Writer) error {
	if record == nil || proof == nil || proof.Version != 1 || proof.InstallationID != record.ID || proof.HostID != snapshot.Input.ServerHostID || proof.HostDigest == "" || proof.HostDigest != snapshot.HostDigests[proof.HostID] || proof.DaemonID == "" || proof.PreparedAt.IsZero() || record.ClusterID != "" {
		return &ValidationError{Message: "无法确认失败 k3d 的创建归属，未自动清理"}
	}
	if snapshot.LocalK3d != nil && (proof.LocalRevision != snapshot.LocalK3d.Revision || proof.DaemonID != snapshot.LocalK3d.DaemonID) || snapshot.LocalK3d == nil && proof.LocalRevision != "" {
		return ErrHostChanged
	}
	if err := s.checkK3dCleanupLease(ctx, record, proof.HostID); err != nil {
		return err
	}
	if err := s.checkK3dCleanupConnections(ctx, snapshot); err != nil {
		return err
	}
	if out == nil {
		out = io.Discard
	}
	err := s.withK3dCleanupRuntime(ctx, record, snapshot, func(ctx context.Context, read k3dCleanupRead, remove func(context.Context, []k3dCleanupCommand, io.Writer) error) error {
		var checks []k3dCleanupCommand
		inventory, err := readK3dCleanupInventory(ctx, record.ID, func(ctx context.Context, args ...string) (string, error) {
			raw, err := read(ctx, args...)
			if err == nil && !slices.Equal(args, []string{"volume", "ls", "--quiet"}) {
				checks = append(checks, k3dCleanupCommand{args: slices.Clone(args), raw: raw})
			}
			return raw, err
		}, proof.AnonymousVolumes...)
		if err != nil {
			return err
		}
		if inventory.DaemonID != proof.DaemonID {
			return ErrHostChanged
		}
		if err := validateK3dCleanupInventory(snapshot.Input, record.ID, inventory); err != nil {
			return err
		}
		if err := validateK3dCleanupAnonymous(proof, inventory); err != nil {
			return err
		}
		if inventory.empty() {
			return nil
		}
		// 先保存匿名卷身份，再调用官方删除；即使进程中断，也能核对孤立卷残留。
		for _, volume := range inventory.AnonymousVolumes {
			if !slices.Contains(proof.AnonymousVolumes, volume) {
				proof.AnonymousVolumes = append(proof.AnonymousVolumes, volume)
			}
		}
		snapshot.K3dCleanup = proof
		ciphertext, err := s.encodeK3dSnapshot(record.ID, snapshot)
		if err != nil {
			return err
		}
		updated := s.db.WithContext(ctx).Model(&model.K3sInstallation{}).Where("uuid = ? AND status IN ? AND lease_owner = ? AND lease_expires_at > ?", record.ID, []string{"running", "failed"}, s.owner, time.Now().UTC()).Update("input_ciphertext", ciphertext)
		if updated.Error != nil || updated.RowsAffected != 1 {
			return errors.Join(ErrHostChanged, updated.Error)
		}
		record.InputCiphertext = ciphertext
		deleteErr := remove(ctx, checks, out)
		remaining, verifyErr := readK3dCleanupInventory(ctx, record.ID, read, proof.AnonymousVolumes...)
		if verifyErr != nil {
			return errors.Join(deleteErr, verifyErr)
		}
		return verifyK3dCleanupResult(proof.DaemonID, remaining, deleteErr)
	})
	if err != nil {
		slog.ErrorContext(ctx, "清理失败的 k3d 资源失败", "operation", "k3d_cleanup_failed", "installation_id", record.ID, "err", err)
		if ValidationMessage(err) != "" {
			return err
		}
		return &ValidationError{Message: "失败 k3d 的自动清理未完成，请查看安装日志"}
	}
	return nil
}

func verifyK3dCleanupResult(daemonID string, remaining *k3dCleanupInventory, deleteErr error) error {
	if remaining == nil || remaining.DaemonID != daemonID {
		return ErrHostChanged
	}
	if remaining.empty() {
		return deleteErr
	}
	var kinds []string
	if len(remaining.Containers) > 0 {
		kinds = append(kinds, "容器")
	}
	if len(remaining.Networks) > 0 {
		kinds = append(kinds, "网络")
	}
	if len(remaining.Volumes) > 0 {
		kinds = append(kinds, "镜像卷")
	}
	if len(remaining.AnonymousVolumes) > 0 {
		kinds = append(kinds, "节点数据卷")
	}
	return &ValidationError{Message: "官方 k3d 清理未完成，仍有" + strings.Join(kinds, "、") + "残留"}
}
