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

	"edo/internal/model"
	"edo/internal/sshclient"
)

var errHostResourcesUnavailable = errors.New("暂时无法读取主机系统资源")

const resourceTTL = time.Minute
const resourceFailureTTL = 10 * time.Second
const resourceTimeout = 10 * time.Second
const resourceCacheLimit = 256
const resourceConcurrency = 4
const resourcePendingLimit = 64

// ResourceInfo 是当前系统命名空间可见的容量，不表示物理机整盘或容器配额。
type ResourceInfo struct {
	State            string    `json:"state"`
	Source           string    `json:"source"`
	Scope            string    `json:"scope"`
	CPUCores         *int      `json:"cpu_cores"`
	MemoryTotalBytes *uint64   `json:"memory_total_bytes"`
	DiskTotalBytes   *uint64   `json:"disk_total_bytes"`
	OS               string    `json:"os"`
	DiskMount        string    `json:"disk_mount"`
	CollectedAt      time.Time `json:"collected_at"`
}

type resourceEntry struct {
	info    ResourceInfo
	expires time.Time
}
type resourceFlight struct {
	done chan struct{}
	info ResourceInfo
	err  error
}
type resourceCache struct {
	mu      sync.Mutex
	entries map[[32]byte]resourceEntry
	flights map[[32]byte]*resourceFlight
	slots   chan struct{}
	clock   func() time.Time
	collect func(context.Context, model.Host) (ResourceInfo, error)
}

func resourceIdentity(host model.Host) [32]byte {
	value, _ := json.Marshal([]any{host.ID, host.ProjectID, host.DepartmentID, host.Mode, host.IsActive, host.Address, host.SSHPort, host.SSHUsername, host.SSHHostKeyFingerprint, host.SSHCredentialCiphertext})
	return sha256.Sum256(value)
}

func unknownResources(host model.Host, now time.Time) ResourceInfo {
	source := "ssh"
	if host.Mode == model.HostModeLocal {
		source = "local"
	}
	return ResourceInfo{State: "unavailable", Source: source, Scope: "current_environment", DiskMount: "/", CollectedAt: now.UTC()}
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

// Resources 先查项目范围再查缓存；分页读取不会触发全主机 SSH 扫描。
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
	cache := &s.resources
	cache.mu.Lock()
	now := time.Now()
	if cache.clock != nil {
		now = cache.clock()
	}
	if cache.entries == nil {
		cache.entries = make(map[[32]byte]resourceEntry)
		cache.flights = make(map[[32]byte]*resourceFlight)
		cache.slots = make(chan struct{}, resourceConcurrency)
	}
	key := resourceIdentity(host)
	if entry, ok := cache.entries[key]; ok && now.Before(entry.expires) {
		cache.mu.Unlock()
		return cloneResources(entry.info), nil
	}
	if flight := cache.flights[key]; flight != nil {
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return ResourceInfo{}, ctx.Err()
		case <-flight.done:
			return cloneResources(flight.info), flight.err
		}
	}
	if len(cache.flights) >= resourcePendingLimit {
		cache.mu.Unlock()
		slog.WarnContext(ctx, "主机资源采集繁忙", "operation", "host_resources_busy", "host_id", host.ID)
		return unknownResources(host, now), nil
	}
	flight := &resourceFlight{done: make(chan struct{})}
	cache.flights[key] = flight
	collector := cache.collect
	slots := cache.slots
	cache.mu.Unlock()
	probeCtx, cancel := context.WithTimeout(ctx, resourceTimeout)
	var info ResourceInfo
	var probeErr error
	// singleflight 的每键异步任务没有排队上限；这里登记固定目标后在请求自身等待，不另建采集协程。
	select {
	case slots <- struct{}{}:
		if !host.IsActive {
			probeErr = errHostResourcesUnavailable
		} else if collector != nil {
			info, probeErr = collector(probeCtx, host)
		} else {
			info, probeErr = s.collectResources(probeCtx, host)
		}
		<-slots
	case <-probeCtx.Done():
		probeErr = probeCtx.Err()
	}
	cancel()
	if probeErr != nil {
		slog.WarnContext(ctx, "读取主机系统资源失败", "operation", "host_resources_collect", "host_id", host.ID, "err", probeErr)
	}
	cache.mu.Lock()
	now = time.Now()
	if cache.clock != nil {
		now = cache.clock()
	}
	cache.mu.Unlock()
	if probeErr != nil {
		info = unknownResources(host, now)
	}
	info.Source = unknownResources(host, now).Source
	info.Scope = "current_environment"
	info.DiskMount = "/"
	info.CollectedAt = now.UTC()
	var current model.Host
	queryErr := s.db.WithContext(ctx).Where(&model.Host{ID: host.ID}).First(&current).Error
	var resultErr error
	if queryErr != nil || resourceIdentity(current) != key {
		slog.WarnContext(ctx, "主机资源采集期间目标发生变化", "operation", "host_resources_changed", "host_id", host.ID, "err", queryErr)
		info = unknownResources(host, now)
		if errors.Is(queryErr, gorm.ErrRecordNotFound) {
			resultErr = ErrHostNotFound
		} else if ctx.Err() != nil {
			resultErr = ctx.Err()
		}
	}
	cache.mu.Lock()
	if queryErr == nil && resourceIdentity(current) == key && ctx.Err() == nil {
		for k, entry := range cache.entries {
			if !now.Before(entry.expires) {
				delete(cache.entries, k)
			}
		}
		if len(cache.entries) >= resourceCacheLimit {
			var oldest [32]byte
			var expiry time.Time
			for k, entry := range cache.entries {
				if expiry.IsZero() || entry.expires.Before(expiry) {
					oldest, expiry = k, entry.expires
				}
			}
			delete(cache.entries, oldest)
		}
		ttl := resourceTTL
		if info.State != "ok" {
			ttl = resourceFailureTTL
		}
		cache.entries[key] = resourceEntry{info: cloneResources(info), expires: now.Add(ttl)}
	}
	flight.info, flight.err = cloneResources(info), resultErr
	delete(cache.flights, key)
	close(flight.done)
	cache.mu.Unlock()
	return info, resultErr
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
	if err := session.Run("env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C sh -se"); err != nil || output.exceeded || stderr.exceeded || ctx.Err() != nil {
		return ResourceInfo{}, errHostResourcesUnavailable
	}
	return parseResourceInventory(output.buffer.String())
}

func collectLocalResources(ctx context.Context) (ResourceInfo, error) {
	info := ResourceInfo{State: "unavailable", OS: runtime.GOOS}
	if value, err := cpu.CountsWithContext(ctx, true); err == nil && value > 0 {
		info.CPUCores = &value
	}
	if value, err := mem.VirtualMemoryWithContext(ctx); err == nil && value.Total > 0 {
		info.MemoryTotalBytes = &value.Total
	}
	if value, err := disk.UsageWithContext(ctx, "/"); err == nil && value.Total > 0 {
		info.DiskTotalBytes = &value.Total
	}
	if ctx.Err() != nil {
		return ResourceInfo{}, ctx.Err()
	}
	if info.CPUCores != nil || info.MemoryTotalBytes != nil || info.DiskTotalBytes != nil {
		info.State = "ok"
		return info, nil
	}
	return info, errHostResourcesUnavailable
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
