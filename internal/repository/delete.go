package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/model"
)

// LockReference 在保存仓库关联的同一事务内调用，与删除互斥。
func LockReference(tx *gorm.DB, id string) error {
	// 先写后读，兼容 SQLite 的写锁和 MySQL 的一致性读。
	if err := tx.Model(&model.GitRepository{}).Where("uuid = ?", id).
		UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
		return fmt.Errorf("锁定代码仓库引用失败: %w", err)
	}
	var repository model.GitRepository
	err := tx.Select("uuid").Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "uuid = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrRepositoryNotFound
	}
	if err != nil {
		return fmt.Errorf("核对代码仓库引用失败: %w", err)
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := LockReference(tx, id); err != nil {
			return err
		}
		// 仓库 UUID 全局唯一。保留所有未归档应用的绑定，停用不算解除绑定。
		var bound bool
		if err := tx.Raw(`SELECT EXISTS (
			SELECT 1 FROM applications WHERE repository_id = ? AND deleted_at IS NULL
			UNION ALL
			SELECT 1 FROM application_repositories ar JOIN applications a ON a.uuid = ar.application_id
			WHERE ar.repository_id = ? AND a.deleted_at IS NULL
		)`, id, id).Scan(&bound).Error; err != nil {
			return fmt.Errorf("检查代码仓库应用关联失败: %w", err)
		}
		if bound {
			return ErrRepositoryInUse
		}
		// 应用可能已经换仓库，未结束运行仍使用创建时的仓库快照。
		var running bool
		if err := tx.Raw(`SELECT EXISTS (
			SELECT 1 FROM pipeline_run_repositories pr JOIN pipeline_runs ON pipeline_runs.uuid = pr.pipeline_run_id
			WHERE pr.repository_id = ? AND (pipeline_runs.status IN ? OR
				(pipeline_runs.status_writeback = ? AND pipeline_runs.repository_check_state <> `+ExpectedCheckStateSQL+`))
			UNION ALL
			SELECT 1 FROM repository_check_deliveries WHERE repository_id = ? AND status = ?
		)`, id, []model.PipelineRunStatus{
			model.PipelineRunDetected, model.PipelineRunReady, model.PipelineRunBlocked,
			model.PipelineRunAwaitingApproval, model.PipelineRunRunning,
		}, true, id, "pending").Scan(&running).Error; err != nil {
			return fmt.Errorf("检查代码仓库运行引用失败: %w", err)
		}
		if running {
			return ErrRepositoryRunning
		}
		// 保留历史关联所需的仓库元数据；已删除的连接无需保留凭据。
		if err := tx.Model(&model.GitRepository{}).Where("uuid = ?", id).UpdateColumns(map[string]any{
			"credential_id": nil, "api_credential_id": nil, "credential_ciphertext": "",
		}).Error; err != nil {
			return fmt.Errorf("清除已删除仓库连接凭据失败: %w", err)
		}
		if err := tx.Delete(&model.GitRepository{}, "uuid = ?", id).Error; err != nil {
			return fmt.Errorf("删除代码仓库失败: %w", err)
		}
		return nil
	})
}

// ExpectedCheckStateSQL 与回写队列共用，表示当前流水线运行应回写的状态。
const ExpectedCheckStateSQL = "CASE WHEN pipeline_runs.status = 'succeeded' THEN 'success' WHEN pipeline_runs.status = 'failed' THEN 'failure' WHEN pipeline_runs.status IN ('canceled','skipped') THEN 'error' ELSE 'pending' END"
