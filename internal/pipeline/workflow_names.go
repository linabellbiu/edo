package pipeline

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/model"
)

var errWorkflowMutationFailed = errors.New("保存应用流水线失败，请稍后重试")

// lockWorkflowApplication 在名称查询前取得应用写锁，写入不得依赖锁前的名称或版本快照。
func lockWorkflowApplication(tx *gorm.DB, applicationID string) error {
	// 沿用用户/项目成员锁：SQLite先取写锁；MySQL no-op零changed rows不能代表不存在。
	if err := tx.Model(&model.Application{}).Where("uuid = ?", applicationID).
		UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
		return err
	}
	var app model.Application
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("uuid").First(&app, "uuid = ?", applicationID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrApplicationNotFound
	}
	return err
}

// workflowNameAvailable 在相同应用写锁内用当前行读取，包含停用记录但排除软删除记录。
func workflowNameAvailable(tx *gorm.DB, applicationID, workflowID, name string) error {
	var other model.ReleaseWorkflow
	query := tx.Model(&model.ReleaseWorkflow{}).Select("uuid").Where("application_id = ? AND name = ?", applicationID, name)
	if workflowID != "" {
		query = query.Where("uuid <> ?", workflowID)
	}
	err := query.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&other).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return ErrWorkflowExists
}

func (s *Service) workflowMutationError(ctx context.Context, operation, applicationID, workflowID string, err error) error {
	if errors.Is(err, ErrWorkflowExists) || errors.Is(err, ErrWorkflowRevisionConflict) ||
		errors.Is(err, ErrWorkflowNotFound) || errors.Is(err, ErrApplicationNotFound) {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "应用流水线修改被拒绝", "operation", operation, "application_id", applicationID, "workflow_id", workflowID, "err", err)
		}
		return err
	}
	if s.logger != nil {
		s.logger.ErrorContext(ctx, "保存应用流水线失败", "operation", operation, "application_id", applicationID, "workflow_id", workflowID, "err", err)
	}
	return errWorkflowMutationFailed
}
