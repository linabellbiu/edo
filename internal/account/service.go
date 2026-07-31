package account

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/auth"
	"edo/internal/model"
)

var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,31}$`)

const (
	initialAdminUsername = "admin"
	initialAdminPassword = "123456"
)

var (
	ErrInvalidUser        = errors.New("用户信息无效")
	ErrInvalidPassword    = errors.New("密码格式无效")
	ErrCurrentPassword    = errors.New("当前密码不正确")
	ErrPasswordUnchanged  = errors.New("新密码不能与当前密码相同")
	ErrUsernameExists     = errors.New("用户名已存在")
	ErrUserNotFound       = errors.New("用户不存在")
	ErrSuperuserImmutable = errors.New("不能通过接口修改超级管理员状态")
)

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

// EnsureInitialAdmin 仅在账户表为空时创建产品约定的默认管理员。
// 默认口令有意不经过普通账户的 12 位密码校验，也不设置强制改密状态。
func (s *Service) EnsureInitialAdmin(ctx context.Context) (*model.User, bool, error) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.User{}).Count(&count).Error; err != nil {
		return nil, false, fmt.Errorf("检查初始化账户失败: %w", err)
	}
	if count > 0 {
		return nil, false, nil
	}

	passwordHash, err := auth.HashPassword(initialAdminPassword)
	if err != nil {
		return nil, false, fmt.Errorf("生成初始化账户密码摘要失败: %w", err)
	}
	var created *model.User
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.User{}).Count(&count).Error; err != nil {
			return fmt.Errorf("再次检查初始化账户失败: %w", err)
		}
		if count > 0 {
			return nil
		}
		now := time.Now().UTC()
		created = &model.User{
			ID: uuid.NewString(), Username: initialAdminUsername, Nickname: "管理员", PasswordHash: passwordHash,
			IsActive: true, IsSuperuser: true, AuthVersion: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(created).Error; err != nil {
			return fmt.Errorf("创建初始化账户失败: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return created, created != nil, nil
}

func (s *Service) CreateAdmin(ctx context.Context, username, nickname, password string) (*model.User, error) {
	return s.create(ctx, username, nickname, password, true)
}

func (s *Service) CreateUser(ctx context.Context, username, nickname, password string) (*model.User, error) {
	return s.create(ctx, username, nickname, password, false)
}

func (s *Service) create(ctx context.Context, username, nickname, password string, superuser bool) (*model.User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	nickname = strings.TrimSpace(nickname)
	if !usernamePattern.MatchString(username) {
		return nil, fmt.Errorf("%w：用户名须为 3 到 32 位小写字母、数字、点、横线或下划线，且以字母开头", ErrInvalidUser)
	}
	if nickname == "" {
		nickname = username
	}
	if utf8.RuneCountInString(nickname) > 64 {
		return nil, fmt.Errorf("%w：用户昵称不能超过 64 个字符", ErrInvalidUser)
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("生成管理员密码摘要失败: %w", err)
	}
	now := time.Now().UTC()
	user := &model.User{
		ID: uuid.NewString(), Username: username, Nickname: nickname, PasswordHash: passwordHash,
		IsActive: true, IsSuperuser: superuser, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(user).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrUsernameExists
		}
		return nil, fmt.Errorf("创建用户失败: %w", err)
	}
	return user, nil
}

func (s *Service) List(ctx context.Context, limit, offset int) ([]model.User, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var users []model.User
	if err := s.db.WithContext(ctx).Order("username ASC").Limit(limit).Offset(offset).Find(&users).Error; err != nil {
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}
	return users, nil
}

func (s *Service) SetActive(ctx context.Context, userID string, active bool) error {
	var user model.User
	if err := s.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUserNotFound
		}
		return fmt.Errorf("查询待修改用户失败: %w", err)
	}
	if user.IsSuperuser {
		return ErrSuperuserImmutable
	}
	if err := s.db.WithContext(ctx).Model(&user).Updates(map[string]any{
		"is_active": active, "updated_at": time.Now().UTC(),
	}).Error; err != nil {
		return fmt.Errorf("修改用户状态失败: %w", err)
	}
	return nil
}

func (s *Service) FindByID(ctx context.Context, id string) (*model.User, error) {
	var user model.User
	if err := s.db.WithContext(ctx).First(&user, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *Service) FindByUsername(ctx context.Context, username string) (*model.User, error) {
	var user model.User
	if err := s.db.WithContext(ctx).First(&user, "username = ?", strings.ToLower(strings.TrimSpace(username))).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *Service) MarkLogin(ctx context.Context, userID string, at time.Time) error {
	return s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).
		Updates(map[string]any{"last_login_at": at, "updated_at": at}).Error
}

// ResetPassword 由离线管理员命令调用，同时重新启用完成身份确认的账户。
func (s *Service) ResetPassword(ctx context.Context, username, password string) (*model.User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) {
		return nil, ErrInvalidUser
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("生成用户密码摘要失败: %w", err)
	}
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&model.User{}).Where("username = ?", username).Updates(map[string]any{
		"password_hash": passwordHash,
		"is_active":     true,
		"auth_version":  gorm.Expr("auth_version + ?", 1),
		"updated_at":    now,
	})
	if result.Error != nil {
		return nil, fmt.Errorf("重置用户密码失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return nil, ErrUserNotFound
	}
	var user model.User
	if err := s.db.WithContext(ctx).First(&user, "username = ?", username).Error; err != nil {
		return nil, fmt.Errorf("读取密码重置结果失败: %w", err)
	}
	return &user, nil
}

// ChangePassword 只允许用户修改自己的本地密码。修改成功后递增认证版本，
// 使其他设备和当前设备的旧会话统一失效。
func (s *Service) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	if strings.TrimSpace(userID) == "" || currentPassword == "" {
		return ErrCurrentPassword
	}
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}

	var user model.User
	if err := s.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUserNotFound
		}
		return fmt.Errorf("读取当前用户失败: %w", err)
	}
	matched, err := auth.ComparePassword(currentPassword, user.PasswordHash)
	if err != nil {
		return fmt.Errorf("校验当前密码失败: %w", err)
	}
	if !matched {
		return ErrCurrentPassword
	}
	matched, err = auth.ComparePassword(newPassword, user.PasswordHash)
	if err != nil {
		return fmt.Errorf("校验新密码失败: %w", err)
	}
	if matched {
		return ErrPasswordUnchanged
	}
	passwordHash, err := auth.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("生成新密码摘要失败: %w", err)
	}
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&model.User{}).
		Where("id = ? AND auth_version = ?", user.ID, user.AuthVersion).
		Updates(map[string]any{
			"password_hash": passwordHash,
			"auth_version":  gorm.Expr("auth_version + ?", 1),
			"updated_at":    now,
		})
	if result.Error != nil {
		return fmt.Errorf("修改当前用户密码失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("用户信息已发生变化，请重新登录后再试")
	}
	return nil
}

func ValidatePassword(password string) error {
	length := utf8.RuneCountInString(password)
	if length < 12 {
		return fmt.Errorf("%w：密码至少需要 12 个字符", ErrInvalidPassword)
	}
	if length > 128 || len(password) > 512 {
		return fmt.Errorf("%w：密码不能超过 128 个字符", ErrInvalidPassword)
	}
	return nil
}
