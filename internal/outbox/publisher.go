package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"zrt/internal/model"
)

type MessagePublisher interface {
	Publish(ctx context.Context, subject string, payload []byte, messageID string) error
}

type Publisher struct {
	db          *gorm.DB
	messages    MessagePublisher
	logger      *slog.Logger
	maxAttempts int
	interval    time.Duration
}

func New(db *gorm.DB, messages MessagePublisher, logger *slog.Logger, maxAttempts int) *Publisher {
	return &Publisher{
		db: db, messages: messages, logger: logger,
		maxAttempts: maxAttempts, interval: 500 * time.Millisecond,
	}
}

func (p *Publisher) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		if err := p.PublishBatch(ctx, 100); err != nil {
			p.logger.Error("投递 Outbox 消息失败", "operation", "outbox_publish_batch", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (p *Publisher) PublishBatch(ctx context.Context, limit int) error {
	var events []model.OutboxEvent
	now := time.Now().UTC()
	if err := p.db.WithContext(ctx).
		Where("published_at IS NULL AND failed_at IS NULL AND next_attempt_at <= ?", now).
		Order("id ASC").Limit(limit).Find(&events).Error; err != nil {
		return fmt.Errorf("读取待投递消息失败: %w", err)
	}
	for i := range events {
		if err := p.publishOne(ctx, &events[i]); err != nil {
			p.logger.Error("投递 Outbox 消息失败",
				"operation", "outbox_publish",
				"event_id", events[i].EventID,
				"aggregate_id", events[i].AggregateID,
				"attempt", events[i].PublishAttempts+1,
				"err", err,
			)
		}
	}
	return nil
}

func (p *Publisher) publishOne(ctx context.Context, event *model.OutboxEvent) error {
	err := p.messages.Publish(ctx, event.Subject, event.Payload, event.EventID)
	now := time.Now().UTC()
	if err == nil {
		return p.db.WithContext(ctx).Model(event).Updates(map[string]any{
			"published_at": now,
			"last_error":   "",
		}).Error
	}

	attempts := event.PublishAttempts + 1
	updates := map[string]any{
		"publish_attempts": attempts,
		"last_error":       err.Error(),
		"next_attempt_at":  now.Add(retryDelay(attempts)),
	}
	if attempts >= p.maxAttempts {
		updates["failed_at"] = now
	}
	if updateErr := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if updateErr := tx.Model(event).Updates(updates).Error; updateErr != nil {
			return updateErr
		}
		if attempts >= p.maxAttempts {
			return tx.Model(&model.Job{}).Where("id = ? AND status = ?", event.AggregateID, model.JobPending).
				Updates(map[string]any{
					"status":        model.JobFailed,
					"error_code":    "queue_publish_failed",
					"error_message": "任务进入队列失败，请稍后重试",
					"finished_at":   now,
					"updated_at":    now,
				}).Error
		}
		return nil
	}); updateErr != nil {
		return fmt.Errorf("记录消息投递失败状态时发生错误: %v；原始错误: %w", updateErr, err)
	}
	return err
}

func retryDelay(attempt int) time.Duration {
	delays := []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}
	if attempt <= 0 {
		return delays[0]
	}
	if attempt > len(delays) {
		return delays[len(delays)-1]
	}
	return delays[attempt-1]
}
