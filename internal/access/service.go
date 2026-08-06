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
	Permissions        []string `json:"permissions"`
	InUse              bool     `json:"-"`
	VisibleMemberCount int64    `json:"-"`
}

type RoleInput struct {
	Name        string
	DisplayName string
	Description string
	Permissions []string
}

type RoleBasicInput struct {
	Name        string
	DisplayName string
	Description string
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
	roleIDs := make([]string, 0, len(roles))
	for i := range roles {
		roleIDs = append(roleIDs, roles[i].ID)
	}
	visibleMemberCounts, rolesInUse, err := s.roleMemberSummary(ctx, roleIDs)
	if err != nil {
		return nil, err
	}
	result := make([]RoleWithPermissions, 0, len(roles))
	for _, role := range roles {
		permissions, err := s.rolePermissions(ctx, role.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, RoleWithPermissions{
			Role: role, Permissions: permissions,
			InUse: rolesInUse[role.ID], VisibleMemberCount: visibleMemberCounts[role.ID],
		})
	}
	return result, nil
}

func (s *Service) roleMemberSummary(
	ctx context.Context,
	roleIDs []string,
) (map[string]int64, map[string]bool, error) {
	visibleCounts := make(map[string]int64, len(roleIDs))
	inUse := make(map[string]bool, len(roleIDs))
	if len(roleIDs) == 0 {
		return visibleCounts, inUse, nil
	}
	var usedRoleIDs []string
	// UserRole 不携带部门字段；这里只返回是否被任意用户使用，不暴露跨部门成员数量。
	if err := s.db.WithContext(ctx).Model(&model.UserRole{}).
		Where("role_id IN ?", roleIDs).
		Group("role_id").
		Pluck("role_id", &usedRoleIDs).Error; err != nil {
		return nil, nil, fmt.Errorf("检查角色使用状态失败: %w", err)
	}
	for _, roleID := range usedRoleIDs {
		inUse[roleID] = true
	}
	type roleMemberCount struct {
		RoleID      string
		MemberCount int64
	}
	var rows []roleMemberCount
	// 以 users 为查询模型，使数据库层的 DepartmentScope 自动限制为当前调用者可见用户。
	if err := s.db.WithContext(ctx).Model(&model.User{}).
		Select("user_roles.role_id AS role_id, COUNT(DISTINCT users.id) AS member_count").
		Joins("JOIN user_roles ON user_roles.user_id = users.id").
		Where("user_roles.role_id IN ?", roleIDs).
		Group("user_roles.role_id").
		Scan(&rows).Error; err != nil {
		return nil, nil, fmt.Errorf("统计可见角色成员失败: %w", err)
	}
	for i := range rows {
		visibleCounts[rows[i].RoleID] = rows[i].MemberCount
	}
	return visibleCounts, inUse, nil
}

func (s *Service) CreateRoleAs(
	ctx context.Context,
	actor *model.User,
	input RoleInput,
) (*RoleWithPermissions, error) {
	return s.createRole(ctx, actor, input)
}

func (s *Service) CreateRole(ctx context.Context, input RoleInput) (*RoleWithPermissions, error) {
	return s.createRole(ctx, nil, input)
}

func (s *Service) createRole(
	ctx context.Context,
	actor *model.User,
	input RoleInput,
) (*RoleWithPermissions, error) {
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
		if err := validateAccessDelegation(
			tx, actor, PermissionRoleCreate, "", permissionCodeSet(input.Permissions), nil,
		); err != nil {
			return err
		}
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
		s.logger.Error("角色已创建但权限策略同步失败", "operation", "role_create", "role_id", role.ID, "err", err)
		return &RoleWithPermissions{Role: role, Permissions: input.Permissions}, ErrRoleCreatedSyncPending
	}
	return &RoleWithPermissions{Role: role, Permissions: input.Permissions}, nil
}

func (s *Service) UpdateRole(ctx context.Context, roleID string, input RoleInput) (*RoleWithPermissions, error) {
	return s.updateRole(ctx, nil, roleID, input)
}

func (s *Service) UpdateRoleAs(
	ctx context.Context,
	actor *model.User,
	roleID string,
	input RoleInput,
) (*RoleWithPermissions, error) {
	return s.updateRole(ctx, actor, roleID, input)
}

