package systemmetrics

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
	"gorm.io/gorm"

	"zrt/internal/messaging"
	"zrt/internal/model"
	"zrt/internal/worker"
)

type workerStatsProvider interface {
	RuntimeStats() worker.RuntimeStats
}

type queueStatsProvider interface {
	QueueStats(context.Context, string) (messaging.QueueStats, error)
}

type HostMetrics struct {
	CPUPercent           float64 `json:"cpu_percent"`
	LogicalCPUs          int     `json:"logical_cpus"`
	MemoryTotalBytes     uint64  `json:"memory_total_bytes"`
	MemoryUsedBytes      uint64  `json:"memory_used_bytes"`
	MemoryAvailableBytes uint64  `json:"memory_available_bytes"`
	MemoryUsedPercent    float64 `json:"memory_used_percent"`
	Load1                float64 `json:"load_1"`
	Load5                float64 `json:"load_5"`
	Load15               float64 `json:"load_15"`
	UptimeSeconds        uint64  `json:"uptime_seconds"`
}

type ProcessMetrics struct {
	CPUPercent    float64 `json:"cpu_percent"`
	RSSBytes      uint64  `json:"rss_bytes"`
	VMSBytes      uint64  `json:"vms_bytes"`
	UptimeSeconds uint64  `json:"uptime_seconds"`
}

type RuntimeMetrics struct {
	Goroutines      int        `json:"goroutines"`
	GOMAXPROCS      int        `json:"gomaxprocs"`
	HeapAllocBytes  uint64     `json:"heap_alloc_bytes"`
	HeapInuseBytes  uint64     `json:"heap_inuse_bytes"`
	StackInuseBytes uint64     `json:"stack_inuse_bytes"`
	SysBytes        uint64     `json:"sys_bytes"`
	NextGCBytes     uint64     `json:"next_gc_bytes"`
	GCCycles        uint32     `json:"gc_cycles"`
	GCPauseTotalMS  float64    `json:"gc_pause_total_ms"`
	LastGCAt        *time.Time `json:"last_gc_at,omitempty"`
}

type JobMetrics struct {
	Total     int64 `json:"total"`
	Pending   int64 `json:"pending"`
	Running   int64 `json:"running"`
	Succeeded int64 `json:"succeeded"`
	Failed    int64 `json:"failed"`
	Canceled  int64 `json:"canceled"`
}

type OutboxMetrics struct {
	Pending int64 `json:"pending"`
	Failed  int64 `json:"failed"`
}

type DatabaseMetrics struct {
	MaxOpenConnections int     `json:"max_open_connections"`
	OpenConnections    int     `json:"open_connections"`
	InUse              int     `json:"in_use"`
	Idle               int     `json:"idle"`
	WaitCount          int64   `json:"wait_count"`
	WaitDurationMS     float64 `json:"wait_duration_ms"`
}

type Snapshot struct {
	CollectedAt time.Time            `json:"collected_at"`
	Host        HostMetrics          `json:"host"`
	Process     ProcessMetrics       `json:"process"`
	Runtime     RuntimeMetrics       `json:"runtime"`
	Worker      worker.RuntimeStats  `json:"worker"`
	Jobs        JobMetrics           `json:"jobs"`
	Outbox      OutboxMetrics        `json:"outbox"`
	Queue       messaging.QueueStats `json:"queue"`
	Database    DatabaseMetrics      `json:"database"`
	Unavailable []string             `json:"unavailable,omitempty"`
}

type Service struct {
	db        *gorm.DB
	sql       *sql.DB
	worker    workerStatsProvider
	queue     queueStatsProvider
	process   *process.Process
	logger    *slog.Logger
	startedAt time.Time

	cpuMu          sync.Mutex
	lastHostCPU    cpu.TimesStat
	lastProcessCPU float64
	lastCPUAt      time.Time
	wasSampled     bool
	errorMu        sync.Mutex
	lastErrorLog   map[string]time.Time
}

