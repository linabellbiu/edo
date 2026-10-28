package scheduler

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

func (s *Service) executionScheduleCurrent(ctx context.Context, payload TaskPayload, action NotificationAction) (bool, error) {
	scope, scoped := database.DepartmentScopeFromContext(ctx)
	if !scoped || scope.ProjectID == "" || scope.DepartmentID == "" {
		s.logger.WarnContext(ctx, "定时任务执行范围无效", "operation", "scheduler_execute_scope", "schedule_id", payload.ScheduleID, "project_id", scope.ProjectID, "department_id", scope.DepartmentID, "err", database.ErrProjectScope)
		return false, ErrInvalidTaskPayload
	}
	var current model.ScheduledTask
	err := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND department_id = ?", payload.ScheduleID, scope.ProjectID, scope.DepartmentID).First(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.skipExecution(ctx, payload, "schedule_missing")
	}
	if err != nil {
		return s.executionLookupFailure(ctx, payload, "scheduler_execute_schedule", err)
	}
	if !current.IsActive {
		return s.skipExecution(ctx, payload, "schedule_disabled")
	}
	var configuredAction NotificationAction
	if err := decodeStrict(current.Payload, &configuredAction); err != nil {
		return s.executionLookupFailure(ctx, payload, "scheduler_execute_configuration", err)
	}
	// JSON 存储可能重排键或规范化空白，按动作字段比较，不能误丢弃同一配置。
	if !current.UpdatedAt.Equal(payload.ConfiguredAt) || current.Action != payload.Action || configuredAction != action {
		return s.skipExecution(ctx, payload, "configuration_changed")
	}
	var project model.Project
	err = s.db.WithContext(ctx).Where("uuid = ? AND department_id = ? AND is_active = ?", scope.ProjectID, scope.DepartmentID, true).First(&project).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.skipExecution(ctx, payload, "project_inactive")
	}
	if err != nil {
		return s.executionLookupFailure(ctx, payload, "scheduler_execute_project", err)
	}
	var department model.Department
	err = s.db.WithContext(ctx).Where("uuid = ? AND is_active = ?", scope.DepartmentID, true).First(&department).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.skipExecution(ctx, payload, "department_inactive")
	}
	if err != nil {
		return s.executionLookupFailure(ctx, payload, "scheduler_execute_department", err)
	}
	return true, nil
}

func (s *Service) skipExecution(ctx context.Context, payload TaskPayload, reason string) (bool, error) {
	scope, _ := database.DepartmentScopeFromContext(ctx)
	s.logger.InfoContext(ctx, "定时任务已失效，跳过排队执行", "operation", "scheduler_execute_skip", "schedule_id", payload.ScheduleID, "project_id", scope.ProjectID, "department_id", scope.DepartmentID, "reason", reason)
	return false, nil
}

func (s *Service) executionLookupFailure(ctx context.Context, payload TaskPayload, operation string, cause error) (bool, error) {
	scope, _ := database.DepartmentScopeFromContext(ctx)
	s.logger.ErrorContext(ctx, "核验定时任务当前范围失败", "operation", operation, "schedule_id", payload.ScheduleID, "project_id", scope.ProjectID, "department_id", scope.DepartmentID, "err", cause)
	return false, ErrExecutionUnavailable
}