func (s *Service) updateRole(
	ctx context.Context,
	actor *model.User,
	roleID string,
	input RoleInput,
) (*RoleWithPermissions, error) {
	input, err := normalizeRoleInput(input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var role model.Role
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateAccessDelegation(
			tx, actor, PermissionRoleUpdate, "", permissionCodeSet(input.Permissions), nil,
		); err != nil {
			return err
		}
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
	result := &RoleWithPermissions{Role: role, Permissions: input.Permissions}
	if err := s.syncRolePermissionsOrDisable(ctx, role.ID, "role_update"); err != nil {
		return result, err
	}
	return result, nil
}

// UpdateRoleBasic 只修改角色标识、展示名称和说明，不触碰权限配置。
func (s *Service) UpdateRoleBasic(
	ctx context.Context,
	roleID string,
	input RoleBasicInput,
) (*RoleWithPermissions, error) {
	input, err := normalizeRoleBasicInput(input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var role model.Role
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&role, "id = ?", roleID).Error; err != nil {
			return err
		}
		return tx.Model(&role).Updates(map[string]any{
			"name": input.Name, "display_name": input.DisplayName,
			"description": input.Description, "updated_at": now,
		}).Error
	}); err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return nil, ErrRoleNotFound
		case errors.Is(err, gorm.ErrDuplicatedKey):
			return nil, ErrRoleNameExists
		default:
			return nil, fmt.Errorf("更新角色基本信息失败: %w", err)
		}
	}
	role.Name = input.Name
	role.DisplayName = input.DisplayName
	role.Description = input.Description
	role.UpdatedAt = now
	permissions, err := s.rolePermissions(ctx, role.ID)
	if err != nil {
		return nil, err
	}
	return &RoleWithPermissions{Role: role, Permissions: permissions}, nil
}

// UpdateRolePermissions 只替换角色权限，不回写可能已经被其他请求修改的基本信息。
func (s *Service) UpdateRolePermissions(
	ctx context.Context,
	roleID string,
	permissions []string,
) (*RoleWithPermissions, error) {
	return s.updateRolePermissions(ctx, nil, roleID, permissions)
}

func (s *Service) UpdateRolePermissionsAs(
	ctx context.Context,
	actor *model.User,
	roleID string,
	permissions []string,
) (*RoleWithPermissions, error) {
	return s.updateRolePermissions(ctx, actor, roleID, permissions)
}

func (s *Service) updateRolePermissions(
	ctx context.Context,
	actor *model.User,
	roleID string,
	permissions []string,
) (*RoleWithPermissions, error) {
	permissions, err := normalizeRolePermissions(permissions)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var role model.Role
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateAccessDelegation(
			tx, actor, PermissionRoleUpdate, "", permissionCodeSet(permissions), nil,
		); err != nil {
			return err
		}
		if err := tx.First(&role, "id = ?", roleID).Error; err != nil {
			return err
		}
		if err := replacePermissions(tx, role.ID, permissions, now); err != nil {
			return err
		}
		return tx.Model(&role).Update("updated_at", now).Error
	}); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRoleNotFound
		}
		return nil, fmt.Errorf("更新角色权限失败: %w", err)
	}
	role.UpdatedAt = now
	result := &RoleWithPermissions{Role: role, Permissions: permissions}
	if err := s.syncRolePermissionsOrDisable(ctx, role.ID, "role_permissions_update"); err != nil {
		return result, err
	}
	return result, nil
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
	if err := s.syncPolicy("role_delete"); err != nil {
		s.logger.Error("角色已删除但权限策略同步失败", "operation", "role_delete", "role_id", roleID, "err", err)
		return ErrRoleDeletedSyncPending
	}
	return nil
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
	return s.setUserRoles(ctx, nil, userID, roleIDs)
}

func (s *Service) SetUserRolesAs(ctx context.Context, actor *model.User, userID string, roleIDs []string) error {
	return s.setUserRoles(ctx, actor, userID, roleIDs)
}

