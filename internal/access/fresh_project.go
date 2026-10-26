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

// FreshProjectPermissions reuses the same current-DB permission calculation as
// project delegation. It does not depend on an asynchronously reloaded enforcer.
func FreshProjectPermissions(db *gorm.DB, ctx context.Context, userID, projectID string) (*FreshProjectAuthorization, error) {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.ProjectID == "" || scope.ProjectID != projectID || scope.DepartmentID == "" || userID == "" {
		return nil, ErrAccessDelegationDenied
	}
	result := &FreshProjectAuthorization{Permissions: map[string]bool{}}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&result.User, "uuid = ?", userID).Error; err != nil || !result.User.IsActive {
			return ErrAccessDelegationDenied
		}
		var project model.Project
		if err := tx.First(&project, "uuid = ?", projectID).Error; err != nil || !project.IsActive || project.DepartmentID != scope.DepartmentID {
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
