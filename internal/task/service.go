package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"zrt/internal/model"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{1,63}$`)

type CreateInput struct {
	Kind           string
	Subject        string
	Payload        any
	IdempotencyKey string
	MaxAttempts    int
	Idempotent     bool
}

type Message struct {
	Version     int             `json:"version"`
	JobID       string          `json:"job_id"`
	Kind        string          `json:"kind"`
	MaxAttempts int             `json:"max_attempts"`
	Payload     json.RawMessage `json:"payload"`
}

type Service struct {
	db                 *gorm.DB
	defaultMaxAttempts int
}

func NewService(db *gorm.DB, defaultMaxAttempts int) *Service {
	return &Service{db: db, defaultMaxAttempts: defaultMaxAttempts}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (*model.Job, error) {
	if !namePattern.MatchString(input.Kind) {
		return nil, errors.New("任务类型格式无效")
	}
	if !strings.HasPrefix(input.Subject, "zrt.task.") || !namePattern.MatchString(input.Subject) {
		return nil, errors.New("任务主题格式无效")
	}
	payload, err := json.Marshal(input.Payload)
	if err != nil {
		return nil, fmt.Errorf("序列化任务参数失败: %w", err)
	}
	maxAttempts, err := normalizeMaxAttempts(input, s.defaultMaxAttempts)
	if err != nil {
		return nil, err
	}

	if input.IdempotencyKey != "" {
		var existing model.Job
		err := s.db.WithContext(ctx).Where("idempotency_key = ?", input.IdempotencyKey).First(&existing).Error
		if err == nil {
			return &existing, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("检查任务幂等键失败: %w", err)
		}
	}

	now := time.Now().UTC()
	jobID := uuid.NewString()
	job := &model.Job{
		ID:           jobID,
		Kind:         input.Kind,
		Subject:      input.Subject,
		Status:       model.JobPending,
		Payload:      datatypes.JSON(payload),
		MaxAttempts:  maxAttempts,
		IsIdempotent: input.Idempotent,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if input.IdempotencyKey != "" {
		job.IdempotencyKey = &input.IdempotencyKey
	}
	event := &model.OutboxEvent{
		EventID:       uuid.NewString(),
		AggregateID:   jobID,
		Subject:       input.Subject,
		NextAttemptAt: now,
		CreatedAt:     now,
	}
	message, err := json.Marshal(Message{
		Version: 1, JobID: jobID, Kind: input.Kind, MaxAttempts: maxAttempts, Payload: payload,
	})
	if err != nil {
		return nil, fmt.Errorf("序列化任务消息失败: %w", err)
	}
	event.Payload = datatypes.JSON(message)

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(job).Error; err != nil {
			return err
		}
		return tx.Create(event).Error
	}); err != nil {
		return nil, fmt.Errorf("创建任务失败: %w", err)
	}
	return job, nil
}

func normalizeMaxAttempts(input CreateInput, fallback int) (int, error) {
	maxAttempts := input.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = fallback
	}
	if fallback < 1 || fallback > 20 {
		return 0, errors.New("任务默认最大执行次数配置无效")
	}
	if maxAttempts < 1 || maxAttempts > fallback {
		return 0, fmt.Errorf("任务最大执行次数必须在 1 到 %d 之间", fallback)
	}
	if hasExternalSideEffects(input.Kind) && !input.Idempotent {
		return 1, nil
	}
	return maxAttempts, nil
}

func hasExternalSideEffects(kind string) bool {
	return strings.HasPrefix(kind, "deploy.") || strings.HasPrefix(kind, "rollback.")
}
