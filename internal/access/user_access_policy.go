package access

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"edo/internal/database"
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
// 分配角色时逐项检查权限，不能用用户拒绝项抵消越权授权。
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
		for _, code := range []string{permission} {
			result[code] = struct{}{}
		}
	}
	return result, nil
}

// validateAccessDelegation 从数据库读取当前权限，防止 Casbin 缓存尚未同步时越权授权。
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
		for _, code := range []string{permission} {
			if _, ok := effective[code]; !ok {
				return ErrAccessDelegationDenied
			}
		}
	}
	for _, permission := range directAllow {
		for _, code := range []string{permission} {
			if _, ok := effective[code]; !ok {
				return ErrAccessDelegationDenied
			}
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
		if !ProjectPermission(permission) {
			result[permission] = struct{}{}
		}
	}
	scope, _ := database.DepartmentScopeFromContext(tx.Statement.Context)
	if scope.ProjectID != "" {
		projectSet, err := projectPermissionSetFromDB(tx, userID, scope.ProjectID)
		if err != nil {
			return nil, err
		}
		for code := range projectSet {
			result[code] = struct{}{}
		}
	}
	var overrides []model.UserPermission
	if err := tx.Where("user_id = ?", userID).Find(&overrides).Error; err != nil {
		return nil, fmt.Errorf("查询操作者权限例外失败: %w", err)
	}
	for _, item := range overrides {
		if item.Effect == model.PermissionAllow && (!ProjectPermission(item.Permission) || scope.ProjectID != "") {
			for _, code := range []string{item.Permission} {
				result[code] = struct{}{}
			}
		}
	}
	for _, item := range overrides {
		if item.Effect == model.PermissionDeny {
			for _, code := range []string{item.Permission} {
				delete(result, code)
			}
		}
	}
	return result, nil
}

// syncUserAccessOrDisable 处理数据库提交后的策略同步失败。
// 同步失败可能遗留已撤销的权限，因此停用账户并递增认证版本，使各实例拒绝旧会话。
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
