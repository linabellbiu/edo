package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"zrt/internal/model"
)

const (
	maximumRunLogMessageBytes = 16 * 1024
	maximumBuildLogBytes      = 2 * 1024 * 1024
	buildLogChunkBytes        = 8 * 1024
)

var ErrInvalidExecutionLogFilter = errors.New("日志查询条件无效")

type ExecutionLogFilter struct {
	BeforeID uint64
	Limit    int
	Level    string
	Query    string
}

type ExecutionLogView struct {
	ID              uint64    `json:"id"`
	PipelineRunID   string    `json:"pipeline_run_id"`
	ApplicationID   string    `json:"application_id"`
	ApplicationName string    `json:"application_name"`
	Stage           string    `json:"stage"`
	Level           string    `json:"level"`
	Message         string    `json:"message"`
	CreatedAt       time.Time `json:"created_at"`
}

func (s *Service) ListExecutionLogs(ctx context.Context, filter ExecutionLogFilter) ([]ExecutionLogView, error) {
	if filter.Limit < 1 || filter.Limit > 500 {
		filter.Limit = 100
	}
	filter.Level = strings.ToLower(strings.TrimSpace(filter.Level))
	if filter.Level != "" && filter.Level != "info" && filter.Level != "output" &&
		filter.Level != "warning" && filter.Level != "error" && filter.Level != "success" {
		return nil, fmt.Errorf("%w：日志级别无效", ErrInvalidExecutionLogFilter)
	}
	filter.Query = strings.TrimSpace(filter.Query)
	if len(filter.Query) > 128 {
		return nil, fmt.Errorf("%w：日志搜索内容过长", ErrInvalidExecutionLogFilter)
	}

	query := s.db.WithContext(ctx).Table("pipeline_run_logs AS log").
		Select(`log.id, log.pipeline_run_id, run.application_id,
			COALESCE(application.name, '') AS application_name,
			log.stage, log.level, log.message, log.created_at`).
		Joins("JOIN pipeline_runs AS run ON run.id = log.pipeline_run_id").
		Joins("LEFT JOIN applications AS application ON application.id = run.application_id").
		Order("log.id DESC").Limit(filter.Limit)
	if filter.BeforeID > 0 {
		query = query.Where("log.id < ?", filter.BeforeID)
	}
	if filter.Level != "" {
		query = query.Where("log.level = ?", filter.Level)
	}
	if filter.Query != "" {
		pattern := "%" + strings.ToLower(filter.Query) + "%"
		query = query.Where("LOWER(log.message) LIKE ? OR LOWER(log.stage) LIKE ? OR LOWER(application.name) LIKE ? OR LOWER(log.pipeline_run_id) LIKE ?", pattern, pattern, pattern, pattern)
	}
	var logs []ExecutionLogView
	if err := query.Scan(&logs).Error; err != nil {
		return nil, fmt.Errorf("查询流水线日志失败: %w", err)
	}
	return logs, nil
}

func (s *Service) ListRunLogs(
	ctx context.Context,
	runID string,
	afterID uint64,
	limit int,
) ([]model.PipelineRunLog, model.PipelineRunStatus, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).Select("id", "status").First(&run, "id = ?", strings.TrimSpace(runID)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", ErrPipelineRunNotFound
		}
		return nil, "", err
	}
	var logs []model.PipelineRunLog
	if err := s.db.WithContext(ctx).
		Where("pipeline_run_id = ? AND id > ?", run.ID, afterID).
		Order("id ASC").Limit(limit).Find(&logs).Error; err != nil {
		return nil, "", err
	}
	return logs, run.Status, nil
}

func (s *Service) appendRunLog(ctx context.Context, runID, stage, level, message string) {
	message = normalizeRunLogMessage(message)
	if strings.TrimSpace(runID) == "" || message == "" {
		return
	}
	entry := model.PipelineRunLog{
		PipelineRunID: runID,
		Stage:         truncateRunLogText(strings.TrimSpace(stage), 32),
		Level:         truncateRunLogText(strings.TrimSpace(level), 16),
		Message:       message,
		CreatedAt:     time.Now().UTC(),
	}
	if entry.Level == "" {
		entry.Level = "info"
	}
	if err := s.db.WithContext(ctx).Create(&entry).Error; err != nil && s.logger != nil {
		s.logger.Error("写入流水线执行日志失败", "operation", "pipeline_log_append", "pipeline_run_id", runID, "stage", stage, "err", err)
	}
}

func normalizeRunLogMessage(message string) string {
	message = strings.ToValidUTF8(message, "�")
	message = strings.ReplaceAll(message, "\x00", "")
	if len(message) <= maximumRunLogMessageBytes {
		return message
	}
	message = message[:maximumRunLogMessageBytes]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message + "\n…单条日志过长，已截断"
}

func truncateRunLogText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

type buildLogWriter struct {
	service    *Service
	ctx        context.Context
	runID      string
	stage      string
	outputName string
	mutex      sync.Mutex
	pending    []byte
	written    int
	truncated  bool
	lastFlush  time.Time
}

func (s *Service) newBuildLogWriter(ctx context.Context, runID, stage string) io.WriteCloser {
	return s.newExecutionLogWriter(ctx, runID, stage, "构建")
}

func (s *Service) newExecutionLogWriter(ctx context.Context, runID, stage, outputName string) io.WriteCloser {
	outputName = strings.TrimSpace(outputName)
	if outputName == "" {
		outputName = "任务"
	}
	return &buildLogWriter{
		service: s, ctx: ctx, runID: runID, stage: stage,
		outputName: outputName, lastFlush: time.Now(),
	}
}

func (w *buildLogWriter) Write(value []byte) (int, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	originalLength := len(value)
	if w.truncated || originalLength == 0 {
		return originalLength, nil
	}
	remaining := maximumBuildLogBytes - w.written
	if remaining <= 0 {
		w.markTruncated()
		return originalLength, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		w.truncated = true
	}
	w.written += len(value)
	w.pending = append(w.pending, value...)
	w.flushCompleteChunks()
	if w.truncated {
		w.flushPending()
		w.service.appendRunLog(w.ctx, w.runID, w.stage, "warning", w.outputName+"输出超过 2 MiB，后续日志已截断。\n")
	}
	return originalLength, nil
}

func (w *buildLogWriter) Close() error {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	w.flushPending()
	return nil
}

func (w *buildLogWriter) flushCompleteChunks() {
	for len(w.pending) >= buildLogChunkBytes {
		cut := bytes.LastIndexByte(w.pending[:buildLogChunkBytes], '\n')
		if cut < 0 {
			cut = buildLogChunkBytes
		} else {
			cut++
		}
		w.flush(cut)
	}
	if len(w.pending) > 0 && bytes.IndexByte(w.pending, '\n') >= 0 && time.Since(w.lastFlush) >= 250*time.Millisecond {
		cut := bytes.LastIndexByte(w.pending, '\n') + 1
		w.flush(cut)
	}
}

func (w *buildLogWriter) flushPending() {
	for len(w.pending) > 0 {
		cut := min(len(w.pending), buildLogChunkBytes)
		w.flush(cut)
	}
}

func (w *buildLogWriter) flush(length int) {
	message := string(w.pending[:length])
	w.pending = w.pending[length:]
	w.lastFlush = time.Now()
	w.service.appendRunLog(w.ctx, w.runID, w.stage, "output", message)
}

func (w *buildLogWriter) markTruncated() {
	w.truncated = true
	w.service.appendRunLog(w.ctx, w.runID, w.stage, "warning", w.outputName+"输出超过 2 MiB，后续日志已截断。\n")
}
