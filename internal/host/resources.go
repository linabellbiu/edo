package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"gorm.io/gorm"

	"edo/internal/cache"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/sshclient"
)

var errHostResourcesUnavailable = errors.New("暂时无法读取主机系统资源")

const resourceTTL = time.Minute
const resourceFailureTTL = 15 * time.Second
const resourceTimeout = 10 * time.Second
const resourceCacheLimit = 1024
const resourceConcurrency = 4

// ResourceInfo 是当前系统命名空间可见的容量，不表示物理机整盘或容器配额。
type ResourceInfo struct {
	State            string     `json:"state"`
	Source           string     `json:"source"`
	Scope            string     `json:"scope"`
	CPUCores         *int       `json:"cpu_cores"`
	MemoryTotalBytes *uint64    `json:"memory_total_bytes"`
	DiskTotalBytes   *uint64    `json:"disk_total_bytes"`
	OS               string     `json:"os"`
	DiskMount        string     `json:"disk_mount"`
	CollectedAt      *time.Time `json:"collected_at"`
}

func resourceIdentity(host model.Host) string {
	value, _ := json.Marshal([]any{host.ID, host.ProjectID, host.DepartmentID, host.Mode, host.IsActive, host.IsBuiltin, host.CreatedAt, host.UpdatedAt,
		host.Address, host.SSHPort, host.SSHUsername, host.SSHHostKeyFingerprint, host.SSHCredentialCiphertext})
	return fmt.Sprintf("%x", sha256.Sum256(value))
}

func cloneResources(value ResourceInfo) ResourceInfo {
	if value.CPUCores != nil {
		v := *value.CPUCores
		value.CPUCores = &v
	}
	if value.MemoryTotalBytes != nil {
		v := *value.MemoryTotalBytes
		value.MemoryTotalBytes = &v
	}
	if value.DiskTotalBytes != nil {
		v := *value.DiskTotalBytes
		value.DiskTotalBytes = &v
	}
	return value
}

// Resources 查询当前请求可见的主机，仅返回后台已保存的结果。
func (s *Service) Resources(ctx context.Context, id string) (ResourceInfo, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		slog.WarnContext(ctx, "主机资源目标为空", "operation", "host_resources_target")
		return ResourceInfo{}, ErrHostNotFound
	}
	var host model.Host
	if err := s.db.WithContext(ctx).Where(&model.Host{ID: id}).First(&host).Error; err != nil {
		slog.WarnContext(ctx, "查询主机资源目标失败", "operation", "host_resources_target", "host_id", id, "err", err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ResourceInfo{}, ErrHostNotFound
		}
		return ResourceInfo{}, errors.New("查询主机系统资源失败")
	}
	return s.ResourceSnapshot(host), nil
}

// ResourceSnapshot 为已经通过项目范围校验的主机构造展示结果。
func (s *Service) ResourceSnapshot(host model.Host) ResourceInfo {
	info := ResourceInfo{State: cache.SnapshotUnavailable}
	if host.IsActive {
		value, status := s.resources.Get(resourceIdentity(host), time.Now())
		info = cloneResources(value)
		info.State, info.CollectedAt = status.State, status.CollectedAt
	}
	info.Source = "ssh"
	if host.Mode == model.HostModeLocal {
		info.Source = "local"
	}
	info.Scope, info.DiskMount = "current_environment", "/"
	return info
}

func (s *Service) RunResourceSync(ctx context.Context) error {
	return cache.RunSnapshotWorkers(ctx, 5*time.Second, "host_resource_sync", resourceConcurrency, s.scanResources, func(ctx context.Context, host model.Host) {
		s.syncResource(ctx, host, s.collectResources)
	})
}

func (s *Service) scanResources(ctx context.Context, yield func(string, model.Host) bool) error {
	var hosts []model.Host
	db := s.db.WithContext(ctx)
	activeProjects := db.Model(&model.Project{}).Select("uuid").Where("is_active = ?", true)
	return db.Where("is_active = ? AND project_id IN (?)", true, activeProjects).FindInBatches(&hosts, 64, func(_ *gorm.DB, _ int) error {
		for _, host := range hosts {
			key := resourceIdentity(host)
			if s.resources.Due(key, time.Now(), resourceTTL, resourceFailureTTL) && !yield(host.ID, host) {
				return ctx.Err()
			}
		}
		return nil
	}).Error
}

