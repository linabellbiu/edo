package access

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/persist"
	rediswatcher "github.com/casbin/redis-watcher/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"edo/internal/account"
	"edo/internal/database"
	"edo/internal/model"
)

var (
	ErrInvalidRole            = errors.New("角色信息无效")
	ErrRoleNameExists         = errors.New("角色标识已存在")
	ErrRoleNotFound           = errors.New("角色不存在")
	ErrRoleInUse              = errors.New("角色仍被用户使用，不能删除")
	ErrInvalidPermission      = errors.New("角色包含未知权限")
	ErrInvalidUserRoles       = errors.New("用户角色配置无效")
	ErrInvalidUserPermissions = errors.New("用户权限覆盖配置无效")
	ErrPolicySync             = errors.New("权限策略同步失败，请稍后重试")
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

type UserPermissionOverrides struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

type Service struct {
	db       *gorm.DB
	enforcer *casbin.SyncedEnforcer
	watcher  persist.Watcher
	logger   *slog.Logger
}

func NewService(db *gorm.DB) (*Service, error) {
	authorizationModel, err := newAuthorizationModel()
	if err != nil {
		return nil, err
	}
	enforcer, err := casbin.NewSyncedEnforcer(authorizationModel, &policyAdapter{db: db})
	if err != nil {
		return nil, fmt.Errorf("初始化 Casbin 权限引擎失败: %w", err)
	}
	enforcer.EnableAutoSave(false)
	return &Service{db: db, enforcer: enforcer, logger: slog.Default()}, nil
}

func NewDistributedService(
	db *gorm.DB,
	redisClient redis.UniversalClient,
	channel string,
	logger *slog.Logger,
) (*Service, error) {
	service, err := NewService(db)
	if err != nil {
		return nil, err
	}
	if logger != nil {
		service.logger = logger
	}
	watcher, err := rediswatcher.NewWatcher("", rediswatcher.WatcherOptions{
		SubClient:  redisClient,
		PubClient:  redisClient,
		Channel:    channel,
		IgnoreSelf: true,
		OptionalUpdateCallback: func(string) {
			if err := service.enforcer.LoadPolicy(); err != nil {
				service.logger.Error("重新加载 Casbin 权限策略失败", "operation", "rbac_policy_reload", "err", err)
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("初始化 Casbin Redis 策略同步失败: %w", err)
	}
	service.watcher = watcher
	if err := service.enforcer.SetWatcher(watcher); err != nil {
		return nil, fmt.Errorf("绑定 Casbin Redis 策略同步失败: %w", err)
	}
	return service, nil
}

func (s *Service) HasPermission(ctx context.Context, user *model.User, permission string) (bool, error) {
	if user == nil || !IsKnown(permission) {
		return false, nil
	}
	if user.IsSuperuser {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	allowed, err := s.enforcer.Enforce(userSubject(user.ID), permission)
	if err != nil {
		return false, fmt.Errorf("执行 Casbin 权限判定失败: %w", err)
	}
	return allowed, nil
}

func (s *Service) UserPermissions(ctx context.Context, user *model.User) ([]string, error) {
	if user == nil {
		return []string{}, nil
	}
	if user.IsSuperuser {
		return []string{"*"}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	permissions := make([]string, 0, len(catalog))
	for _, item := range catalog {
		allowed, err := s.enforcer.Enforce(userSubject(user.ID), item.Code)
		if err != nil {
			return nil, fmt.Errorf("计算 Casbin 有效权限失败: %w", err)
		}
		if allowed {
			permissions = append(permissions, item.Code)
		}
	}
	sort.Strings(permissions)
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
	if err := s.syncPolicy("role_create"); err != nil {
		return nil, err
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
	if err := s.syncPolicy("role_update"); err != nil {
		return nil, err
	}
	role.Name = input.Name
	role.DisplayName = input.DisplayName
	role.Description = input.Description
	role.UpdatedAt = now
	return &RoleWithPermissions{Role: role, Permissions: input.Permissions}, nil
}

func (s *Service) DeleteRole(ctx context.Context, roleID string) error {
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
	}); err != nil {
		return err
	}
	return s.syncPolicy("role_delete")
}

// DeleteUser 删除账户自身的身份和授权数据，但保留其已经创建的部门业务资源及审计记录。
// 业务资源使用冻结的 department_id 归属部门，不能因人员离职而被级联删除。
func (s *Service) DeleteUser(ctx context.Context, userID string) error {
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.First(&user, "id = ?", userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return account.ErrUserNotFound
			}
			return fmt.Errorf("查询待删除用户失败: %w", err)
		}
		if user.IsSuperuser {
			return account.ErrSuperuserImmutable
		}
		if err := account.NewService(tx).EnsureCredentialsUnreferenced(ctx, user.ID, user.DepartmentID); err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&model.UserRole{}).Error; err != nil {
			return fmt.Errorf("删除用户角色失败: %w", err)
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&model.UserPermission{}).Error; err != nil {
			return fmt.Errorf("删除用户权限覆盖失败: %w", err)
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&model.ExternalIdentity{}).Error; err != nil {
			return fmt.Errorf("删除用户外部身份失败: %w", err)
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&model.GitCredential{}).Error; err != nil {
			return fmt.Errorf("删除用户个人 Git 令牌失败: %w", err)
		}
		if err := tx.Delete(&user).Error; err != nil {
			return fmt.Errorf("删除用户失败: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	return s.syncPolicy("user_delete")
}

func (s *Service) SetUserRoles(ctx context.Context, userID string, roleIDs []string) error {
	roleIDs = uniqueSorted(roleIDs)
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
	}); err != nil {
		return err
	}
	return s.syncPolicy("user_roles_update")
}

func (s *Service) SetUserPermissions(ctx context.Context, userID string, overrides UserPermissionOverrides) error {
	var ok bool
	overrides.Allow, ok = normalizePermissionCodes(overrides.Allow)
	if !ok {
		return ErrInvalidUserPermissions
	}
	overrides.Deny, ok = normalizePermissionCodes(overrides.Deny)
	if !ok {
		return ErrInvalidUserPermissions
	}
	seen := make(map[string]struct{}, len(overrides.Allow)+len(overrides.Deny))
	for _, permission := range overrides.Allow {
		seen[permission] = struct{}{}
	}
	for _, permission := range overrides.Deny {
		if _, exists := seen[permission]; exists {
			return ErrInvalidUserPermissions
		}
		seen[permission] = struct{}{}
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.First(&user, "id = ?", userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvalidUserPermissions
			}
			return fmt.Errorf("查询用户失败: %w", err)
		}
		if user.IsSuperuser {
			return ErrInvalidUserPermissions
		}
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserPermission{}).Error; err != nil {
			return fmt.Errorf("清理用户权限覆盖失败: %w", err)
		}
		now := time.Now().UTC()
		items := make([]model.UserPermission, 0, len(overrides.Allow)+len(overrides.Deny))
		for _, permission := range overrides.Allow {
			items = append(items, model.UserPermission{UserID: userID, Permission: permission, Effect: model.PermissionAllow, CreatedAt: now, UpdatedAt: now})
		}
		for _, permission := range overrides.Deny {
			items = append(items, model.UserPermission{UserID: userID, Permission: permission, Effect: model.PermissionDeny, CreatedAt: now, UpdatedAt: now})
		}
		if len(items) > 0 {
			if err := tx.Create(&items).Error; err != nil {
				return fmt.Errorf("保存用户权限覆盖失败: %w", err)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return s.syncPolicy("user_permissions_update")
}

func (s *Service) UserPermissionOverrides(ctx context.Context, userID string) (UserPermissionOverrides, error) {
	var items []model.UserPermission
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Order("permission ASC").Find(&items).Error; err != nil {
		return UserPermissionOverrides{}, fmt.Errorf("查询用户权限覆盖失败: %w", err)
	}
	result := UserPermissionOverrides{Allow: []string{}, Deny: []string{}}
	for _, item := range items {
		if item.Effect == model.PermissionAllow {
			result.Allow = append(result.Allow, item.Permission)
		} else if item.Effect == model.PermissionDeny {
			result.Deny = append(result.Deny, item.Permission)
		}
	}
	return result, nil
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
	return s.CreateUserInDepartment(ctx, username, nickname, password, "", roleIDs)
}

func (s *Service) CreateUserInDepartment(
	ctx context.Context,
	username, nickname, password, departmentID string,
	roleIDs []string,
) (*model.User, error) {
	if strings.TrimSpace(departmentID) == "" {
		if scope, ok := database.DepartmentScopeFromContext(ctx); ok {
			departmentID = scope.DepartmentID
		}
	}
	if strings.TrimSpace(departmentID) == "" {
		departmentID = database.DefaultDepartmentID
	}
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
		created, err := account.NewService(tx).CreateUserInDepartment(ctx, username, nickname, password, departmentID)
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
	if err := s.syncPolicy("user_create"); err != nil {
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
	permissions, ok := normalizePermissionCodes(input.Permissions)
	if !ok {
		return RoleInput{}, ErrInvalidPermission
	}
	input.Permissions = permissions
	return input, nil
}

// normalizePermissionCodes 在写入数据库前把旧聚合权限展开为当前细粒度权限。
// 同时提交旧码和对应新码时会自动去重，便于前后端滚动升级。
func normalizePermissionCodes(permissions []string) ([]string, bool) {
	expanded := make([]string, 0, len(permissions))
	for _, permission := range uniqueSorted(permissions) {
		if !IsKnown(permission) {
			return nil, false
		}
		expanded = append(expanded, ExpandLegacyPermission(permission)...)
	}
	return uniqueSorted(expanded), true
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

func (s *Service) syncPolicy(operation string) error {
	if err := s.enforcer.LoadPolicy(); err != nil {
		s.logger.Error("重新加载 Casbin 权限策略失败", "operation", operation, "err", err)
		return ErrPolicySync
	}
	if s.watcher != nil {
		if err := s.watcher.Update(); err != nil {
			s.logger.Error("发布 Casbin 权限策略变更失败", "operation", operation, "err", err)
			return ErrPolicySync
		}
	}
	return nil
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
