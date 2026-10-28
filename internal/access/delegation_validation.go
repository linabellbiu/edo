package access

import (
	"errors"

	"gorm.io/gorm"

	"edo/internal/model"
)

// ValidateRoleDelegation 供其他业务在同一事务中复用授权委派校验，不接受缺失或已停用的操作者。
func ValidateRoleDelegation(tx *gorm.DB, actor *model.User, requiredPermission string, roleIDs []string) error {
	if actor == nil {
		return ErrAccessDelegationDenied
	}
	var stored model.User
	if err := tx.First(&stored, "uuid = ? AND is_active = ?", actor.ID, true).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrAccessDelegationDenied
		}
		return err
	}
	permissions, err := validatedRolePermissionSet(tx, roleIDs)
	if err != nil {
		return err
	}
	return validateAccessDelegation(tx, &stored, requiredPermission, "", permissions, nil)
}
