package systemmetrics

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/messaging"
	"zrt/internal/model"
	"zrt/internal/worker"
)

type testWorkerStats struct {
	stats worker.RuntimeStats
}

func (w testWorkerStats) RuntimeStats() worker.RuntimeStats { return w.stats }

type testQueueStats struct {
	stats messaging.QueueStats
	err   error
}

func (q testQueueStats) QueueStats(context.Context, string) (messaging.QueueStats, error) {
	return q.stats, q.err
}

func TestSnapshotIncludesRuntimeTasksAndQueue(t *testing.T) {
	db, sqlDB, logger := openMetricsTestDB(t, "metrics_snapshot")
	now := time.Now().UTC()
	jobs := []model.Job{
		testJob("pending", model.JobPending, now),
		testJob("running", model.JobRunning, now),
		testJob("succeeded", model.JobSucceeded, now),
		testJob("failed", model.JobFailed, now),
		testJob("canceled", model.JobCanceled, now),
	}
	if err := db.Create(&jobs).Error; err != nil {
		t.Fatalf("写入测试任务失败: %v", err)
	}
	failedAt := now
	if err := db.Create(&[]model.OutboxEvent{
		{EventID: "pending", AggregateID: "pending", Subject: "zrt.task.test", Payload: datatypes.JSON(`{}`), NextAttemptAt: now, CreatedAt: now},
		{EventID: "failed", AggregateID: "failed", Subject: "zrt.task.test", Payload: datatypes.JSON(`{}`), NextAttemptAt: now, FailedAt: &failedAt, CreatedAt: now},
	}).Error; err != nil {
		t.Fatalf("写入测试 Outbox 失败: %v", err)
	}

	service := New(db, sqlDB,
		testWorkerStats{stats: worker.RuntimeStats{Instances: 1, Capacity: 4, Active: 2, Executed: 9}},
		testQueueStats{stats: messaging.QueueStats{Connected: true, PendingMessages: 3, AckPending: 2, DeadMessages: 1}},
		logger,
	)
	snapshot := service.Snapshot(context.Background())
	if snapshot.Jobs.Total != 5 || snapshot.Jobs.Pending != 1 || snapshot.Jobs.Running != 1 || snapshot.Jobs.Failed != 1 {
		t.Fatalf("任务统计错误: %+v", snapshot.Jobs)
	}
	if snapshot.Outbox.Pending != 1 || snapshot.Outbox.Failed != 1 {
		t.Fatalf("Outbox 统计错误: %+v", snapshot.Outbox)
	}
	if snapshot.Worker.Active != 2 || snapshot.Queue.PendingMessages != 3 || snapshot.Queue.AckPending != 2 {
		t.Fatalf("Worker 或队列统计错误: worker=%+v queue=%+v", snapshot.Worker, snapshot.Queue)
	}
	if snapshot.Runtime.Goroutines < 1 || snapshot.Host.LogicalCPUs < 1 || snapshot.Process.RSSBytes == 0 {
		t.Fatalf("运行时指标缺失: runtime=%+v host=%+v process=%+v", snapshot.Runtime, snapshot.Host, snapshot.Process)
	}
}

func TestSnapshotKeepsOtherMetricsWhenQueueIsUnavailable(t *testing.T) {
	db, sqlDB, logger := openMetricsTestDB(t, "metrics_partial")
	service := New(db, sqlDB, testWorkerStats{}, testQueueStats{err: errors.New("connection closed")}, logger)
	snapshot := service.Snapshot(context.Background())
	if !slices.Contains(snapshot.Unavailable, "queue") {
		t.Fatalf("未标记不可用队列: %v", snapshot.Unavailable)
	}
	if snapshot.Runtime.Goroutines < 1 || snapshot.CollectedAt.IsZero() {
		t.Fatalf("队列异常时未保留其他指标: %+v", snapshot)
	}
}

func openMetricsTestDB(t *testing.T, name string) (*gorm.DB, *sql.DB, *slog.Logger) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:" + name + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开系统指标测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移系统指标测试数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("读取数据库连接池失败: %v", err)
	}
	return db, sqlDB, logger
}

func testJob(id string, status model.JobStatus, now time.Time) model.Job {
	return model.Job{
		ID: id, Kind: "system.test", Subject: "zrt.task.system.test", Status: status,
		Payload: datatypes.JSON(`{}`), MaxAttempts: 1, CreatedAt: now, UpdatedAt: now,
	}
}
