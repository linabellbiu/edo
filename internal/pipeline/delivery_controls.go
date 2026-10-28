package pipeline

import (
	"context"
	"edo/internal/access"
	"edo/internal/database"
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

// CheckDeliveryPermission 按当前项目的实时成员、角色及个人拒绝校验交付动作。
func (s *Service) CheckDeliveryPermission(ctx context.Context, actorID, permission string) error {
	return s.requireDeliveryPermission(ctx, actorID, permission)
}

func (s *Service) requireDeliveryPermission(ctx context.Context, actorID, permission string) error {
	return s.requireDeliveryPermissionWithDatabase(ctx, s.db, actorID, permission)
}
func (s *Service) requireDeliveryPermissionWithDatabase(ctx context.Context, db *gorm.DB, actorID, permission string) error {
	denied := cicd.ErrExecutionAuthorization
	scope, _ := database.DepartmentScopeFromContext(ctx)
	if s.deliveryAccess == nil {
		s.logger.Error("交付权限服务不可用", "operation", "delivery_permission", "project_id", scope.ProjectID, "actor_id", actorID, "permission", permission, "err", denied)
		return denied
	}
	authorization, err := access.FreshProjectPermissions(db, ctx, actorID, scope.ProjectID)
	if err != nil || !authorization.Permissions[permission] {
		s.logger.Error("交付执行权限不足", "operation", "delivery_permission", "project_id", scope.ProjectID, "actor_id", actorID, "permission", permission, "err", err)
		return denied
	}
	return nil
}

func (s *Service) requirePipelineExecutionPermission(ctx context.Context, run model.PipelineRun, snapshot workflowSnapshot) error {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || run.ProjectID == "" || run.DepartmentID == "" || scope.ProjectID != run.ProjectID || scope.DepartmentID != run.DepartmentID || snapshot.AuthorityID == "" {
		s.logger.Error("流水线执行授权范围无效", "operation", "pipeline_execution_authorization", "pipeline_run_id", run.ID,
			"project_id", run.ProjectID, "department_id", run.DepartmentID, "actor_id", snapshot.AuthorityID, "err", cicd.ErrExecutionAuthorization)
		return cicd.ErrExecutionAuthorization
	}
	return s.requireDeliveryPermission(ctx, snapshot.AuthorityID, "pipeline_run.execute")
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