func (s *Service) syncResource(ctx context.Context, host model.Host, collect func(context.Context, model.Host) (ResourceInfo, error)) {
	key := resourceIdentity(host)
	scoped := database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: host.ProjectID, DepartmentID: host.DepartmentID})
	var current model.Host
	db := s.db.WithContext(scoped)
	activeProjects := db.Model(&model.Project{}).Select("uuid").Where("is_active = ?", true)
	if err := db.Where("project_id IN (?)", activeProjects).First(&current, "uuid = ?", host.ID).Error; err != nil || resourceIdentity(current) != key {
		slog.DebugContext(ctx, "主机资源采集目标已变化", "operation", "host_resources_changed", "host_id", host.ID, "err", err)
		return
	}
	probeCtx, cancel := context.WithTimeout(scoped, resourceTimeout)
	defer cancel()
	info, err := collect(probeCtx, current)
	if ctx.Err() != nil {
		return
	}
	if err == nil && info.State != cache.SnapshotOK {
		err = errHostResourcesUnavailable
	}
	if err != nil {
		slog.WarnContext(ctx, "后台读取主机系统资源失败", "operation", "host_resources_collect", "host_id", host.ID, "err", err)
		s.resources.Fail(key, time.Now())
		return
	}
	// 读路径只接受当前连接身份，所以迟到的旧身份结果不可见。
	s.resources.Put(key, info, time.Now())
}

func (s *Service) collectResources(ctx context.Context, host model.Host) (ResourceInfo, error) {
	if host.Mode == model.HostModeLocal && host.ID == model.BuiltinLocalHostID {
		return collectLocalResources(ctx)
	}
	if host.Mode != model.HostModeSSH || !sshclient.ValidFingerprint(host.SSHHostKeyFingerprint) {
		return ResourceInfo{}, errHostResourcesUnavailable
	}
	bundle, err := s.decryptCredential(&host)
	if err != nil {
		return ResourceInfo{}, err
	}
	connector, err := sshclient.NewConnector(dockerHostURL(Input{Address: host.Address, SSHPort: host.SSHPort, SSHUsername: host.SSHUsername}), sshclient.Bundle{Password: bundle.Password, PrivateKey: bundle.PrivateKey, Passphrase: bundle.Passphrase}, host.SSHHostKeyFingerprint, resourceTimeout)
	if err != nil {
		return ResourceInfo{}, err
	}
	client, err := connector.ConnectPinned(ctx)
	if err != nil {
		return ResourceInfo{}, err
	}
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stop()
	session, err := client.NewSession()
	if err != nil {
		return ResourceInfo{}, err
	}
	defer session.Close()
	output := &resourceOutput{limit: 4096, abort: func() { _ = client.Close() }}
	stderr := &resourceOutput{limit: 1024, abort: func() { _ = client.Close() }}
	session.Stdout, session.Stderr = output, stderr
	session.Stdin = strings.NewReader(hostResourceProbeCommand)
	runErr := session.Run("env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C sh -se")
	if ctx.Err() != nil {
		return ResourceInfo{}, ctx.Err()
	}
	if output.exceeded || stderr.exceeded {
		return ResourceInfo{}, fmt.Errorf("%w：采集输出超过限制", errHostResourcesUnavailable)
	}
	if runErr != nil {
		return ResourceInfo{}, fmt.Errorf("执行主机系统资源采集失败：%w", runErr)
	}
	return parseResourceInventory(output.buffer.String())
}

func collectLocalResources(ctx context.Context) (ResourceInfo, error) {
	info := ResourceInfo{State: "unavailable", OS: runtime.GOOS}
	cores, cpuErr := cpu.CountsWithContext(ctx, true)
	if cpuErr == nil && cores > 0 {
		info.CPUCores = &cores
	}
	memory, memoryErr := mem.VirtualMemoryWithContext(ctx)
	if memoryErr == nil && memory.Total > 0 {
		info.MemoryTotalBytes = &memory.Total
	}
	usage, diskErr := disk.UsageWithContext(ctx, "/")
	if diskErr == nil && usage.Total > 0 {
		info.DiskTotalBytes = &usage.Total
	}
	if ctx.Err() != nil {
		return ResourceInfo{}, ctx.Err()
	}
	if info.CPUCores != nil || info.MemoryTotalBytes != nil || info.DiskTotalBytes != nil {
		info.State = "ok"
		if err := errors.Join(cpuErr, memoryErr, diskErr); err != nil {
			slog.DebugContext(ctx, "本地主机有部分指标未能采集", "operation", "host_resources_local_collect", "err", err)
		}
		return info, nil
	}
	return info, errors.Join(errHostResourcesUnavailable, cpuErr, memoryErr, diskErr)
}

