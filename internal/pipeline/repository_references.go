package pipeline

import (
	"errors"
	"log/slog"
	"slices"

	"gorm.io/gorm"

	"edo/internal/model"
	"edo/internal/repository"
)

var ErrPipelineRepositoryUnavailable = errors.New("代码仓库已不可用，请重新选择代码版本")

// 锁住本次实际保存的仓库，避免预览期间更换应用仓库后又冻结已删除的仓库。
func lockPipelineRunRepositories(tx *gorm.DB, groups ...[]model.PipelineRunRepository) error {
	ids := make([]string, 0, len(groups))
	for _, components := range groups {
		for _, component := range components {
			ids = append(ids, component.RepositoryID)
		}
	}
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		if err := repository.LockReference(tx, id); err != nil {
			if errors.Is(err, repository.ErrRepositoryNotFound) {
				slog.WarnContext(tx.Statement.Context, "冻结流水线仓库引用失败", "operation", "pipeline_repository_reference_lock", "repository_id", id, "err", err)
				return ErrPipelineRepositoryUnavailable
			}
			return err
		}
	}
	return nil
}

func createPipelineRunRepositories(tx *gorm.DB, components []model.PipelineRunRepository) error {
	if err := lockPipelineRunRepositories(tx, components); err != nil {
		return err
	}
	return tx.Create(&components).Error
}
