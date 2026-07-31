package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/model"
)

const (
	maximumRunLogMessageBytes  = 16 * 1024
	maximumPipelineRunLogBytes = 2 * 1024 * 1024
	maximumBuildLogBytes       = 2 * 1024 * 1024
	buildLogChunkBytes         = 8 * 1024
)

const pipelineRunLogTruncatedMessage = "…本次流水线日志已达到 2 MiB 上限，后续日志不再保存"

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
	stage = truncateRunLogText(strings.TrimSpace(stage), 32)
	level = truncateRunLogText(strings.TrimSpace(level), 16)
	if level == "" {
		level = "info"
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.PipelineRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "log_bytes", "log_truncated").First(&run, "id = ?", runID).Error; err != nil {
			return err
		}
		if run.LogTruncated {
			return nil
		}
		payloadLimit := maximumPipelineRunLogBytes - len(pipelineRunLogTruncatedMessage)
		remaining := payloadLimit - int(run.LogBytes)
		truncated := remaining <= 0 || len(message) > remaining
		if remaining > 0 {
			stored := message
			if len(stored) > remaining {
				stored = truncateUTF8Bytes(stored, remaining)
			}
			if stored != "" {
				if err := tx.Create(&model.PipelineRunLog{
					PipelineRunID: runID, Stage: stage, Level: level, Message: stored, CreatedAt: time.Now().UTC(),
				}).Error; err != nil {
					return err
				}
				run.LogBytes += uint64(len(stored))
			}
		}
		if truncated || int(run.LogBytes) >= payloadLimit {
			if err := tx.Create(&model.PipelineRunLog{
				PipelineRunID: runID, Stage: stage, Level: "warning",
				Message: pipelineRunLogTruncatedMessage, CreatedAt: time.Now().UTC(),
			}).Error; err != nil {
				return err
			}
			run.LogBytes += uint64(len(pipelineRunLogTruncatedMessage))
			run.LogTruncated = true
		}
		return tx.Model(&model.PipelineRun{}).Where("id = ?", runID).
			Updates(map[string]any{"log_bytes": run.LogBytes, "log_truncated": run.LogTruncated}).Error
	})
	if err != nil && s.logger != nil {
		s.logger.Error("写入流水线执行日志失败", "operation", "pipeline_log_append", "pipeline_run_id", runID, "stage", stage, "err", err)
	}
}

func truncateUTF8Bytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for value != "" && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
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
	redactions [][]byte
}

func (s *Service) newBuildLogWriter(ctx context.Context, runID, stage string, redactions ...string) io.WriteCloser {
	return s.newExecutionLogWriter(ctx, runID, stage, "构建", redactions...)
}

func (s *Service) newExecutionLogWriter(
	ctx context.Context,
	runID, stage, outputName string,
	redactions ...string,
) io.WriteCloser {
	outputName = strings.TrimSpace(outputName)
	if outputName == "" {
		outputName = "任务"
	}
	return &buildLogWriter{
		service: s, ctx: ctx, runID: runID, stage: stage,
		outputName: outputName, lastFlush: time.Now(), redactions: normalizeLogRedactions(redactions),
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
		w.flushFinal()
		w.service.appendRunLog(w.ctx, w.runID, w.stage, "warning", w.outputName+"输出超过 2 MiB，后续日志已截断。\n")
	}
	return originalLength, nil
}

func (w *buildLogWriter) Close() error {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	w.flushFinal()
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
		cut = w.safeFlushLength(cut)
		if cut == 0 {
			break
		}
		w.flush(cut)
	}
	if len(w.pending) > 0 && bytes.IndexByte(w.pending, '\n') >= 0 && time.Since(w.lastFlush) >= 250*time.Millisecond {
		cut := bytes.LastIndexByte(w.pending, '\n') + 1
		if cut = w.safeFlushLength(cut); cut > 0 {
			w.flush(cut)
		}
	}
}

func (w *buildLogWriter) flushFinal() {
	if len(w.pending) == 0 {
		return
	}
	message := redactLogBytes(w.pending, w.redactions)
	w.pending = nil
	for len(message) > 0 {
		cut := min(len(message), buildLogChunkBytes)
		w.lastFlush = time.Now()
		w.service.appendRunLog(w.ctx, w.runID, w.stage, "output", string(message[:cut]))
		message = message[cut:]
	}
}

func (w *buildLogWriter) flush(length int) {
	message := string(redactLogBytes(w.pending[:length], w.redactions))
	w.pending = w.pending[length:]
	w.lastFlush = time.Now()
	w.service.appendRunLog(w.ctx, w.runID, w.stage, "output", message)
}

func (w *buildLogWriter) markTruncated() {
	w.truncated = true
	w.service.appendRunLog(w.ctx, w.runID, w.stage, "warning", w.outputName+"输出超过 2 MiB，后续日志已截断。\n")
}

func (w *buildLogWriter) safeFlushLength(length int) int {
	if length <= 0 || len(w.redactions) == 0 {
		return length
	}
	for {
		original := length
		for _, secret := range w.redactions {
			maximumPrefix := min(len(secret)-1, length)
			for prefixLength := maximumPrefix; prefixLength > 0; prefixLength-- {
				start := length - prefixLength
				if bytes.Equal(w.pending[start:length], secret[:prefixLength]) {
					length = start
					break
				}
			}
		}
		if length == original || length == 0 {
			return length
		}
	}
}

func normalizeLogRedactions(values []string) [][]byte {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			unique[value] = struct{}{}
		}
	}
	result := make([][]byte, 0, len(unique))
	for value := range unique {
		result = append(result, []byte(value))
	}
	sort.Slice(result, func(i, j int) bool { return len(result[i]) > len(result[j]) })
	return result
}

func redactLogBytes(message []byte, redactions [][]byte) []byte {
	result := append([]byte(nil), message...)
	for _, secret := range redactions {
		result = bytes.ReplaceAll(result, secret, []byte("[已脱敏]"))
	}
	return result
}