// 固定只读协议不接收路径或 Shell 参数，不需要 sudo；缺失工具保留空值。
const hostResourceProbeCommand = `LC_ALL=C; export LC_ALL
edo_os=$(uname -s 2>/dev/null) || edo_os=
printf 'os\t%s\n' "$edo_os"
case "$edo_os" in
Linux)
  edo_cpu=$(getconf _NPROCESSORS_ONLN 2>/dev/null) || edo_cpu=
  edo_memory=$(awk '$1=="MemTotal:"{print $2;exit}' /proc/meminfo 2>/dev/null) || edo_memory=
  printf 'cpu\t%s\nmemory_kib\t%s\n' "$edo_cpu" "$edo_memory";;
Darwin)
  edo_cpu=$(sysctl -n hw.logicalcpu 2>/dev/null) || edo_cpu=
  edo_memory=$(sysctl -n hw.memsize 2>/dev/null) || edo_memory=
  printf 'cpu\t%s\nmemory_bytes\t%s\n' "$edo_cpu" "$edo_memory";;
*)printf 'cpu\t\nmemory_bytes\t\n';;
esac
edo_df=$(df -Pk / 2>/dev/null) || edo_df=
edo_disk=$(printf '%s\n' "$edo_df" | awk 'NR==2{print $2;exit}') || edo_disk=
printf 'disk_kib\t%s\nend\t1\n' "$edo_disk"
`

func parseResourceInventory(value string) (ResourceInfo, error) {
	result := ResourceInfo{State: "unavailable", Scope: "current_environment", DiskMount: "/"}
	if len(value) > 4096 || !strings.HasSuffix(value, "end\t1\n") {
		return result, errHostResourcesUnavailable
	}
	fields := make(map[string]string, 5)
	for _, line := range strings.Split(strings.TrimSuffix(value, "\n"), "\n") {
		key, v, ok := strings.Cut(line, "\t")
		if !ok || strings.ContainsAny(v, "\x00\r\t") {
			return result, errHostResourcesUnavailable
		}
		if _, exists := fields[key]; exists {
			return result, errHostResourcesUnavailable
		}
		switch key {
		case "os", "cpu", "memory_kib", "memory_bytes", "disk_kib", "end":
		default:
			return result, errHostResourcesUnavailable
		}
		fields[key] = v
	}
	if len(fields) != 5 || fields["end"] != "1" {
		return result, errHostResourcesUnavailable
	}
	_, hasCPU := fields["cpu"]
	_, hasDisk := fields["disk_kib"]
	_, hasKiB := fields["memory_kib"]
	_, hasBytes := fields["memory_bytes"]
	if !hasCPU || !hasDisk || hasKiB == hasBytes {
		return result, errHostResourcesUnavailable
	}
	if fields["os"] == "Linux" {
		result.OS = "linux"
	} else if fields["os"] == "Darwin" {
		result.OS = "darwin"
	} else {
		return result, errHostResourcesUnavailable
	}
	number := func(key string, factor uint64) *uint64 {
		text := fields[key]
		if text == "" || strings.ContainsAny(text, "+- .eE") {
			return nil
		}
		n, err := strconv.ParseUint(text, 10, 64)
		if err != nil || n == 0 || n > ^uint64(0)/factor {
			return nil
		}
		n *= factor
		return &n
	}
	if n := number("cpu", 1); n != nil && *n <= 1_000_000 {
		value := int(*n)
		result.CPUCores = &value
	}
	if _, ok := fields["memory_kib"]; ok {
		result.MemoryTotalBytes = number("memory_kib", 1024)
	} else {
		result.MemoryTotalBytes = number("memory_bytes", 1)
	}
	result.DiskTotalBytes = number("disk_kib", 1024)
	if result.CPUCores != nil || result.MemoryTotalBytes != nil || result.DiskTotalBytes != nil {
		result.State = "ok"
	}
	return result, nil
}

type resourceOutput struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	mu       sync.Mutex
	abort    func()
}

func (w *resourceOutput) Write(value []byte) (int, error) {
	w.mu.Lock()
	if len(value) <= w.limit {
		w.limit -= len(value)
		n, err := w.buffer.Write(value)
		w.mu.Unlock()
		return n, err
	}
	w.exceeded = true
	w.mu.Unlock()
	if w.abort != nil {
		w.abort()
	}
	return 0, fmt.Errorf("%w：采集结果超过限制", errHostResourcesUnavailable)
}
