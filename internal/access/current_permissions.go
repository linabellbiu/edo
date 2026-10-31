package access

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

// CurrentUserPermissions 保留 Casbin 授权，并以当前事务表收紧身份响应中的权限。
// 跨实例策略尚未重载时不能重新展示已撤销的权限，也不能仅凭数据库记录扩大授权。
func (s *Service) CurrentUserPermissions(ctx context.Context, user *model.User) (permissions []string, err error) {
	permissions = []string{}
	if user == nil {
		return permissions, nil
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	defer func() {
		if err != nil {
			logger := s.logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.WarnContext(ctx, "复核当前身份权限失败", "operation", "current_user_permissions", "user_id", user.ID, "project_id", scope.ProjectID, "err", err)
		}
	}()
	if scope.UserID != "" && scope.UserID != user.ID {
		return nil, ErrAccessDelegationDenied
	}
	// 嵌套事务的 SAVEPOINT 无法提高外层隔离级别；此入口必须从根数据库建立快照。
	if _, nested := s.db.Statement.ConnPool.(gorm.TxCommitter); nested {
		return nil, errors.New("当前身份权限复核不能复用已有事务")
	}
	candidates, err := s.UserPermissions(ctx, user)
	if err != nil {
		return nil, err
	}
	// 明确采用一致只读快照，防止角色和个人拒绝分别读到不同提交后组合出旧授权。
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.User
		if err := tx.First(&current, "uuid = ?", user.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccessDelegationDenied
			}
			return err
		}
		if !current.IsActive || current.AuthVersion != user.AuthVersion || current.DepartmentID != user.DepartmentID || current.IsSuperuser != user.IsSuperuser {
			return ErrAccessDelegationDenied
		}
		var projectPermissions map[string]bool
		if scope.ProjectID != "" {
			// 沿用长连接及项目委派的实时成员核验，个人允许不能绕过项目成员资格。
			fresh, err := FreshProjectPermissions(tx, ctx, user.ID, scope.ProjectID)
			if err != nil {
				return err
			}
			if fresh.User.AuthVersion != current.AuthVersion || fresh.User.DepartmentID != current.DepartmentID || fresh.User.IsSuperuser != current.IsSuperuser {
				return ErrAccessDelegationDenied
			}
			projectPermissions = fresh.Permissions
		}
		if current.IsSuperuser {
			permissions = []string{"*"}
			return nil
		}
		// 平台角色不能自动向项目授予权限，项目部分只使用上面已复核的成员权限。
		platformScope := scope
		platformScope.ProjectID = ""
		platformContext := database.WithDepartmentScope(ctx, platformScope)
		platformPermissions, err := effectivePermissionSetFromDB(tx.WithContext(platformContext), user.ID)
		if err != nil {
			return err
		}
		for _, permission := range candidates {
			if ProjectPermission(permission) {
				if projectPermissions[permission] {
					permissions = append(permissions, permission)
				}
			} else if _, allowed := platformPermissions[permission]; allowed {
				permissions = append(permissions, permission)
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	return permissions, nil
}
