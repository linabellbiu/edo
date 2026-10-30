package access

import (
	"context"
	"errors"
	"fmt"

	"edo/internal/model"
	"gorm.io/gorm"
)

var ErrProjectLastOwner = errors.New("请先指定另一名项目管理员，再移除或更改当前管理员")

func lockProjectMembers(tx *gorm.DB, id string) (*model.Project, error) {
	// 与回滚任务相同，先取得写锁再读取授权。MySQL/PostgreSQL 锁定此项目行，
	// SQLite 提前取得写锁，避免两个读事务同时升级；不改变项目更新时间。
	if err := tx.Model(&model.Project{}).Where("uuid = ?", id).
		UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
		return nil, fmt.Errorf("锁定项目成员配置失败: %w", err)
	}
	var project model.Project
	if err := tx.First(&project, "uuid = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrProjectNotFound
		}
		return nil, fmt.Errorf("读取已锁定项目失败: %w", err)
	}
	return &project, nil
}

func preserveProjectOwner(tx *gorm.DB, project *model.Project, targetID, roleID string) error {
	if roleID == ProjectOwnerRoleID {
		return nil
	}
	var current int64
	if err := tx.Model(&model.ProjectMember{}).
		Where("project_id = ? AND user_id = ? AND role_id = ?", project.ID, targetID, ProjectOwnerRoleID).
		Count(&current).Error; err != nil {
		return fmt.Errorf("读取原项目管理员失败: %w", err)
	}
	if current == 0 {
		return nil
	}
	var remaining []string
	if err := tx.Model(&model.ProjectMember{}).
		Joins("JOIN users ON users.uuid = project_members.user_id").
		Where("project_members.project_id = ? AND project_members.user_id <> ? AND project_members.role_id = ?", project.ID, targetID, ProjectOwnerRoleID).
		Where("users.is_active = ? AND users.department_id = ?", true, project.DepartmentID).
		Pluck("project_members.user_id", &remaining).Error; err != nil {
		return fmt.Errorf("核对剩余项目管理员失败: %w", err)
	}
	for _, ownerID := range remaining {
		permissions, err := projectPermissionSetFromDB(tx, ownerID, project.ID)
		if err != nil {
			return err
		}
		if permissions["project.member.update"] {
			return nil
		}
	}
	return ErrProjectLastOwner
}

func (s *Service) authorizeProjectManagement(ctx context.Context, user *model.User, project *model.Project, permission string) (err error) {
	defer func() {
		if err != nil {
			s.logger.WarnContext(ctx, "项目管理权限复核失败", "operation", "project_management_authorize", "project_id", project.ID, "user_id", user.ID, "permission", permission, "err", err)
		}
	}()
	scope := projectContext(ctx, user, project)
	current, err := freshProjectPermissions(s.db, scope, user.ID, project.ID, true)
	if err != nil {
		return err
	}
	allowed, err := s.HasPermission(scope, &current.User, permission)
	if err != nil {
		return err
	}
	if !allowed || !current.Permissions[permission] {
		return ErrAccessDelegationDenied
	}
	return nil
}
