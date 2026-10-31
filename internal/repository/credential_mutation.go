package repository

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/database"
	"edo/internal/model"
)

func normalizedReferenceIDs(input normalizedInput) []string {
	ids := make([]string, 0, 2)
	for _, id := range []*string{input.credentialID, input.apiCredentialID} {
		if id != nil {
			ids = append(ids, *id)
		}
	}
	return ids
}

func lockRepositoryMutation(tx *gorm.DB, expected *model.GitRepository) error {
	// 无最终凭据的更新也先写后读，避免 SQLite 的读事务升级及 MySQL 旧快照。
	if err := tx.Model(&model.GitRepository{}).Where("uuid = ?", expected.ID).
		UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
		return fmt.Errorf("锁定待修改代码仓库失败: %w", err)
	}
	var current model.GitRepository
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "uuid = ?", expected.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrRepositoryNotFound
		}
		return fmt.Errorf("读取当前代码仓库失败: %w", err)
	}
	if current.CloneURL != expected.CloneURL || current.Provider != expected.Provider || current.AuthType != expected.AuthType ||
		current.Username != expected.Username || current.CredentialCiphertext != expected.CredentialCiphertext ||
		current.AllowInsecureHTTP != expected.AllowInsecureHTTP || current.WebhookEnabled != expected.WebhookEnabled || current.WebhookSecretCiphertext != expected.WebhookSecretCiphertext ||
		referenceValue(current.CredentialID) != referenceValue(expected.CredentialID) || referenceValue(current.APICredentialID) != referenceValue(expected.APICredentialID) {
		return ErrRepositoryChanged
	}
	return nil
}

func validateLockedReferences(tx *gorm.DB, actorID string, existing *model.GitRepository, requested Input, normalized *normalizedInput) (err error) {
	defer func() {
		if err != nil {
			slog.WarnContext(tx.Statement.Context, "代码仓库凭据复核失败", "operation", "repository_reference_validate", "actor_user_id", actorID, "credential_ids", normalizedReferenceIDs(*normalized), "err", err)
		}
	}()
	owners := make(map[string]bool, 2)
	read := func(id string) (*model.GitCredential, error) {
		var value model.GitCredential
		if err := tx.Select("uuid", "user_id", "provider", "auth_type", "username").
			Clauses(clause.Locking{Strength: "UPDATE"}).First(&value, "uuid = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrInvalidCredential
			}
			return nil, fmt.Errorf("复核代码仓库凭据失败: %w", err)
		}
		if !owners[value.UserID] {
			var owner struct {
				ID           string `gorm:"column:uuid"`
				DepartmentID string
			}
			// 所有凭据写锁已取得；不反向锁用户，避免与删除/调部门的用户→凭据顺序冲突。
			if err := tx.Table("users").Select("uuid", "department_id").Where("uuid = ?", value.UserID).Take(&owner).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, ErrInvalidCredential
				}
				return nil, fmt.Errorf("复核代码仓库凭据所有者失败: %w", err)
			}
			if scope, scoped := database.DepartmentScopeFromContext(tx.Statement.Context); scoped && !scope.AllDepartments && (scope.DepartmentID == "" || owner.DepartmentID != scope.DepartmentID) {
				return nil, ErrInvalidCredential
			}
			owners[value.UserID] = true
		}
		return &value, nil
	}
	if normalized.credentialID != nil {
		value, err := read(*normalized.credentialID)
		if err != nil {
			return err
		}
		retained := existing != nil && requested.CredentialID == nil && requested.Credential == nil && referenceValue(existing.CredentialID) == value.ID
		if !retained && value.UserID != actorID || value.AuthType != normalized.AuthType || !credentialProviderCompatible(value.Provider, normalized.Provider) {
			return ErrInvalidCredential
		}
		if !retained && strings.TrimSpace(requested.Username) == "" {
			normalized.Username = value.Username
		}
	}
	if normalized.apiCredentialID != nil {
		value, err := read(*normalized.apiCredentialID)
		if err != nil {
			return err
		}
		if value.UserID != actorID || value.AuthType != model.GitAuthToken || value.Provider != normalized.Provider {
			return ErrInvalidCredential
		}
	}
	return nil
}

func referenceValue(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

func logRepositoryMutationError(ctx context.Context, operation, actorID, repositoryID string, err error) {
	if err != nil {
		slog.WarnContext(ctx, "代码仓库变更失败", "operation", operation, "actor_user_id", actorID, "repository_id", repositoryID, "err", err)
	}
}