func New(db *gorm.DB, sqlDB *sql.DB, taskWorker workerStatsProvider, queue queueStatsProvider, logger *slog.Logger) *Service {
	service := &Service{
		db: db, sql: sqlDB, worker: taskWorker, queue: queue,
		logger: logger, startedAt: time.Now().UTC(), lastErrorLog: make(map[string]time.Time),
	}
	currentProcess, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		logger.Error("初始化进程指标采集失败", "operation", "system_metrics_process_init", "pid", os.Getpid(), "err", err)
		return service
	}
	service.process = currentProcess
	baselineCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	baseline := Snapshot{Host: HostMetrics{LogicalCPUs: runtime.NumCPU()}}
	if err := service.readCPU(baselineCtx, &baseline); err != nil {
		logger.Warn("初始化 CPU 指标基线失败", "operation", "system_metrics_cpu_init", "err", err)
	}
	cancel()
	return service
}

func (s *Service) Snapshot(ctx context.Context) Snapshot {
	now := time.Now().UTC()
	snapshot := Snapshot{CollectedAt: now}
	snapshot.Host.LogicalCPUs = runtime.NumCPU()
	snapshot.Process.UptimeSeconds = uint64(now.Sub(s.startedAt).Seconds())
	snapshot.Runtime = readRuntimeMetrics()
	if s.worker != nil {
		snapshot.Worker = s.worker.RuntimeStats()
	}
	if s.sql != nil {
		snapshot.Database = readDatabaseMetrics(s.sql.Stats())
	}

	if err := s.readCPU(ctx, &snapshot); err != nil {
		s.markUnavailable(&snapshot, "cpu", "采集 CPU 指标失败", err)
	}
	if memory, err := mem.VirtualMemoryWithContext(ctx); err != nil {
		s.markUnavailable(&snapshot, "host_memory", "采集主机内存指标失败", err)
	} else {
		snapshot.Host.MemoryTotalBytes = memory.Total
		snapshot.Host.MemoryUsedBytes = memory.Used
		snapshot.Host.MemoryAvailableBytes = memory.Available
		snapshot.Host.MemoryUsedPercent = roundPercent(memory.UsedPercent)
	}
	if s.process == nil {
		snapshot.Unavailable = append(snapshot.Unavailable, "process")
	} else if memory, err := s.process.MemoryInfoWithContext(ctx); err != nil {
		s.markUnavailable(&snapshot, "process_memory", "采集进程内存指标失败", err)
	} else {
		snapshot.Process.RSSBytes = memory.RSS
		snapshot.Process.VMSBytes = memory.VMS
	}
	if average, err := load.AvgWithContext(ctx); err != nil {
		s.markUnavailable(&snapshot, "load", "采集系统负载指标失败", err)
	} else {
		snapshot.Host.Load1 = average.Load1
		snapshot.Host.Load5 = average.Load5
		snapshot.Host.Load15 = average.Load15
	}
	if uptime, err := host.UptimeWithContext(ctx); err != nil {
		s.markUnavailable(&snapshot, "uptime", "采集主机运行时间失败", err)
	} else {
		snapshot.Host.UptimeSeconds = uptime
	}
	if jobs, err := s.readJobs(ctx); err != nil {
		s.markUnavailable(&snapshot, "jobs", "查询任务统计失败", err)
	} else {
		snapshot.Jobs = jobs
	}
	if outbox, err := s.readOutbox(ctx); err != nil {
		s.markUnavailable(&snapshot, "outbox", "查询 Outbox 统计失败", err)
	} else {
		snapshot.Outbox = outbox
	}
	if s.queue == nil {
		snapshot.Unavailable = append(snapshot.Unavailable, "queue")
	} else {
		queueCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		queue, err := s.queue.QueueStats(queueCtx, worker.ConsumerName)
		cancel()
		if err != nil {
			s.markUnavailable(&snapshot, "queue", "查询消息队列统计失败", err)
		} else {
			snapshot.Queue = queue
		}
	}
	return snapshot
}

