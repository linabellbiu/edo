package access

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"zrt/internal/account"
	"zrt/internal/model"
)

var (
	ErrInvalidRole       = errors.New("角色信息无效")
	ErrRoleNameExists    = errors.New("角色标识已存在")
	ErrRoleNotFound      = errors.New("角色不存在")
	ErrRoleInUse         = errors.New("角色仍被用户使用，不能删除")
	ErrInvalidPermission = errors.New("角色包含未知权限")
	ErrInvalidUserRoles  = errors.New("用户角色配置无效")
)

var roleNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,63}$`)

type RoleWithPermissions struct {
	model.Role
	Permissions []string `json:"permissions"`
}

type RoleInput struct {
	Name        string
	DisplayName string
	Description string
	Permissions []string
}

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) HasPermission(ctx context.Context, user *model.User, permission string) (bool, error) {
	if user == nil || !IsKnown(permission) {
		return false, nil
	}
	if user.IsSuperuser {
		return true, nil
	}
	var count int64
	err := s.db.WithContext(ctx).Table("user_roles AS ur").
		Joins("JOIN role_permissions AS rp ON rp.role_id = ur.role_id").
		Where("ur.user_id = ? AND rp.permission = ?", user.ID, permission).
		Limit(1).Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("查询用户权限失败: %w", err)
	}
	return count > 0, nil
}

func (s *Service) UserPermissions(ctx context.Context, user *model.User) ([]string, error) {
	if user == nil {
		return []string{}, nil
	}
	if user.IsSuperuser {
		return []string{"*"}, nil
	}
	var permissions []string
	err := s.db.WithContext(ctx).Table("user_roles AS ur").Distinct("rp.permission").
		Joins("JOIN role_permissions AS rp ON rp.role_id = ur.role_id").
		Where("ur.user_id = ?", user.ID).Order("rp.permission ASC").Pluck("rp.permission", &permissions).Error
	if err != nil {
		return nil, fmt.Errorf("查询用户权限列表失败: %w", err)
	}
	return permissions, nil
}

func (s *Service) ListRoles(ctx context.Context) ([]RoleWithPermissions, error) {
	var roles []model.Role
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&roles).Error; err != nil {
		return nil, fmt.Errorf("查询角色失败: %w", err)
	}
	result := make([]RoleWithPermissions, 0, len(roles))
	for _, role := range roles {
		permissions, err := s.rolePermissions(ctx, role.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, RoleWithPermissions{Role: role, Permissions: permissions})
	}
	return result, nil
}

func (s *Service) CreateRole(ctx context.Context, input RoleInput) (*RoleWithPermissions, error) {
	input, err := normalizeRoleInput(input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	role := model.Role{
		ID: uuid.NewString(), Name: input.Name, DisplayName: input.DisplayName,
		Description: input.Description, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&role).Error; err != nil {
			return err
		}
		return replacePermissions(tx, role.ID, input.Permissions, now)
	}); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrRoleNameExists
		}
		return nil, fmt.Errorf("创建角色失败: %w", err)
	}
	return &RoleWithPermissions{Role: role, Permissions: input.Permissions}, nil
}

func (s *Service) UpdateRole(ctx context.Context, roleID string, input RoleInput) (*RoleWithPermissions, error) {
	input, err := normalizeRoleInput(input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var role model.Role
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&role, "id = ?", roleID).Error; err != nil {
			return err
		}
		if err := tx.Model(&role).Updates(map[string]any{
			"name": input.Name, "display_name": input.DisplayName,
			"description": input.Description, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return replacePermissions(tx, role.ID, input.Permissions, now)
	}); err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return nil, ErrRoleNotFound
		case errors.Is(err, gorm.ErrDuplicatedKey):
			return nil, ErrRoleNameExists
		default:
			return nil, fmt.Errorf("更新角色失败: %w", err)
		}
	}
	role.Name = input.Name
	role.DisplayName = input.DisplayName
	role.Description = input.Description
	role.UpdatedAt = now
	return &RoleWithPermissions{Role: role, Permissions: input.Permissions}, nil
}

func (s *Service) DeleteRole(ctx context.Context, roleID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var role model.Role
		if err := tx.First(&role, "id = ?", roleID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoleNotFound
			}
			return fmt.Errorf("查询待删除角色失败: %w", err)
		}
		var count int64
		if err := tx.Model(&model.UserRole{}).Where("role_id = ?", roleID).Count(&count).Error; err != nil {
			return fmt.Errorf("检查角色使用状态失败: %w", err)
		}
		if count > 0 {
			return ErrRoleInUse
		}
		if err := tx.Where("role_id = ?", roleID).Delete(&model.RolePermission{}).Error; err != nil {
			return fmt.Errorf("删除角色权限失败: %w", err)
		}
		if err := tx.Delete(&role).Error; err != nil {
			return fmt.Errorf("删除角色失败: %w", err)
		}
		return nil
	})
}

func (s *Service) SetUserRoles(ctx context.Context, userID string, roleIDs []string) error {
	roleIDs = uniqueSorted(roleIDs)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var userCount int64
		if err := tx.Model(&model.User{}).Where("id = ?", userID).Count(&userCount).Error; err != nil {
			return fmt.Errorf("查询用户失败: %w", err)
		}
		if userCount != 1 {
			return ErrInvalidUserRoles
		}
		if len(roleIDs) > 0 {
			var roleCount int64
			if err := tx.Model(&model.Role{}).Where("id IN ?", roleIDs).Count(&roleCount).Error; err != nil {
				return fmt.Errorf("查询待分配角色失败: %w", err)
			}
			if roleCount != int64(len(roleIDs)) {
				return ErrInvalidUserRoles
			}
		}
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserRole{}).Error; err != nil {
			return fmt.Errorf("清理用户原有角色失败: %w", err)
		}
		now := time.Now().UTC()
		assignments := make([]model.UserRole, 0, len(roleIDs))
		for _, roleID := range roleIDs {
			assignments = append(assignments, model.UserRole{UserID: userID, RoleID: roleID, CreatedAt: now})
		}
		if len(assignments) > 0 {
			if err := tx.Create(&assignments).Error; err != nil {
				return fmt.Errorf("保存用户角色失败: %w", err)
			}
		}
		return nil
	})
}

func (s *Service) ValidateRoleIDs(ctx context.Context, roleIDs []string) error {
	roleIDs = uniqueSorted(roleIDs)
	if len(roleIDs) == 0 {
		return nil
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.Role{}).Where("id IN ?", roleIDs).Count(&count).Error; err != nil {
		return fmt.Errorf("查询待分配角色失败: %w", err)
	}
	if count != int64(len(roleIDs)) {
		return ErrInvalidUserRoles
	}
	return nil
}

func (s *Service) CreateUser(
	ctx context.Context,
	username, nickname, password string,
	roleIDs []string,
) (*model.User, error) {
	roleIDs = uniqueSorted(roleIDs)
	var user *model.User
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(roleIDs) > 0 {
			var count int64
			if err := tx.Model(&model.Role{}).Where("id IN ?", roleIDs).Count(&count).Error; err != nil {
				return fmt.Errorf("查询待分配角色失败: %w", err)
			}
			if count != int64(len(roleIDs)) {
				return ErrInvalidUserRoles
			}
		}
		created, err := account.NewService(tx).CreateUser(ctx, username, nickname, password)
		if err != nil {
			return err
		}
		user = created
		now := time.Now().UTC()
		assignments := make([]model.UserRole, 0, len(roleIDs))
		for _, roleID := range roleIDs {
			assignments = append(assignments, model.UserRole{UserID: user.ID, RoleID: roleID, CreatedAt: now})
		}
		if len(assignments) > 0 {
			if err := tx.Create(&assignments).Error; err != nil {
				return fmt.Errorf("保存用户角色失败: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (s *Service) UserRoleIDs(ctx context.Context, userID string) ([]string, error) {
	var roleIDs []string
	if err := s.db.WithContext(ctx).Model(&model.UserRole{}).Where("user_id = ?", userID).
		Order("role_id ASC").Pluck("role_id", &roleIDs).Error; err != nil {
		return nil, fmt.Errorf("查询用户角色失败: %w", err)
	}
	return roleIDs, nil
}

func (s *Service) rolePermissions(ctx context.Context, roleID string) ([]string, error) {
	var permissions []string
	if err := s.db.WithContext(ctx).Model(&model.RolePermission{}).Where("role_id = ?", roleID).
		Order("permission ASC").Pluck("permission", &permissions).Error; err != nil {
		return nil, fmt.Errorf("查询角色权限失败: %w", err)
	}
	return permissions, nil
}

func normalizeRoleInput(input RoleInput) (RoleInput, error) {
	input.Name = strings.ToLower(strings.TrimSpace(input.Name))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Description = strings.TrimSpace(input.Description)
	if !roleNamePattern.MatchString(input.Name) || input.DisplayName == "" ||
		utf8.RuneCountInString(input.DisplayName) > 64 || utf8.RuneCountInString(input.Description) > 255 {
		return RoleInput{}, ErrInvalidRole
	}
	input.Permissions = uniqueSorted(input.Permissions)
	for _, permission := range input.Permissions {
		if !IsKnown(permission) {
			return RoleInput{}, ErrInvalidPermission
		}
	}
	return input, nil
}

func replacePermissions(tx *gorm.DB, roleID string, permissions []string, now time.Time) error {
	if err := tx.Where("role_id = ?", roleID).Delete(&model.RolePermission{}).Error; err != nil {
		return err
	}
	items := make([]model.RolePermission, 0, len(permissions))
	for _, permission := range permissions {
		items = append(items, model.RolePermission{RoleID: roleID, Permission: permission, CreatedAt: now})
	}
	if len(items) == 0 {
		return nil
	}
	return tx.Create(&items).Error
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
