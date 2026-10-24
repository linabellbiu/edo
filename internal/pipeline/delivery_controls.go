package pipeline

import (
	"context"
	"edo/internal/access"
	"gorm.io/gorm"

	"edo/internal/cicd"
	"edo/internal/model"
)

func (s *Service) Delivery() *cicd.Service { return cicd.New(s.db, s.secrets, s.logger) }

func (s *Service) ListNodeRuns(ctx context.Context, runID string) ([]model.PipelineNodeRun, error) {
	if _, err := s.FindRun(ctx, runID); err != nil {
		return nil, err
	}
	items := []model.PipelineNodeRun{}
	err := s.db.WithContext(ctx).Where("pipeline_run_id = ?", runID).Order("id ASC").Find(&items).Error
	return items, err
}

func (s *Service) ListCheckReports(ctx context.Context, runID string) ([]model.PipelineCheckReport, error) {
	if _, err := s.FindRun(ctx, runID); err != nil {
		return nil, err
	}
	items := []model.PipelineCheckReport{}
	err := s.db.WithContext(ctx).Where("pipeline_run_id = ?", runID).Order("id ASC").Find(&items).Error
	return items, err
}

func (s *Service) ConfigureDeliveryAccess(permissions *access.Service) {
	s.deliveryAccess = permissions
}
func (s *Service) requireDeliveryPermission(ctx context.Context, actorID, permission string) error {
	return s.requireDeliveryPermissionWithDatabase(ctx, s.db, actorID, permission)
}
func (s *Service) requireDeliveryPermissionWithDatabase(ctx context.Context, db *gorm.DB, actorID, permission string) error {
	denied := cicd.ErrExecutionAuthorization
	if s.deliveryAccess == nil {
		s.logger.Error("交付权限服务不可用", "operation", "delivery_permission", "permission", permission)
		return denied
	}
	var actor model.User
	if err := db.WithContext(ctx).First(&actor, "uuid = ? AND is_active = ?", actorID, true).Error; err != nil {
		s.logger.Error("执行授权用户不可用", "operation", "delivery_permission", "actor_id", actorID, "err", err)
		return denied
	}
	allowed, err := s.deliveryAccess.HasPermission(ctx, &actor, permission)
	if err != nil || !allowed {
		s.logger.Error("交付执行权限不足", "operation", "delivery_permission", "actor_id", actorID, "permission", permission, "err", err)
		return denied
	}
	return nil
}
func (s *Service) DatabaseMigrations(ctx context.Context, id string) ([]model.ApplicationDatabaseMigration, error) {
	var target model.ApplicationDatabase
	if err := s.db.WithContext(ctx).First(&target, "uuid = ?", id).Error; err != nil {
		s.logger.Error("读取应用数据库失败", "operation", "database_migration_list_target", "database_id", id, "err", err)
		return nil, cicd.ErrInvalidDatabase
	}
	items := []model.ApplicationDatabaseMigration{}
	if err := s.db.WithContext(ctx).Where("database_id = ?", id).Order("id DESC").Limit(100).Find(&items).Error; err != nil {
		s.logger.Error("读取数据库变更记录失败", "operation", "database_migration_list", "database_id", id, "err", err)
		return nil, cicd.ErrStorage
	}
	return items, nil
}
func (s *Service) CheckDeliveries(ctx context.Context, runID string) ([]model.RepositoryCheckDelivery, error) {
	if _, err := s.FindRun(ctx, runID); err != nil {
		return nil, err
	}
	items := []model.RepositoryCheckDelivery{}
	err := s.db.WithContext(ctx).Where("pipeline_run_id = ?", runID).Order("id ASC").Find(&items).Error
	return items, err
}