func (s *Service) setUserRoles(ctx context.Context, actor *model.User, userID string, roleIDs []string) error {
	roleIDs = uniqueSorted(roleIDs)
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.First(&user, "id = ?", userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return account.ErrUserNotFound
			}
			return fmt.Errorf("查询待配置角色的用户失败: %w", err)
		}
		if user.IsSuperuser {
			return account.ErrSuperuserImmutable
		}
		rolePermissions, err := validatedRolePermissionSet(tx, roleIDs)
		if err != nil {
			return err
		}
		if err := validateAccessDelegation(tx, actor, PermissionUserUpdate, user.ID, rolePermissions, nil); err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&model.UserRole{}).Error; err != nil {
			return fmt.Errorf("清理用户原有角色失败: %w", err)
		}
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
	}); err != nil {
		return err
	}
	return s.syncUserAccessOrDisable(ctx, userID, "user_roles_update")
}

func (s *Service) SetUserPermissions(ctx context.Context, userID string, overrides UserPermissionOverrides) error {
	return s.setUserPermissions(ctx, nil, userID, overrides)
}

func (s *Service) SetUserPermissionsAs(ctx context.Context, actor *model.User, userID string, overrides UserPermissionOverrides) error {
	return s.setUserPermissions(ctx, actor, userID, overrides)
}

func (s *Service) setUserPermissions(ctx context.Context, actor *model.User, userID string, overrides UserPermissionOverrides) error {
	overrides, err := normalizeUserPermissionOverrides(overrides)
	if err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.First(&user, "id = ?", userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return account.ErrUserNotFound
			}
			return fmt.Errorf("查询用户失败: %w", err)
		}
		if user.IsSuperuser {
			return account.ErrSuperuserImmutable
		}
		var currentRoleIDs []string
		if err := tx.Model(&model.UserRole{}).Where("user_id = ?", user.ID).Pluck("role_id", &currentRoleIDs).Error; err != nil {
			return fmt.Errorf("查询用户当前角色失败: %w", err)
		}
		rolePermissions, err := validatedRolePermissionSet(tx, uniqueSorted(currentRoleIDs))
		if err != nil {
			return err
		}
		if err := validateAccessDelegation(tx, actor, PermissionUserUpdate, user.ID, rolePermissions, overrides.Allow); err != nil {
			return err
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
	return s.syncUserAccessOrDisable(ctx, userID, "user_permissions_update")
}

// SetUserAccess 在同一个事务中替换用户的角色和用户级权限例外。
// 只有数据库提交成功后才重新加载一次 Casbin 策略，避免页面分步保存造成部分配置生效。
func (s *Service) SetUserAccess(
	ctx context.Context,
	userID string,
	roleIDs []string,
	overrides UserPermissionOverrides,
) error {
	return s.setUserAccess(ctx, nil, userID, roleIDs, overrides)
}

func (s *Service) SetUserAccessAs(
	ctx context.Context,
	actor *model.User,
	userID string,
	roleIDs []string,
	overrides UserPermissionOverrides,
) error {
	return s.setUserAccess(ctx, actor, userID, roleIDs, overrides)
}

