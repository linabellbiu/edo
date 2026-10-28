package identity

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/access"
	"edo/internal/model"
)

type RoleChoice struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

func (s *Service) delegationFailure(ctx context.Context, operation, actorID, providerID string, err error) error {
	slog.ErrorContext(ctx, "登录方式授权校验失败", "operation", operation, "actor_user_id", actorID, "provider_id", providerID, "err", err)
	if errors.Is(err, access.ErrAccessDelegationDenied) || errors.Is(err, access.ErrInvalidUserRoles) || errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrProviderAccessDenied
	}
	return err
}

func providerRoleIDs(roleID string) []string {
	roleID = strings.TrimSpace(roleID)
	if roleID == "" {
		return nil
	}
	return []string{roleID}
}

func (s *Service) RoleChoices(ctx context.Context, actor *model.User) ([]RoleChoice, error) {
	result := []RoleChoice{}
	if actor == nil {
		return nil, s.delegationFailure(ctx, "identity_role_choices", "", "", access.ErrAccessDelegationDenied)
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		action := ""
		for _, code := range []string{access.PermissionIdentityCreate, access.PermissionIdentityUpdate} {
			err := access.ValidateRoleDelegation(tx, actor, code, nil)
			if err == nil {
				action = code
				break
			}
			if !errors.Is(err, access.ErrAccessDelegationDenied) {
				return err
			}
		}
		if action == "" {
			return access.ErrAccessDelegationDenied
		}
		var roles []model.Role
		if err := tx.Order("name ASC").Find(&roles).Error; err != nil {
			return err
		}
		for _, role := range roles {
			err := access.ValidateRoleDelegation(tx, actor, action, []string{role.ID})
			if errors.Is(err, access.ErrAccessDelegationDenied) {
				continue
			}
			if err != nil {
				return err
			}
			result = append(result, RoleChoice{ID: role.ID, DisplayName: role.DisplayName})
		}
		return nil
	})
	if err != nil {
		return nil, s.delegationFailure(ctx, "identity_role_choices", actor.ID, "", err)
	}
	return result, nil
}

func (s *Service) CreateAs(ctx context.Context, actor *model.User, input ProviderInput) (*ProviderView, error) {
	if actor == nil {
		return nil, s.delegationFailure(ctx, "identity_create", "", "", access.ErrAccessDelegationDenied)
	}
	var result *ProviderView
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := access.ValidateRoleDelegation(tx, actor, access.PermissionIdentityCreate, providerRoleIDs(input.DefaultRoleID)); err != nil {
			return err
		}
		copy := *s
		copy.db = tx
		var err error
		result, err = copy.createProvider(ctx, input, actor.ID)
		return err
	})
	if err != nil {
		return nil, s.delegationFailure(ctx, "identity_create", actor.ID, "", err)
	}
	return result, nil
}

func (s *Service) UpdateAs(ctx context.Context, actor *model.User, id string, input ProviderInput) (*ProviderView, error) {
	if actor == nil {
		return nil, s.delegationFailure(ctx, "identity_update", "", id, access.ErrAccessDelegationDenied)
	}
	var result *ProviderView
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := access.ValidateRoleDelegation(tx, actor, access.PermissionIdentityUpdate, providerRoleIDs(input.DefaultRoleID)); err != nil {
			return err
		}
		copy := *s
		copy.db = tx
		var err error
		result, err = copy.updateProvider(ctx, id, input)
		return err
	})
	if err != nil {
		return nil, s.delegationFailure(ctx, "identity_update", actor.ID, id, err)
	}
	return result, nil
}

func (s *Service) SetActiveAs(ctx context.Context, actor *model.User, id string, active bool) error {
	if actor == nil {
		return s.delegationFailure(ctx, "identity_status", "", id, access.ErrAccessDelegationDenied)
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var provider model.IdentityProvider
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&provider, "uuid = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrProviderNotFound
			}
			return err
		}
		var roles []string
		if active {
			roles = providerRoleIDs(provider.DefaultRoleID)
		}
		if err := access.ValidateRoleDelegation(tx, actor, access.PermissionIdentityUpdate, roles); err != nil {
			return err
		}
		copy := *s
		copy.db = tx
		return copy.setProviderActive(ctx, id, active)
	})
	if err != nil {
		return s.delegationFailure(ctx, "identity_status", actor.ID, id, err)
	}
	return nil
}
