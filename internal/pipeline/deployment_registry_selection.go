package pipeline

import (
	"context"
	"errors"

	"edo/internal/database"
	"edo/internal/model"
	"gorm.io/gorm"
)

var (
	ErrDeploymentRegistryRequired    = errors.New("Docker 和 docker compose 部署必须选择并保存镜像仓库")
	ErrDeploymentRegistryUnavailable = errors.New("已保存的部署镜像仓库不可用，请重新选择并保存")
)

// 新运行只接受方案中已保存的选择，不从上游构建或制品补全仓库。
func (s *Service) freezeSavedDeploymentRegistry(ctx context.Context, db *gorm.DB, node *model.WorkflowNode, kind model.DeploymentPlanKind, actorID string) error {
	if !model.RequiresDeploymentRegistry(kind) {
		return nil
	}
	id := model.SavedDeploymentRegistryID(node.Config)
	if id == "" {
		s.logger.WarnContext(ctx, "部署未保存镜像仓库", "operation", "deployment_registry_freeze", "node_id", node.ID, "err", ErrDeploymentRegistryRequired)
		return ErrDeploymentRegistryRequired
	}
	var registry model.ImageRegistry
	query := db.WithContext(ctx).Where("uuid = ? AND is_active = ?", id, true)
	if scope, ok := database.DepartmentScopeFromContext(ctx); ok && scope.ProjectID != "" {
		query = query.Where("project_id = ?", scope.ProjectID)
	}
	if err := query.First(&registry).Error; err != nil {
		s.logger.WarnContext(ctx, "已保存的部署镜像仓库不可用", "operation", "deployment_registry_freeze", "node_id", node.ID, "registry_id", id, "err", err)
		return ErrDeploymentRegistryUnavailable
	}
	if err := s.requireDeliveryPermissionWithDatabase(ctx, db, actorID, "image_registry.execute"); err != nil {
		return err
	}
	node.Config.DeploymentRegistryID = id
	return nil
}
