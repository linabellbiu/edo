package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

type FreshProjectAuthorization struct {
	User        model.User
	RoleID      string
	Permissions map[string]bool
	Digest      string
}

// HasCurrentProjectPermission 在已通过策略校验后复核当前项目成员、角色与个人拒绝。
func (s *Service) HasCurrentProjectPermission(ctx context.Context, user *model.User, permission string) (bool, error) {
	if user == nil || !ProjectPermission(permission) {
		return false, nil
	}
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.ProjectID == "" {
		return false, nil
	}
	authorization, err := FreshProjectPermissions(s.db, ctx, user.ID, scope.ProjectID)
	if errors.Is(err, ErrAccessDelegationDenied) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return authorization.Permissions[permission], nil
}

// FreshProjectPermissions reuses the same current-DB permission calculation as
// project delegation. It does not depend on an asynchronously reloaded enforcer.
func FreshProjectPermissions(db *gorm.DB, ctx context.Context, userID, projectID string) (*FreshProjectAuthorization, error) {
	return freshProjectPermissions(db, ctx, userID, projectID, false)
}

// 只有项目自身的管理操作允许复核停用项目，业务操作仍必须拒绝停用项目。
func freshProjectPermissions(db *gorm.DB, ctx context.Context, userID, projectID string, allowInactive bool) (*FreshProjectAuthorization, error) {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.ProjectID == "" || scope.ProjectID != projectID || scope.DepartmentID == "" || userID == "" {
		return nil, ErrAccessDelegationDenied
	}
	result := &FreshProjectAuthorization{Permissions: map[string]bool{}}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 只跨部门读取操作者身份；项目和授权查询继续使用冻结范围，普通成员随后核对部门。
		identityContext := database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: userID, AllDepartments: true})
		if err := tx.WithContext(identityContext).First(&result.User, "uuid = ?", userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccessDelegationDenied
			}
			return err
		}
		if !result.User.IsActive {
			return ErrAccessDelegationDenied
		}
		var project model.Project
		if err := tx.First(&project, "uuid = ?", projectID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccessDelegationDenied
			}
			return err
		}
		if (!project.IsActive && !allowInactive) || project.DepartmentID != scope.DepartmentID {
			return ErrAccessDelegationDenied
		}
		var member model.ProjectMember
		err := tx.Where("project_id = ? AND user_id = ?", projectID, userID).First(&member).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if !result.User.IsSuperuser && (err != nil || result.User.DepartmentID != project.DepartmentID) {
			return ErrAccessDelegationDenied
		}
		if err == nil {
			result.RoleID = member.RoleID
		}
		if result.User.IsSuperuser {
			for _, permission := range Catalog() {
				if ProjectPermission(permission.Code) {
					result.Permissions[permission.Code] = true
				}
			}
		} else {
			var err error
			result.Permissions, err = projectPermissionSetFromDB(tx, userID, projectID)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	codes := make([]string, 0, len(result.Permissions))
	for code, allowed := range result.Permissions {
		if allowed {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	encoded, _ := json.Marshal([]any{projectID, result.User.ID, result.User.AuthVersion, result.User.IsSuperuser, result.RoleID, codes})
	digest := sha256.Sum256(encoded)
	result.Digest = hex.EncodeToString(digest[:])
	return result, nil
}
