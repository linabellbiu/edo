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
	ErrRoleCreatedSyncPending      = errors.New("角色已创建，但权限策略同步尚未完成；当前角色没有关联账户，请勿重复创建")
	ErrRoleDeletedSyncPending      = errors.New("角色已删除，但权限策略同步尚未完成；删除已经生效，请勿重复提交")
	ErrRolePermissionsSyncDisabled = errors.New("角色权限已保存，但权限策略同步失败；为防止旧权限继续生效，关联账户已停用，请勿重复提交")
	ErrRolePermissionsSyncUnsafe   = errors.New("角色权限已保存，但权限策略同步和关联账户安全停用均失败")
)

// syncRolePermissionsOrDisable 处理角色权限提交后的策略同步失败。
// 角色是跨部门共享资源，因此安全停用必须绕过请求部门过滤，覆盖该角色的全部非超级管理员成员。
func (s *Service) syncRolePermissionsOrDisable(ctx context.Context, roleID, operation string) error {
	if err := s.syncPolicy(operation); err == nil {
		return nil
	} else {
		safeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		safeContext = database.WithDepartmentScope(safeContext, database.DepartmentScope{AllDepartments: true})
		safetyErr := s.db.WithContext(safeContext).Transaction(func(tx *gorm.DB) error {
			countMemberIDs := tx.Model(&model.UserRole{}).Select("user_id").Where("role_id = ?", roleID)
			var expected int64
			if countErr := tx.Model(&model.User{}).
				Where("is_superuser = ?", false).
				Where("id IN (?)", countMemberIDs).
				Count(&expected).Error; countErr != nil {
				return fmt.Errorf("统计待安全停用的角色成员失败: %w", countErr)
			}
			updateMemberIDs := tx.Model(&model.UserRole{}).Select("user_id").Where("role_id = ?", roleID)
			result := tx.Model(&model.User{}).
				Where("is_superuser = ?", false).
				Where("id IN (?)", updateMemberIDs).
				Updates(map[string]any{
					"is_active": false, "auth_version": gorm.Expr("auth_version + ?", 1), "updated_at": time.Now().UTC(),
				})
			if result.Error != nil {
				return fmt.Errorf("安全停用角色成员失败: %w", result.Error)
			}
			if result.RowsAffected != expected {
				return fmt.Errorf("安全停用角色成员数量不一致: expected=%d actual=%d", expected, result.RowsAffected)
			}
			return nil
		})
		if safetyErr != nil {
			s.logger.Error("角色权限策略同步失败且无法安全停用关联账户", "operation", operation, "role_id", roleID, "sync_err", err, "disable_err", safetyErr)
			return fmt.Errorf("%w: %v", ErrRolePermissionsSyncUnsafe, err)
		}
		s.logger.Error("角色权限策略同步失败，关联账户已安全停用", "operation", operation, "role_id", roleID, "err", err)
		return ErrRolePermissionsSyncDisabled
	}
}

func permissionCodeSet(permissions []string) map[string]struct{} {
	result := make(map[string]struct{}, len(permissions))
	for _, permission := range permissions {
		result[permission] = struct{}{}
	}
	return result
}
