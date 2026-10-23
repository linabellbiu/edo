package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/model"
	"edo/internal/task"
)

type MessagePublisher interface {
	Publish(ctx context.Context, subject string, payload []byte, messageID string) error
}

const (
	maximumPublishBatchSize = 100
	payloadBatchSize        = 10
)

type Publisher struct {
	db            *gorm.DB
	messages      MessagePublisher
	logger        *slog.Logger
	maxAttempts   int
	interval      time.Duration
	subjectPrefix string
	deadSubject   string
}

func New(db *gorm.DB, messages MessagePublisher, logger *slog.Logger, natsConfig config.NATS) *Publisher {
	return &Publisher{
		db: db, messages: messages, logger: logger,
		maxAttempts: natsConfig.MaxAttempts, interval: 500 * time.Millisecond,
		subjectPrefix: natsConfig.SubjectPrefix, deadSubject: natsConfig.DeadSubject,
	}
}

func (p *Publisher) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		if err := p.PublishBatch(ctx, maximumPublishBatchSize); err != nil {
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
	if limit <= 0 {
		return nil
	}
	limit = min(limit, maximumPublishBatchSize)
	var ids []uint64
	now := time.Now().UTC()
	if err := p.db.WithContext(ctx).Model(&model.OutboxEvent{}).
		Where("published_at IS NULL AND failed_at IS NULL AND next_attempt_at <= ?", now).
		Order("id ASC").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return fmt.Errorf("读取待投递消息失败: %w", err)
	}
	// 先固定本轮 ID，再小批读取大字段；失败、退避及新插入事件都不会改写本轮集合。
	var events []model.OutboxEvent
	for offset := 0; offset < len(ids); offset += payloadBatchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch := ids[offset:min(offset+payloadBatchSize, len(ids))]
		if err := p.db.WithContext(ctx).Omit("last_error").
			Where("id IN ?", batch).Order("id ASC").Find(&events).Error; err != nil {
			return fmt.Errorf("读取待投递消息失败: %w", err)
		}
		for i := range events {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := p.publishOne(ctx, &events[i]); err != nil {
				p.logger.Error("投递 Outbox 消息失败",
					"operation", "outbox_publish",
					"event_id", events[i].EventID,
					"aggregate_id", events[i].AggregateID,
					"attempt", events[i].PublishAttempts+1,
					"err", err,
				)
			}
			// 不等整轮发布结束就解除已处理载荷的引用。
			events[i] = model.OutboxEvent{}
		}
	}
	return nil
}

func (p *Publisher) publishOne(ctx context.Context, event *model.OutboxEvent) error {
	subject, err := p.transportSubject(event)
	if err == nil {
		err = p.messages.Publish(ctx, subject, event.Payload, event.EventID)
	}
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
			return tx.Model(&model.Job{}).Where("uuid = ? AND status = ?", event.AggregateID, model.JobPending).
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

func (p *Publisher) transportSubject(event *model.OutboxEvent) (string, error) {
	if event.Subject == p.deadSubject || !strings.HasPrefix(event.Subject, task.LogicalSubjectPrefix+".") {
		return event.Subject, nil
	}
	var message task.Message
	if err := json.Unmarshal(event.Payload, &message); err != nil {
		return "", fmt.Errorf("解析任务消息封套失败: %w", err)
	}
	if message.Version != 1 || message.JobID == "" || message.JobID != event.AggregateID ||
		message.MaxAttempts < 1 || message.Payload == nil {
		return "", errors.New("任务消息封套与 Outbox 记录不一致")
	}
	return task.TransportSubject(event.Subject, message.Kind, p.subjectPrefix)
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
