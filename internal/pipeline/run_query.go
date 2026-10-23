package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/model"
)

var (
	ErrInvalidRunQuery = errors.New("运行记录筛选条件无效，请检查状态、时间和分页")
	ErrRunQueryFailed  = errors.New("查询运行记录失败，请稍后重试")
)

type RunQueryOptions struct {
	ReleasePlanExecutionID string
	Status                 model.PipelineRunStatus
	From                   *time.Time
	To                     *time.Time
	Order                  string
	Page                   int
	PageSize               int
}

type RunQueryResult struct {
	Runs     []model.PipelineRun `json:"pipeline_runs"`
	Total    int64               `json:"total"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"page_size"`
}

// QueryRuns 先按状态和创建时间筛选，再按自增行号排序、分页。
func (s *Service) QueryRuns(ctx context.Context, options RunQueryOptions) (RunQueryResult, error) {
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}
	options.Status = model.PipelineRunStatus(strings.TrimSpace(string(options.Status)))
	options.ReleasePlanExecutionID = strings.TrimSpace(options.ReleasePlanExecutionID)
	options.Order = strings.TrimSpace(options.Order)
	if options.Order == "" {
		options.Order = "desc"
	}
	if options.Page == 0 {
		options.Page = 1
	}
	if options.PageSize == 0 {
		options.PageSize = 50
	}
	invalidField := ""
	if len(options.ReleasePlanExecutionID) > 36 {
		invalidField = "release_plan_execution_id"
	}
	switch options.Status {
	case "", model.PipelineRunDetected, model.PipelineRunReady, model.PipelineRunBlocked,
		model.PipelineRunAwaitingApproval, model.PipelineRunRunning, model.PipelineRunSucceeded,
		model.PipelineRunFailed, model.PipelineRunSkipped, model.PipelineRunCanceled:
	default:
		invalidField = "status"
	}
	if options.Order != "asc" && options.Order != "desc" {
		invalidField = "order"
	}
	if options.Page < 1 || options.PageSize < 1 || options.PageSize > 200 ||
		options.Page-1 > int(^uint(0)>>1)/options.PageSize {
		invalidField = "page"
	}
	if options.From != nil && options.From.IsZero() || options.To != nil && options.To.IsZero() ||
		options.From != nil && options.To != nil && !options.From.Before(*options.To) {
		invalidField = "time"
	}
	if invalidField != "" {
		logger.WarnContext(ctx, "流水线运行筛选参数无效", "operation", "pipeline_run_query", "field", invalidField, "err", ErrInvalidRunQuery)
		return RunQueryResult{}, ErrInvalidRunQuery
	}

	query := s.db.WithContext(ctx).Model(&model.PipelineRun{})
	if options.ReleasePlanExecutionID != "" {
		query = query.Where("release_plan_execution_id = ?", options.ReleasePlanExecutionID)
	}
	if options.Status != "" {
		query = query.Where("status = ?", options.Status)
	}
	if options.From != nil {
		query = query.Where("created_at >= ?", options.From.UTC())
	}
	if options.To != nil {
		query = query.Where("created_at < ?", options.To.UTC())
	}
	result := RunQueryResult{Runs: make([]model.PipelineRun, 0), Page: options.Page, PageSize: options.PageSize}
	if err := query.Session(&gorm.Session{}).Count(&result.Total).Error; err != nil {
		logger.ErrorContext(ctx, "统计流水线运行失败", "operation", "pipeline_run_count", "err", err)
		return RunQueryResult{}, ErrRunQueryFailed
	}
	desc := options.Order == "desc"
	if err := pipelineRunQuery(query.Session(&gorm.Session{})).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: desc}).
		Offset((options.Page - 1) * options.PageSize).Limit(options.PageSize).Find(&result.Runs).Error; err != nil {
		logger.ErrorContext(ctx, "查询流水线运行失败", "operation", "pipeline_run_query", "page", options.Page, "page_size", options.PageSize, "err", err)
		return RunQueryResult{}, ErrRunQueryFailed
	}
	if err := s.enrichPipelineRuns(ctx, result.Runs); err != nil {
		logger.ErrorContext(ctx, "读取流水线运行关联信息失败", "operation", "pipeline_run_enrich", "err", err)
		return RunQueryResult{}, ErrRunQueryFailed
	}
	return result, nil
}
