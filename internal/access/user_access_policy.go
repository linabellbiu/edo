package access

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"edo/internal/model"
)

var (
	ErrSelfAccessUpdate       = errors.New("不能修改自己的角色或权限")
	ErrAccessDelegationDenied = errors.New("不能授予超出当前账户有效权限的角色或权限")
	ErrUserAccessSyncDisabled = errors.New("访问配置已保存，但权限策略同步失败；为防止旧权限继续生效，目标账户已停用")
	ErrUserAccessSyncUnsafe   = errors.New("访问配置已保存，但权限策略同步和账户安全停用均失败")
	ErrUserCreatedSyncPending = errors.New("用户已创建，但权限策略同步尚未完成")
)

// validatedRolePermissionSet 校验角色存在性并返回角色携带的全部权限。
// 委派边界必须检查角色全集，不能依赖目标用户的 deny 抵消越权角色。
func validatedRolePermissionSet(tx *gorm.DB, roleIDs []string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	if len(roleIDs) == 0 {
		return result, nil
	}
	var roleCount int64
	if err := tx.Model(&model.Role{}).Where("id IN ?", roleIDs).Count(&roleCount).Error; err != nil {
		return nil, fmt.Errorf("查询待分配角色失败: %w", err)
	}
	if roleCount != int64(len(roleIDs)) {
		return nil, ErrInvalidUserRoles
	}
	var permissions []string
	if err := tx.Model(&model.RolePermission{}).Where("role_id IN ?", roleIDs).
		Pluck("permission", &permissions).Error; err != nil {
		return nil, fmt.Errorf("查询待分配角色权限失败: %w", err)
	}
	for _, permission := range permissions {
		result[permission] = struct{}{}
	}
	return result, nil
}

// validateAccessDelegation 使用数据库中的当前权限计算委派边界，避免依赖可能尚未同步的 Casbin 内存快照。
func validateAccessDelegation(
	tx *gorm.DB,
	actor *model.User,
	requiredPermission string,
	targetUserID string,
	rolePermissions map[string]struct{},
	directAllow []string,
) error {
	if actor == nil {
		// nil 仅供包内受信任的初始化和测试路径使用；HTTP 接口必须始终传入当前操作者。
		return nil
	}
	var storedActor model.User
	if err := tx.First(&storedActor, "id = ?", actor.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrAccessDelegationDenied
		}
		return fmt.Errorf("查询权限配置操作者失败: %w", err)
	}
	if storedActor.IsSuperuser {
		return nil
	}
	if targetUserID != "" && storedActor.ID == targetUserID {
		return ErrSelfAccessUpdate
	}
	effective, err := effectivePermissionSetFromDB(tx, storedActor.ID)
	if err != nil {
		return err
	}
	if _, ok := effective[requiredPermission]; !ok {
		return ErrAccessDelegationDenied
	}
	for permission := range rolePermissions {
		if _, ok := effective[permission]; !ok {
			return ErrAccessDelegationDenied
		}
	}
	for _, permission := range directAllow {
		if _, ok := effective[permission]; !ok {
			return ErrAccessDelegationDenied
		}
	}
	return nil
}

func effectivePermissionSetFromDB(tx *gorm.DB, userID string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	var rolePermissions []string
	if err := tx.Model(&model.RolePermission{}).
		Select("role_permissions.permission").
		Joins("JOIN user_roles ON user_roles.role_id = role_permissions.role_id").
		Where("user_roles.user_id = ?", userID).
		Pluck("role_permissions.permission", &rolePermissions).Error; err != nil {
		return nil, fmt.Errorf("查询操作者角色权限失败: %w", err)
	}
	for _, permission := range rolePermissions {
		result[permission] = struct{}{}
	}
	var overrides []model.UserPermission
	if err := tx.Where("user_id = ?", userID).Find(&overrides).Error; err != nil {
		return nil, fmt.Errorf("查询操作者权限例外失败: %w", err)
	}
	for _, item := range overrides {
		if item.Effect == model.PermissionAllow {
			result[item.Permission] = struct{}{}
		}
	}
	for _, item := range overrides {
		if item.Effect == model.PermissionDeny {
			delete(result, item.Permission)
		}
	}
	return result, nil
}

// syncUserAccessOrDisable 处理数据库提交后的策略同步失败。
// 用户访问配置可能包含撤权，因此同步失败时停用目标账户并递增认证版本，
// 让所有实例的身份校验都从数据库 fail closed，而不是向调用方返回可盲目重试的普通失败。
func (s *Service) syncUserAccessOrDisable(ctx context.Context, userID, operation string) error {
	if err := s.syncPolicy(operation); err == nil {
		return nil
	} else {
		safeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		result := s.db.WithContext(safeContext).Model(&model.User{}).Where("id = ?", userID).Updates(map[string]any{
			"is_active": false, "auth_version": gorm.Expr("auth_version + ?", 1), "updated_at": time.Now().UTC(),
		})
		if result.Error != nil || result.RowsAffected != 1 {
			s.logger.Error("权限策略同步失败且无法安全停用目标账户", "operation", operation, "user_id", userID, "sync_err", err, "disable_err", result.Error, "rows_affected", result.RowsAffected)
			return fmt.Errorf("%w: %v", ErrUserAccessSyncUnsafe, err)
		}
		s.logger.Error("权限策略同步失败，目标账户已安全停用", "operation", operation, "user_id", userID, "err", err)
		return ErrUserAccessSyncDisabled
	}
}
