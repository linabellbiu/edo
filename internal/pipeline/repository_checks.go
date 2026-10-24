package pipeline

import (
	"context"
	"errors"
	"strings"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func checkState(status model.PipelineRunStatus) string {
	switch status {
	case model.PipelineRunSucceeded:
		return "success"
	case model.PipelineRunFailed:
		return "failure"
	case model.PipelineRunCanceled, model.PipelineRunSkipped:
		return "error"
	}
	return "pending"
}
func (s *Service) RunRepositoryChecks(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.ReconcileRepositoryChecks(ctx); err != nil {
			s.logger.Error("同步仓库检查结果失败", "operation", "repository_checks_reconcile", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) ReconcileRepositoryChecks(ctx context.Context) error {
	var runs []model.PipelineRun
	expected := "CASE WHEN status = 'succeeded' THEN 'success' WHEN status = 'failed' THEN 'failure' WHEN status IN ('canceled','skipped') THEN 'error' ELSE 'pending' END"
	if err := s.db.WithContext(ctx).Where("status_writeback = ? AND repository_check_state <> "+expected, true).Order("id ASC").Limit(100).Find(&runs).Error; err != nil {
		return err
	}
	for _, run := range runs {
		scope := database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: run.ProjectID, DepartmentID: run.DepartmentID})
		state := checkState(run.Status)
		err := s.db.WithContext(scope).Transaction(func(tx *gorm.DB) error {
			var link model.PipelineRunRepository
			if err := tx.First(&link, "pipeline_run_id = ?", run.ID).Error; err != nil {
				return err
			}
			item := model.RepositoryCheckDelivery{ID: uuid.NewString(), ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, PipelineRunID: run.ID, RepositoryID: link.RepositoryID, CommitSHA: run.CommitSHA, Context: "edo/" + run.WorkflowID, State: state, DeliveryKey: strings.TrimPrefix(sha256Digest([]byte(run.ID+":"+state)), "sha256:"), Status: "pending", NextAttemptAt: time.Now().UTC()}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error; err != nil {
				return err
			}
			return tx.Model(&model.PipelineRun{}).Where("uuid = ? AND status = ?", run.ID, run.Status).Update("repository_check_state", state).Error
		})
		if err != nil {
			return err
		}
	}
	var items []model.RepositoryCheckDelivery
	if err := s.db.WithContext(ctx).Where("status = ? AND next_attempt_at <= ?", "pending", time.Now().UTC()).Order("id ASC").Limit(50).Find(&items).Error; err != nil {
		return err
	}
	for _, item := range items {
		scope := database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: item.ProjectID, DepartmentID: item.DepartmentID})
		var run model.PipelineRun
		if err := s.db.WithContext(scope).First(&run, "uuid = ?", item.PipelineRunID).Error; err != nil {
			return err
		}
		var newer int64
		if err := s.db.WithContext(scope).Model(&model.PipelineRun{}).Where("workflow_id = ? AND commit_sha = ? AND id > ? AND status_writeback = ?", run.WorkflowID, run.CommitSHA, run.RowID, true).Count(&newer).Error; err != nil {
			return err
		}
		status, message := "delivered", "检查结果已回写"
		var err error
		if checkState(run.Status) != item.State || newer > 0 {
			status, message = "obsolete", "已有更新的检查状态"
		} else if s.repositories == nil {
			err = ErrPipelineExecutionUnavailable
		} else {
			err = s.repositories.WriteCheckStatus(scope, item.RepositoryID, item.CommitSHA, item.Context, item.State)
		}
		attempts := item.Attempts + 1
		if err != nil {
			status, message = "pending", "检查结果回写失败，稍后重试"
			var response *repository.CheckStatusError
			if attempts >= 4 || errors.Is(err, repository.ErrInvalidRepository) || errors.Is(err, repository.ErrRepositoryNotFound) || errors.Is(err, repository.ErrCheckCredentials) || (errors.As(err, &response) && response.Permanent()) {
				status, message = "failed", "检查结果回写失败，请检查仓库 API 令牌和权限"
			}
			s.logger.ErrorContext(ctx, "回写仓库检查结果失败", "operation", "repository_check_deliver", "run_id", run.ID, "attempt", attempts, "err", err)
		}
		delay := time.Duration(1<<attempts) * 15 * time.Second
		if err := s.db.WithContext(scope).Model(&model.RepositoryCheckDelivery{}).Where("uuid = ? AND status = ? AND attempts = ?", item.ID, "pending", item.Attempts).Updates(map[string]any{"status": status, "message": message, "attempts": attempts, "next_attempt_at": time.Now().UTC().Add(delay)}).Error; err != nil {
			return err
		}
		if err := s.db.WithContext(scope).Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("updated_at", time.Now().UTC()).Error; err != nil {
			return err
		}
		s.realtime.Changed(scope, "pipeline_runs", run.ID)
	}
	return nil
}
