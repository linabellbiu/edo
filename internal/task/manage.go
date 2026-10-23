package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/model"
)

var (
	ErrJobNotFound     = errors.New("任务不存在")
	ErrJobState        = errors.New("任务当前状态不允许此操作")
	ErrJobNotRetryable = errors.New("该任务包含非幂等操作，不能人工重试")
)

type View struct {
	ID            string          `json:"id"`
	RowID         uint64          `json:"row_id"`
	PipelineRunID string          `json:"pipeline_run_id,omitempty"`
	Kind          string          `json:"kind"`
	Status        model.JobStatus `json:"status"`
	Attempt       int             `json:"attempt"`
	MaxAttempts   int             `json:"max_attempts"`
	IsIdempotent  bool            `json:"is_idempotent"`
	ErrorCode     string          `json:"error_code"`
	ErrorMessage  string          `json:"error_message"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	StartedAt     *time.Time      `json:"started_at,omitempty"`
	FinishedAt    *time.Time      `json:"finished_at,omitempty"`
}

func (s *Service) Get(ctx context.Context, id string) (*View, error) {
	var job model.Job
	if err := s.db.WithContext(ctx).Where("uuid = ?", id).First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrJobNotFound
		}
		return nil, fmt.Errorf("查询任务详情失败: %w", err)
	}
	view := toView(&job)
	return &view, nil
}

func (s *Service) List(ctx context.Context, status model.JobStatus, limit int) ([]View, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	query := s.db.WithContext(ctx).Order("id DESC").Limit(limit)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var jobs []model.Job
	if err := query.Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("查询任务失败: %w", err)
	}
	result := make([]View, 0, len(jobs))
	for index := range jobs {
		result = append(result, toView(&jobs[index]))
	}
	return result, nil
}

func (s *Service) Cancel(ctx context.Context, id string) error {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&model.Job{}).
		Where("uuid = ? AND status = ?", id, model.JobPending).
		Updates(map[string]any{
			"status": model.JobCanceled, "finished_at": now, "updated_at": now,
			"error_code": "canceled_by_user", "error_message": "任务已由用户取消",
		})
	if result.Error != nil {
		return fmt.Errorf("取消任务失败: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.Job{}).Where("uuid = ?", id).Count(&count).Error; err != nil {
		return fmt.Errorf("检查任务状态失败: %w", err)
	}
	if count == 0 {
		return ErrJobNotFound
	}
	return ErrJobState
}

func (s *Service) Retry(ctx context.Context, id string) (*model.Job, error) {
	var source model.Job
	if err := s.db.WithContext(ctx).First(&source, "uuid = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrJobNotFound
		}
		return nil, fmt.Errorf("查询待重试任务失败: %w", err)
	}
	if source.Status != model.JobFailed {
		return nil, ErrJobState
	}
	if !source.IsIdempotent {
		return nil, ErrJobNotRetryable
	}
	return s.Create(ctx, CreateInput{
		ProjectID: source.ProjectID, DepartmentID: source.DepartmentID,
		Kind: source.Kind, Subject: source.Subject, Payload: source.Payload,
		MaxAttempts: source.MaxAttempts, Idempotent: true,
		IdempotencyKey: "manual-retry:" + source.ID + ":" + uuid.NewString(),
	})
}

func toView(job *model.Job) View {
	view := View{
		ID: job.ID, RowID: job.RowID, Kind: job.Kind, Status: job.Status, Attempt: job.Attempt,
		MaxAttempts: job.MaxAttempts, IsIdempotent: job.IsIdempotent,
		ErrorCode: job.ErrorCode, ErrorMessage: job.ErrorMessage,
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
		StartedAt: job.StartedAt, FinishedAt: job.FinishedAt,
	}
	if job.Kind == "pipeline.build" || job.Kind == "pipeline.deploy" {
		var payload struct {
			PipelineRunID string `json:"pipeline_run_id"`
		}
		if json.Unmarshal(job.Payload, &payload) == nil {
			if id, err := uuid.Parse(payload.PipelineRunID); err == nil {
				view.PipelineRunID = id.String()
			}
		}
	}
	return view
}