func (s *Service) readCPU(ctx context.Context, snapshot *Snapshot) error {
	hostTimes, err := cpu.TimesWithContext(ctx, false)
	if err != nil {
		return err
	}
	if len(hostTimes) != 1 {
		return fmt.Errorf("CPU 汇总指标数量异常: %d", len(hostTimes))
	}
	var processTotal float64
	if s.process != nil {
		processTimes, err := s.process.TimesWithContext(ctx)
		if err != nil {
			return err
		}
		processTotal = processTimes.User + processTimes.System
	}

	now := time.Now()
	s.cpuMu.Lock()
	defer s.cpuMu.Unlock()
	if s.wasSampled {
		currentTotal := totalCPUTime(hostTimes[0])
		previousTotal := totalCPUTime(s.lastHostCPU)
		totalDelta := currentTotal - previousTotal
		idleDelta := hostTimes[0].Idle + hostTimes[0].Iowait - s.lastHostCPU.Idle - s.lastHostCPU.Iowait
		if totalDelta > 0 {
			snapshot.Host.CPUPercent = roundPercent((totalDelta - idleDelta) / totalDelta * 100)
		}
		elapsed := now.Sub(s.lastCPUAt).Seconds()
		if elapsed > 0 && snapshot.Host.LogicalCPUs > 0 && processTotal >= s.lastProcessCPU {
			snapshot.Process.CPUPercent = roundPercent((processTotal - s.lastProcessCPU) / elapsed / float64(snapshot.Host.LogicalCPUs) * 100)
		}
	}
	s.lastHostCPU = hostTimes[0]
	s.lastProcessCPU = processTotal
	s.lastCPUAt = now
	s.wasSampled = true
	return nil
}

func totalCPUTime(value cpu.TimesStat) float64 {
	return value.User + value.System + value.Idle + value.Nice + value.Iowait + value.Irq + value.Softirq + value.Steal
}

func (s *Service) readJobs(ctx context.Context) (JobMetrics, error) {
	var rows []struct {
		Status model.JobStatus
		Total  int64
	}
	if err := s.db.WithContext(ctx).Model(&model.Job{}).
		Select("status, COUNT(*) AS total").Group("status").Scan(&rows).Error; err != nil {
		return JobMetrics{}, err
	}
	metrics := JobMetrics{}
	for _, row := range rows {
		metrics.Total += row.Total
		switch row.Status {
		case model.JobPending:
			metrics.Pending = row.Total
		case model.JobRunning:
			metrics.Running = row.Total
		case model.JobSucceeded:
			metrics.Succeeded = row.Total
		case model.JobFailed:
			metrics.Failed = row.Total
		case model.JobCanceled:
			metrics.Canceled = row.Total
		}
	}
	return metrics, nil
}

func (s *Service) readOutbox(ctx context.Context) (OutboxMetrics, error) {
	var metrics OutboxMetrics
	if err := s.db.WithContext(ctx).Model(&model.OutboxEvent{}).
		Where("published_at IS NULL AND failed_at IS NULL").Count(&metrics.Pending).Error; err != nil {
		return OutboxMetrics{}, err
	}
	if err := s.db.WithContext(ctx).Model(&model.OutboxEvent{}).
		Where("failed_at IS NOT NULL").Count(&metrics.Failed).Error; err != nil {
		return OutboxMetrics{}, err
	}
	return metrics, nil
}

func (s *Service) markUnavailable(snapshot *Snapshot, component, message string, err error) {
	snapshot.Unavailable = append(snapshot.Unavailable, component)
	s.errorMu.Lock()
	lastLogged := s.lastErrorLog[component]
	if time.Since(lastLogged) < time.Minute {
		s.errorMu.Unlock()
		return
	}
	s.lastErrorLog[component] = time.Now()
	s.errorMu.Unlock()
	s.logger.Error(message, "operation", "system_metrics_snapshot", "component", component, "err", err)
}

func readRuntimeMetrics() RuntimeMetrics {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	metrics := RuntimeMetrics{
		Goroutines: runtime.NumGoroutine(), GOMAXPROCS: runtime.GOMAXPROCS(0),
		HeapAllocBytes: memory.HeapAlloc, HeapInuseBytes: memory.HeapInuse,
		StackInuseBytes: memory.StackInuse, SysBytes: memory.Sys,
		NextGCBytes: memory.NextGC, GCCycles: memory.NumGC,
		GCPauseTotalMS: float64(memory.PauseTotalNs) / float64(time.Millisecond),
	}
	if memory.LastGC > 0 {
		lastGCAt := time.Unix(0, int64(memory.LastGC)).UTC()
		metrics.LastGCAt = &lastGCAt
	}
	return metrics
}

func readDatabaseMetrics(stats sql.DBStats) DatabaseMetrics {
	return DatabaseMetrics{
		MaxOpenConnections: stats.MaxOpenConnections,
		OpenConnections:    stats.OpenConnections,
		InUse:              stats.InUse,
		Idle:               stats.Idle,
		WaitCount:          stats.WaitCount,
		WaitDurationMS:     float64(stats.WaitDuration) / float64(time.Millisecond),
	}
}

func roundPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}
	return math.Round(value*10) / 10
}
