package worker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/model"
	"edo/internal/task"
)

func TestReclaimerRequeuesExpiredLeaseWithRetriesRemaining(t *testing.T) {
	db := openWorkerTestDB(t, "reclaim_requeue")
	job, _ := createWorkerTestJob(t, db, "system.noop", 4)
	expireWorkerTestLease(t, db, job.ID, 1, time.Now().UTC().Add(-time.Minute))

	reclaimer := NewReclaimer(db, NewRegistry(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	count, err := reclaimer.ReclaimExpired(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("回收过期任务失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("回收数量不符: got=%d want=1", count)
	}

	var stored model.Job
	if err := db.First(&stored, "uuid = ?", job.ID).Error; err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if stored.Status != model.JobPending || stored.LeaseOwner != "" || stored.LeaseExpiresAt != nil {
		t.Fatalf("任务未正确回收为待处理: status=%s owner=%s lease=%v", stored.Status, stored.LeaseOwner, stored.LeaseExpiresAt)
	}

	var events []model.OutboxEvent
	if err := db.Where("aggregate_id = ?", job.ID).Order("id ASC").Find(&events).Error; err != nil {
		t.Fatalf("读取 Outbox 事件失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("应当额外写入一条新的 Outbox 事件: got=%d", len(events))
	}
	var message task.Message
	if err := json.Unmarshal(events[len(events)-1].Payload, &message); err != nil {
		t.Fatalf("解析回收事件消息失败: %v", err)
	}
	if message.JobID != job.ID || message.Kind != "system.noop" {
		t.Fatalf("回收事件消息内容不符: %+v", message)
	}
}

func TestReclaimerAbandonsJobWithoutRemainingAttemptsAndResolvesBusinessFailure(t *testing.T) {
	db := openWorkerTestDB(t, "reclaim_abandon")
	job, _ := createWorkerTestJob(t, db, "pipeline.build", 1)
	expireWorkerTestLease(t, db, job.ID, 1, time.Now().UTC().Add(-time.Minute))

	registry := NewRegistry()
	var resolvedID, resolvedCode string
	if err := registry.RegisterTerminalFailureHook("pipeline.build", func(_ context.Context, _ *gorm.DB, j model.Job, code, _ string) error {
		resolvedID = j.ID
		resolvedCode = code
		return nil
	}); err != nil {
		t.Fatalf("注册终止失败钩子失败: %v", err)
	}

	reclaimer := NewReclaimer(db, registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
	count, err := reclaimer.ReclaimExpired(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("回收过期任务失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("回收数量不符: got=%d want=1", count)
	}
	if resolvedID != job.ID || resolvedCode != abandonedErrorCode {
		t.Fatalf("未调用业务收敛钩子: id=%s code=%s", resolvedID, resolvedCode)
	}

	var stored model.Job
	if err := db.First(&stored, "uuid = ?", job.ID).Error; err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if stored.Status != model.JobFailed || stored.ErrorCode != abandonedErrorCode {
		t.Fatalf("任务未正确标记为失败: status=%s code=%s", stored.Status, stored.ErrorCode)
	}
}

func TestReclaimerIgnoresJobsWithFreshLease(t *testing.T) {
	db := openWorkerTestDB(t, "reclaim_fresh")
	job, _ := createWorkerTestJob(t, db, "system.noop", 4)
	expireWorkerTestLease(t, db, job.ID, 1, time.Now().UTC().Add(time.Minute))

	reclaimer := NewReclaimer(db, NewRegistry(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	count, err := reclaimer.ReclaimExpired(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("回收过期任务失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("租约未过期的任务不应被回收: got=%d", count)
	}

	var stored model.Job
	if err := db.First(&stored, "uuid = ?", job.ID).Error; err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if stored.Status != model.JobRunning || stored.LeaseOwner != "worker-test" {
		t.Fatalf("租约未过期的任务状态被意外改变: status=%s owner=%s", stored.Status, stored.LeaseOwner)
	}
}

// expireWorkerTestLease 把任务置为运行中并设定租约到期时间，模拟执行者已经失联。
func expireWorkerTestLease(t *testing.T, db *gorm.DB, jobID string, attempt int, leaseExpiresAt time.Time) {
	t.Helper()
	if err := db.Model(&model.Job{}).Where("uuid = ?", jobID).Updates(map[string]any{
		"status": model.JobRunning, "attempt": attempt, "lease_owner": "worker-test",
		"lease_expires_at": leaseExpiresAt,
	}).Error; err != nil {
		t.Fatalf("准备运行中任务失败: %v", err)
	}
}
