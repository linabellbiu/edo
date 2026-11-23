package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"edo/internal/model"
	"edo/internal/task"
)

const (
	// reclaimBatchSize 限制每轮回收的任务数，避免长时间占用数据库连接。
	reclaimBatchSize = 50
	// reclaimInterval 是回收扫描周期。租约过期后最多再等一个周期即可被接管。
	reclaimInterval = 15 * time.Second
	// abandonedErrorCode 标识执行者失联、且次数已耗尽、无法再次投递的任务。
	abandonedErrorCode    = "lease_expired"
	abandonedErrorMessage = "任务执行者已失联，系统已回收该任务"
)

// Reclaimer 找回租约已过期却仍停留在 running 的任务。
//
// Worker 在执行中崩溃、消息被确认或 JetStream 投递次数耗尽时，数据库中的 Job 会永久 running，
// 没有任何消息会再次触发它。回收器把仍可重试的任务放回 pending 并写入新的 Outbox 事件，
// 由现有投递链路重新下发；次数已耗尽的任务则直接走终态失败收敛。
type Reclaimer struct {
	db       *gorm.DB
	registry *Registry
	logger   *slog.Logger
}

// NewReclaimer 创建回收器。registry 用于执行业务失败收敛，可以为 nil（此时只更新任务本身）。
func NewReclaimer(db *gorm.DB, registry *Registry, logger *slog.Logger) *Reclaimer {
	return &Reclaimer{db: db, registry: registry, logger: logger}
}

// Run 按固定周期回收过期任务，直到 ctx 结束。
func (r *Reclaimer) Run(ctx context.Context) error {
	ticker := time.NewTicker(reclaimInterval)
	defer ticker.Stop()
	for {
		if _, err := r.ReclaimExpired(ctx, time.Now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
			r.logger.Error("回收过期任务失败", "operation", "worker_reclaim", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// ReclaimExpired 处理一轮租约已过期的任务，返回本轮实际回收的数量。
func (r *Reclaimer) ReclaimExpired(ctx context.Context, now time.Time) (int, error) {
	var candidateIDs []string
	if err := r.db.WithContext(ctx).Model(&model.Job{}).
		Where("status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at < ?", model.JobRunning, now).
		Order("lease_expires_at ASC").Limit(reclaimBatchSize).
		Pluck("uuid", &candidateIDs).Error; err != nil {
		return 0, fmt.Errorf("查询过期任务失败: %w", err)
	}
	reclaimed := 0
	for _, jobID := range candidateIDs {
		ok, err := r.reclaimOne(ctx, jobID, now)
		if err != nil {
			r.logger.Error("回收单个过期任务失败", "operation", "worker_reclaim_job", "job_id", jobID, "err", err)
			continue
		}
		if ok {
			reclaimed++
		}
	}
	return reclaimed, nil
}

// reclaimOne 在一个事务内完成单个任务的回收。
// 每一步都用带租约过期条件的更新，与心跳续约、Worker 正常完成之间的竞争只有一方会生效。
func (r *Reclaimer) reclaimOne(ctx context.Context, jobID string, now time.Time) (bool, error) {
	var done bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.Job
		err := tx.First(&job, "uuid = ? AND status = ? AND lease_expires_at < ?", jobID, model.JobRunning, now).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if job.Attempt >= job.MaxAttempts {
			done, err = r.abandon(ctx, tx, job, now)
			return err
		}
		done, err = r.requeue(tx, job, now)
		return err
	})
	return done, err
}

// requeue 把任务放回 pending，并写入一条新的 Outbox 事件，由投递链路重新下发。
// 清空租约字段，使 Worker 的 claim 条件（pending 或租约过期）能够接管。
func (r *Reclaimer) requeue(tx *gorm.DB, job model.Job, now time.Time) (bool, error) {
	result := tx.Model(&model.Job{}).
		Where("uuid = ? AND status = ? AND lease_expires_at < ?", job.ID, model.JobRunning, now).
		Updates(map[string]any{
			"status":           model.JobPending,
			"lease_owner":      "",
			"lease_expires_at": nil,
			"updated_at":       now,
		})
	if result.Error != nil {
		return false, fmt.Errorf("回收任务状态失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return false, nil
	}

	message, err := json.Marshal(task.Message{
		Version: 1, JobID: job.ID, Kind: job.Kind, MaxAttempts: job.MaxAttempts,
		Payload: json.RawMessage(job.Payload),
	})
	if err != nil {
		return false, fmt.Errorf("序列化回收任务消息失败: %w", err)
	}
	if err := tx.Create(&model.OutboxEvent{
		ProjectID: job.ProjectID, DepartmentID: job.DepartmentID,
		EventID:       uuid.NewString(),
		AggregateID:   job.ID,
		Subject:       job.Subject,
		Payload:       datatypes.JSON(message),
		NextAttemptAt: now,
		CreatedAt:     now,
	}).Error; err != nil {
		return false, fmt.Errorf("写入回收任务 Outbox 失败: %w", err)
	}
	r.logger.Warn("已回收租约过期的任务并重新入队",
		"operation", "worker_reclaim_requeue", "job_id", job.ID, "kind", job.Kind, "attempt", job.Attempt)
	return true, nil
}

// abandon 处理次数已耗尽的任务：写入失败状态并调用业务收敛钩子。
func (r *Reclaimer) abandon(ctx context.Context, tx *gorm.DB, job model.Job, now time.Time) (bool, error) {
	result := tx.Model(&model.Job{}).
		Where("uuid = ? AND status = ? AND lease_expires_at < ?", job.ID, model.JobRunning, now).
		Updates(map[string]any{
			"status":           model.JobFailed,
			"error_code":       abandonedErrorCode,
			"error_message":    abandonedErrorMessage,
			"finished_at":      now,
			"lease_owner":      "",
			"lease_expires_at": nil,
			"updated_at":       now,
		})
	if result.Error != nil {
		return false, fmt.Errorf("标记过期任务失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return false, nil
	}
	if r.registry != nil {
		if err := r.registry.ResolveTerminalFailure(ctx, tx, job, abandonedErrorCode, abandonedErrorMessage); err != nil {
			return false, fmt.Errorf("收敛过期任务的业务状态失败: %w", err)
		}
	}
	r.logger.Warn("过期任务已达到最大执行次数，标记为失败",
		"operation", "worker_reclaim_abandon", "job_id", job.ID, "kind", job.Kind, "attempt", job.Attempt)
	return true, nil
}
