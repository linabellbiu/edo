package logretention

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"edo/internal/configuration"
	"edo/internal/model"
)

type Report struct {
	Enabled             bool      `json:"enabled"`
	PipelineLogsDeleted int64     `json:"pipeline_logs_deleted"`
	AuditLogsDeleted    int64     `json:"audit_logs_deleted"`
	CompletedAt         time.Time `json:"completed_at"`
}

type Service struct {
	db       *gorm.DB
	settings *configuration.Service
	logger   *slog.Logger
}

func NewService(db *gorm.DB, settings *configuration.Service, logger *slog.Logger) *Service {
	return &Service{db: db, settings: settings, logger: logger}
}

func (s *Service) Cleanup(ctx context.Context) (Report, error) {
	settings, err := s.settings.GetLogRetentionSettings(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("读取日志保留策略失败: %w", err)
	}
	report := Report{Enabled: settings.Enabled, CompletedAt: time.Now().UTC()}
	if !settings.Enabled {
		return report, nil
	}

	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pipelineResult := tx.Where("created_at < ?", now.AddDate(0, 0, -settings.PipelineLogDays)).Delete(&model.PipelineRunLog{})
		if pipelineResult.Error != nil {
			return fmt.Errorf("清理流水线日志失败: %w", pipelineResult.Error)
		}
		report.PipelineLogsDeleted = pipelineResult.RowsAffected

		auditResult := tx.Where("created_at < ?", now.AddDate(0, 0, -settings.AuditLogDays)).Delete(&model.AuditLog{})
		if auditResult.Error != nil {
			return fmt.Errorf("清理审计日志失败: %w", auditResult.Error)
		}
		report.AuditLogsDeleted = auditResult.RowsAffected
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	report.CompletedAt = time.Now().UTC()
	return report, nil
}

func (s *Service) Run(ctx context.Context) error {
	s.cleanupAndLog(ctx)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.cleanupAndLog(ctx)
		}
	}
}

func (s *Service) cleanupAndLog(ctx context.Context) {
	report, err := s.Cleanup(ctx)
	if err != nil {
		s.logger.Error("执行日志保留清理失败", "operation", "log_retention_cleanup", "err", err)
		return
	}
	if !report.Enabled {
		return
	}
	s.logger.Info("日志保留清理完成", "operation", "log_retention_cleanup", "pipeline_logs_deleted", report.PipelineLogsDeleted, "audit_logs_deleted", report.AuditLogsDeleted)
}
