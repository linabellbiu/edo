package access

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

// currentPlatformPermission 只收紧已经被 Casbin 允许的平台权限，不生成新的授权。
// 角色、个人允许/拒绝及当前身份在同一条参数化查询中判定，避免读取不同提交的组合。
func (s *Service) currentPlatformPermission(ctx context.Context, user *model.User, permission string) (bool, error) {
	scope, _ := database.DepartmentScopeFromContext(ctx)
	if scope.UserID != "" && scope.UserID != user.ID || scope.DepartmentID != "" && scope.DepartmentID != user.DepartmentID {
		return false, nil
	}
	query := func() *gorm.DB { return s.db.Session(&gorm.Session{NewDB: true}).WithContext(ctx) }
	deny := query().Model(&model.UserPermission{}).Select("1").
		Where("user_permissions.user_id = users.uuid AND user_permissions.permission = ? AND user_permissions.effect = ?", permission, model.PermissionDeny)
	allow := query().Model(&model.UserPermission{}).Select("1").
		Where("user_permissions.user_id = users.uuid AND user_permissions.permission = ? AND user_permissions.effect = ?", permission, model.PermissionAllow)
	role := query().Model(&model.UserRole{}).Select("1").
		Joins("JOIN role_permissions ON role_permissions.role_id = user_roles.role_id").
		Where("user_roles.user_id = users.uuid AND role_permissions.permission = ?", permission)
	var count int64
	err := query().Model(&model.User{}).
		Where("users.uuid = ? AND users.is_active = ? AND users.auth_version = ? AND users.department_id = ? AND users.is_superuser = ?", user.ID, true, user.AuthVersion, user.DepartmentID, false).
		Where("NOT EXISTS (?)", deny).
		Where("(EXISTS (?) OR EXISTS (?))", allow, role).
		Count(&count).Error
	if err != nil {
		s.logger.ErrorContext(ctx, "复核平台权限失败", "operation", "platform_permission_recheck", "user_id", user.ID, "permission", permission, "err", err)
		return false, errors.New("权限校验暂时不可用，请稍后重试")
	}
	return count == 1, nil
}
