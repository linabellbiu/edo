package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"edo/internal/auth"
	"edo/internal/model"
)

var (
	ErrInvalidCredentials = errors.New("用户名或密码错误")
	ErrAccountDisabled    = errors.New("账户已被禁用，请联系管理员")
	ErrTooManyAttempts    = errors.New("登录失败次数过多，请稍后重试")
	ErrLoginUnavailable   = errors.New("登录服务暂时不可用，请稍后重试")
)

type LoginResult struct {
	User      *model.User
	Token     string
	ExpiresAt time.Time
}

type LoginService struct {
	accounts  *Service
	sessions  *auth.SessionStore
	limiter   *auth.LoginRateLimiter
	logger    *slog.Logger
	dummyHash string
}

func NewLoginService(
	accounts *Service,
	sessions *auth.SessionStore,
	limiter *auth.LoginRateLimiter,
	logger *slog.Logger,
) (*LoginService, error) {
	dummyHash, err := auth.HashPassword("edo-invalid-password-placeholder")
	if err != nil {
		return nil, err
	}
	return &LoginService{
		accounts: accounts, sessions: sessions, limiter: limiter, logger: logger, dummyHash: dummyHash,
	}, nil
}

func (s *LoginService) Login(ctx context.Context, username, password, clientIP string) (LoginResult, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	blocked, retryAfter, err := s.limiter.Blocked(ctx, username, clientIP)
	if err != nil {
		s.logger.Error("读取登录限流状态失败", "operation", "auth_login_rate_limit", "identity", loginIdentity(username, clientIP), "err", err)
		return LoginResult{}, ErrLoginUnavailable
	}
	if blocked {
		s.logger.Warn("登录请求被限流", "operation", "auth_login_blocked", "identity", loginIdentity(username, clientIP), "retry_after_seconds", int(retryAfter.Seconds()))
		return LoginResult{}, ErrTooManyAttempts
	}

	user, findErr := s.accounts.FindByUsername(ctx, username)
	if errors.Is(findErr, gorm.ErrRecordNotFound) {
		_, _ = auth.ComparePassword(password, s.dummyHash)
		return LoginResult{}, s.loginFailure(ctx, username, clientIP, "user_not_found")
	}
	if findErr != nil {
		s.logger.Error("查询登录用户失败", "operation", "auth_login_find_user", "identity", loginIdentity(username, clientIP), "err", findErr)
		return LoginResult{}, ErrLoginUnavailable
	}

	matched, compareErr := auth.ComparePassword(password, user.PasswordHash)
	if compareErr != nil {
		s.logger.Error("校验用户密码摘要失败", "operation", "auth_login_password", "user_id", user.ID, "err", compareErr)
		return LoginResult{}, ErrLoginUnavailable
	}
	if !matched {
		return LoginResult{}, s.loginFailure(ctx, username, clientIP, "password_mismatch")
	}
	if !user.IsActive {
		s.logger.Warn("已禁用账户尝试登录", "operation", "auth_login_disabled", "user_id", user.ID, "client_ip", clientIP)
		return LoginResult{}, ErrAccountDisabled
	}

	token, session, err := s.sessions.Create(ctx, user.ID, user.AuthVersion)
	if err != nil {
		s.logger.Error("创建登录会话失败", "operation", "auth_login_session", "user_id", user.ID, "err", err)
		return LoginResult{}, ErrLoginUnavailable
	}
	if err := s.limiter.Reset(ctx, username, clientIP); err != nil {
		s.logger.Error("清理登录限流状态失败", "operation", "auth_login_rate_reset", "user_id", user.ID, "err", err)
	}
	now := time.Now().UTC()
	if err := s.accounts.MarkLogin(ctx, user.ID, now); err != nil {
		s.logger.Error("更新用户最后登录时间失败", "operation", "auth_login_mark", "user_id", user.ID, "err", err)
	}
	user.LastLoginAt = &now
	s.logger.Info("用户登录成功", "operation", "auth_login", "user_id", user.ID, "client_ip", clientIP)
	return LoginResult{User: user, Token: token, ExpiresAt: session.ExpiresAt}, nil
}

// CreateSession 为已经由 LDAP 或 OAuth 验证过身份的用户创建同一类 EDO 会话。
func (s *LoginService) CreateSession(ctx context.Context, user *model.User, clientIP, method string) (LoginResult, error) {
	if user == nil || !user.IsActive {
		return LoginResult{}, ErrAccountDisabled
	}
	token, session, err := s.sessions.Create(ctx, user.ID, user.AuthVersion)
	if err != nil {
		s.logger.Error("创建外部登录会话失败", "operation", "auth_external_session", "user_id", user.ID, "method", method, "err", err)
		return LoginResult{}, ErrLoginUnavailable
	}
	now := time.Now().UTC()
	if err := s.accounts.MarkLogin(ctx, user.ID, now); err != nil {
		s.logger.Error("更新外部登录时间失败", "operation", "auth_external_mark", "user_id", user.ID, "method", method, "err", err)
	}
	user.LastLoginAt = &now
	s.logger.Info("外部身份登录成功", "operation", "auth_external_login", "user_id", user.ID, "method", method, "client_ip", clientIP)
	return LoginResult{User: user, Token: token, ExpiresAt: session.ExpiresAt}, nil
}

func (s *LoginService) loginFailure(ctx context.Context, username, clientIP, reason string) error {
	if err := s.limiter.RecordFailure(ctx, username, clientIP); err != nil {
		s.logger.Error("记录登录失败次数失败", "operation", "auth_login_failure_record", "identity", loginIdentity(username, clientIP), "err", err)
		return ErrLoginUnavailable
	}
	s.logger.Warn("用户登录失败", "operation", "auth_login_failed", "identity", loginIdentity(username, clientIP), "reason", reason)
	return ErrInvalidCredentials
}

func loginIdentity(username, clientIP string) string {
	digest := sha256.Sum256([]byte(username + "\x00" + clientIP))
	return hex.EncodeToString(digest[:8])
}
