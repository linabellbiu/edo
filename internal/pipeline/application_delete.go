package pipeline

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"edo/internal/model"
)

var (
	ErrApplicationRunning = errors.New("应用仍有未结束的流水线，请先结束或停止运行")
	ErrApplicationInUse   = errors.New("应用仍被发布计划引用，请先从相关发布计划中移除")
	errDeleteApplication  = errors.New("删除应用失败，请稍后重试")
)

func (s *Service) DeleteApplication(ctx context.Context, id string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockWorkflowApplication(tx, id); err != nil {
			return err
		}
		var running int64
		if err := tx.Model(&model.PipelineRun{}).Where("application_id = ? AND status IN ?", id, []model.PipelineRunStatus{
			model.PipelineRunDetected, model.PipelineRunReady, model.PipelineRunBlocked,
			model.PipelineRunAwaitingApproval, model.PipelineRunRunning,
		}).Count(&running).Error; err != nil {
			return fmt.Errorf("检查应用流水线运行状态: %w", err)
		}
		if running > 0 {
			return ErrApplicationRunning
		}
		var references int64
		if err := tx.Model(&model.ReleasePlan{}).Where(`EXISTS (
			SELECT 1 FROM release_groups
			JOIN release_group_applications ON release_group_applications.release_group_id = release_groups.uuid
			WHERE release_groups.release_plan_id = release_plans.uuid AND release_group_applications.application_id = ?
		)`, id).Count(&references).Error; err != nil {
			return fmt.Errorf("检查应用发布计划引用: %w", err)
		}
		if references > 0 {
			return ErrApplicationInUse
		}
		// 软删除应用与所属流水线，保留运行快照、产物和部署记录。
		if err := tx.Where("application_id = ?", id).Delete(&model.ReleaseWorkflow{}).Error; err != nil {
			return fmt.Errorf("删除应用所属流水线: %w", err)
		}
		return tx.Delete(&model.Application{}, "uuid = ?", id).Error
	})
	switch {
	case err == nil:
		s.realtime.Changed(ctx, "applications", id)
		return nil
	case errors.Is(err, ErrApplicationNotFound), errors.Is(err, ErrApplicationRunning), errors.Is(err, ErrApplicationInUse):
		s.logger.WarnContext(ctx, "拒绝删除应用", "operation", "application_delete", "application_id", id, "err", err)
		return err
	default:
		s.logger.ErrorContext(ctx, "删除应用失败", "operation", "application_delete", "application_id", id, "err", err)
		return errDeleteApplication
	}
}
