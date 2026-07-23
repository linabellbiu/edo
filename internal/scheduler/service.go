package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"zrt/internal/model"
	"zrt/internal/notification"
	"zrt/internal/task"
)

var (
	ErrInvalidSchedule    = errors.New("定时任务配置无效")
	ErrScheduleExists     = errors.New("定时任务名称已存在")
	ErrScheduleNotFound   = errors.New("定时任务不存在")
	ErrInvalidTaskPayload = errors.New("定时任务参数无效")
)

var scheduleNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]{1,127}$`)

type NotificationAction struct {
	ChannelID string                     `json:"channel_id"`
	Title     string                     `json:"title"`
	Message   string                     `json:"message"`
	Severity  model.NotificationSeverity `json:"severity"`
}

type Input struct {
	Name           string
	CronExpression string
	Timezone       string
	Action         model.ScheduleAction
	Payload        json.RawMessage
}

type TaskPayload struct {
	ScheduleID  string               `json:"schedule_id"`
	ScheduledAt time.Time            `json:"scheduled_at"`
	Action      model.ScheduleAction `json:"action"`
	Payload     json.RawMessage      `json:"payload"`
}

type Service struct {
	db          *gorm.DB
	notifier    *notification.Service
	maxAttempts int
	logger      *slog.Logger
}

func NewService(db *gorm.DB, notifier *notification.Service, maxAttempts int, logger *slog.Logger) *Service {
	return &Service{db: db, notifier: notifier, maxAttempts: maxAttempts, logger: logger}
}

func (s *Service) List(ctx context.Context) ([]model.ScheduledTask, error) {
	var schedules []model.ScheduledTask
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&schedules).Error; err != nil {
		return nil, fmt.Errorf("查询定时任务失败: %w", err)
	}
	return schedules, nil
}

func (s *Service) Create(ctx context.Context, actorID string, input Input) (*model.ScheduledTask, error) {
	name, expression, timezone, action, payload, nextRunAt, err := normalizeInput(input, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	item := &model.ScheduledTask{
		ID: uuid.NewString(), Name: name, CronExpression: expression, Timezone: timezone,
		Action: action, Payload: datatypes.JSON(payload), IsActive: true, NextRunAt: nextRunAt,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(item).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrScheduleExists
		}
		return nil, fmt.Errorf("创建定时任务失败: %w", err)
	}
	return item, nil
}

func (s *Service) Update(ctx context.Context, id string, input Input) (*model.ScheduledTask, error) {
	var item model.ScheduledTask
	if err := s.db.WithContext(ctx).First(&item, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrScheduleNotFound
		}
		return nil, fmt.Errorf("查询定时任务失败: %w", err)
	}
	name, expression, timezone, action, payload, nextRunAt, err := normalizeInput(input, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&item).Updates(map[string]any{
		"name": name, "cron_expression": expression, "timezone": timezone,
		"action": action, "payload": datatypes.JSON(payload), "next_run_at": nextRunAt, "updated_at": now,
	}).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrScheduleExists
		}
		return nil, fmt.Errorf("更新定时任务失败: %w", err)
	}
	item.Name, item.CronExpression, item.Timezone, item.Action = name, expression, timezone, action
	item.Payload, item.NextRunAt, item.UpdatedAt = datatypes.JSON(payload), nextRunAt, now
	return &item, nil
}

func (s *Service) SetActive(ctx context.Context, id string, active bool) error {
	var item model.ScheduledTask
	if err := s.db.WithContext(ctx).First(&item, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrScheduleNotFound
		}
		return fmt.Errorf("查询定时任务失败: %w", err)
	}
	updates := map[string]any{"is_active": active, "updated_at": time.Now().UTC()}
	if active {
		nextRunAt, err := nextTime(item.CronExpression, item.Timezone, time.Now().UTC())
		if err != nil {
			return ErrInvalidSchedule
		}
		updates["next_run_at"] = nextRunAt
	}
	if err := s.db.WithContext(ctx).Model(&item).Updates(updates).Error; err != nil {
		return fmt.Errorf("修改定时任务状态失败: %w", err)
	}
	return nil
}

func (s *Service) EnqueueDue(ctx context.Context, now time.Time) error {
	var due []model.ScheduledTask
	if err := s.db.WithContext(ctx).Where("is_active = ? AND next_run_at <= ?", true, now.UTC()).
		Order("next_run_at ASC").Limit(100).Find(&due).Error; err != nil {
		return fmt.Errorf("查询到期定时任务失败: %w", err)
	}
	for index := range due {
		if err := s.enqueueOne(ctx, &due[index], now.UTC()); err != nil {
			s.logger.Error("投递到期定时任务失败", "operation", "scheduler_enqueue", "schedule_id", due[index].ID, "err", err)
		}
	}
	return nil
}

func (s *Service) Execute(ctx context.Context, payload TaskPayload) error {
	if payload.ScheduleID == "" || payload.ScheduledAt.IsZero() || payload.Action != model.ScheduleNotification {
		return ErrInvalidTaskPayload
	}
	var action NotificationAction
	if err := decodeStrict(payload.Payload, &action); err != nil || !validNotificationAction(action) {
		return ErrInvalidTaskPayload
	}
	_, err := s.notifier.Enqueue(ctx, notification.EnqueueInput{
		ChannelID: action.ChannelID, Title: action.Title, Message: action.Message,
		Severity: action.Severity, Source: "scheduler", SourceID: payload.ScheduleID,
		DedupeKey: "schedule:" + payload.ScheduleID + ":" + payload.ScheduledAt.UTC().Format(time.RFC3339),
	})
	return err
}

func (s *Service) Run(ctx context.Context, pollInterval time.Duration) {
	if pollInterval <= 0 {
		pollInterval = 15 * time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if err := s.EnqueueDue(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			s.logger.Error("定时任务扫描失败", "operation", "scheduler_scan", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) enqueueOne(ctx context.Context, due *model.ScheduledTask, now time.Time) error {
	scheduledAt := due.NextRunAt.UTC()
	nextRunAt, err := nextTime(due.CronExpression, due.Timezone, now)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.ScheduledTask{}).
			Where("id = ? AND is_active = ? AND next_run_at = ?", due.ID, true, due.NextRunAt).
			Updates(map[string]any{"next_run_at": nextRunAt, "last_run_at": scheduledAt, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		job, err := task.NewService(tx, s.maxAttempts).Create(ctx, task.CreateInput{
			Kind: "scheduler.execute", Subject: "zrt.task.scheduler.execute", Idempotent: true,
			IdempotencyKey: "schedule:" + due.ID + ":" + scheduledAt.Format(time.RFC3339),
			Payload: TaskPayload{
				ScheduleID: due.ID, ScheduledAt: scheduledAt, Action: due.Action,
				Payload: append(json.RawMessage(nil), due.Payload...),
			},
		})
		if err != nil {
			return err
		}
		return tx.Model(&model.ScheduledTask{}).Where("id = ?", due.ID).Update("last_job_id", job.ID).Error
	})
}

func normalizeInput(input Input, after time.Time) (string, string, string, model.ScheduleAction, []byte, time.Time, error) {
	name := strings.TrimSpace(input.Name)
	expression := strings.TrimSpace(input.CronExpression)
	timezone := strings.TrimSpace(input.Timezone)
	if timezone == "" {
		timezone = "Asia/Shanghai"
	}
	if !scheduleNamePattern.MatchString(name) || len(expression) > 128 || len(timezone) > 64 || input.Action != model.ScheduleNotification {
		return "", "", "", "", nil, time.Time{}, ErrInvalidSchedule
	}
	var action NotificationAction
	if err := decodeStrict(input.Payload, &action); err != nil || !validNotificationAction(action) {
		return "", "", "", "", nil, time.Time{}, ErrInvalidSchedule
	}
	payload, err := json.Marshal(action)
	if err != nil {
		return "", "", "", "", nil, time.Time{}, ErrInvalidSchedule
	}
	nextRunAt, err := nextTime(expression, timezone, after)
	if err != nil {
		return "", "", "", "", nil, time.Time{}, ErrInvalidSchedule
	}
	return name, expression, timezone, input.Action, payload, nextRunAt, nil
}

func nextTime(expression, timezone string, after time.Time) (time.Time, error) {
	if len(strings.Fields(expression)) != 5 {
		return time.Time{}, errors.New("只支持标准五段 Cron 表达式")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, err
	}
	schedule, err := cron.ParseStandard(expression)
	if err != nil {
		return time.Time{}, err
	}
	return schedule.Next(after.In(location)).UTC(), nil
}

func validNotificationAction(action NotificationAction) bool {
	return action.ChannelID != "" && strings.TrimSpace(action.Title) != "" && strings.TrimSpace(action.Message) != "" &&
		len([]rune(action.Title)) <= 255 && len([]rune(action.Message)) <= 8192 &&
		(action.Severity == model.NotificationInfo || action.Severity == model.NotificationWarning || action.Severity == model.NotificationCritical)
}

func decodeStrict(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("任务参数包含多余内容")
	}
	return nil
}