func (s *Service) setUserAccess(
	ctx context.Context,
	actor *model.User,
	userID string,
	roleIDs []string,
	overrides UserPermissionOverrides,
) error {
	roleIDs = uniqueSorted(roleIDs)
	overrides, err := normalizeUserPermissionOverrides(overrides)
	if err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.First(&user, "id = ?", userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return account.ErrUserNotFound
			}
			return fmt.Errorf("查询待配置权限的用户失败: %w", err)
		}
		if user.IsSuperuser {
			return account.ErrSuperuserImmutable
		}
		rolePermissions, err := validatedRolePermissionSet(tx, roleIDs)
		if err != nil {
			return err
		}
		if err := validateAccessDelegation(tx, actor, PermissionUserUpdate, user.ID, rolePermissions, overrides.Allow); err != nil {
			return err
		}

		now := time.Now().UTC()
		if err := tx.Where("user_id = ?", user.ID).Delete(&model.UserRole{}).Error; err != nil {
			return fmt.Errorf("清理用户原有角色失败: %w", err)
		}
		assignments := make([]model.UserRole, 0, len(roleIDs))
		for _, roleID := range roleIDs {
			assignments = append(assignments, model.UserRole{UserID: user.ID, RoleID: roleID, CreatedAt: now})
		}
		if len(assignments) > 0 {
			if err := tx.Create(&assignments).Error; err != nil {
				return fmt.Errorf("保存用户角色失败: %w", err)
			}
		}

		if err := tx.Where("user_id = ?", user.ID).Delete(&model.UserPermission{}).Error; err != nil {
			return fmt.Errorf("清理用户权限覆盖失败: %w", err)
		}
		permissions := make([]model.UserPermission, 0, len(overrides.Allow)+len(overrides.Deny))
		for _, permission := range overrides.Allow {
			permissions = append(permissions, model.UserPermission{
				UserID: user.ID, Permission: permission, Effect: model.PermissionAllow,
				CreatedAt: now, UpdatedAt: now,
			})
		}
		for _, permission := range overrides.Deny {
			permissions = append(permissions, model.UserPermission{
				UserID: user.ID, Permission: permission, Effect: model.PermissionDeny,
				CreatedAt: now, UpdatedAt: now,
			})
		}
		if len(permissions) > 0 {
			if err := tx.Create(&permissions).Error; err != nil {
				return fmt.Errorf("保存用户权限覆盖失败: %w", err)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return s.syncUserAccessOrDisable(ctx, userID, "user_access_update")
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
	return s.createUserInDepartment(ctx, nil, username, nickname, password, departmentID, roleIDs)
}

func (s *Service) CreateUserInDepartmentAs(
	ctx context.Context,
	actor *model.User,
	username, nickname, password, departmentID string,
	roleIDs []string,
) (*model.User, error) {
	return s.createUserInDepartment(ctx, actor, username, nickname, password, departmentID, roleIDs)
}

func (s *Service) createUserInDepartment(
	ctx context.Context,
	actor *model.User,
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
		rolePermissions, err := validatedRolePermissionSet(tx, roleIDs)
		if err != nil {
			return err
		}
		if err := validateAccessDelegation(tx, actor, PermissionUserCreate, "", rolePermissions, nil); err != nil {
			return err
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
		s.logger.Error("用户已创建但权限策略同步失败", "operation", "user_create", "user_id", user.ID, "err", err)
		return user, ErrUserCreatedSyncPending
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
	basic, err := normalizeRoleBasicInput(RoleBasicInput{
		Name: input.Name, DisplayName: input.DisplayName, Description: input.Description,
	})
	if err != nil {
		return RoleInput{}, ErrInvalidRole
	}
	permissions, err := normalizeRolePermissions(input.Permissions)
	if err != nil {
		return RoleInput{}, err
	}
	input.Name = basic.Name
	input.DisplayName = basic.DisplayName
	input.Description = basic.Description
	input.Permissions = permissions
	return input, nil
}

func normalizeRoleBasicInput(input RoleBasicInput) (RoleBasicInput, error) {
	input.Name = strings.ToLower(strings.TrimSpace(input.Name))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Description = strings.TrimSpace(input.Description)
	if !roleNamePattern.MatchString(input.Name) || input.DisplayName == "" ||
		utf8.RuneCountInString(input.DisplayName) > 64 || utf8.RuneCountInString(input.Description) > 255 {
		return RoleBasicInput{}, ErrInvalidRole
	}
	return input, nil
}

func normalizeRolePermissions(permissions []string) ([]string, error) {
	permissions, ok := normalizePermissionCodes(permissions)
	if !ok {
		return nil, ErrInvalidPermission
	}
	return permissions, nil
}

func normalizeUserPermissionOverrides(overrides UserPermissionOverrides) (UserPermissionOverrides, error) {
	var ok bool
	overrides.Allow, ok = normalizePermissionCodes(overrides.Allow)
	if !ok {
		return UserPermissionOverrides{}, ErrInvalidUserPermissions
	}
	overrides.Deny, ok = normalizePermissionCodes(overrides.Deny)
	if !ok {
		return UserPermissionOverrides{}, ErrInvalidUserPermissions
	}
	seen := make(map[string]struct{}, len(overrides.Allow)+len(overrides.Deny))
	for _, permission := range overrides.Allow {
		seen[permission] = struct{}{}
	}
	for _, permission := range overrides.Deny {
		if _, exists := seen[permission]; exists {
			return UserPermissionOverrides{}, ErrInvalidUserPermissions
		}
		seen[permission] = struct{}{}
	}
	return overrides, nil
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
