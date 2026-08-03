package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/account"
	"edo/internal/auth"
	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
)

const currentUserKey = "current_user"

type authHandler struct {
	login    *account.LoginService
	accounts *account.Service
	sessions *auth.SessionStore
	access   *access.Service
	config   config.Auth
	logger   *slog.Logger
}

type loginRequest struct {
	Username string `json:"username" binding:"required,max=32"`
	Password string `json:"password" binding:"required,max=512"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required,max=512"`
	NewPassword     string `json:"new_password" binding:"required,max=512"`
}

type userResponse struct {
	ID             string     `json:"id"`
	Username       string     `json:"username"`
	Nickname       string     `json:"nickname"`
	DepartmentID   string     `json:"department_id"`
	DepartmentName string     `json:"department_name,omitempty"`
	IsSuperuser    bool       `json:"is_superuser"`
	LastLoginAt    *time.Time `json:"last_login_at,omitempty"`
	Permissions    []string   `json:"permissions"`
}

func (h authHandler) handleLogin(c *gin.Context) {
	var request loginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("登录请求参数无效", "operation", "auth_login_bind", "request_id", requestIDFrom(c), "err", err)
		c.JSON(http.StatusBadRequest, errorResponse{
			Code: "invalid_request", Message: "请输入有效的用户名和密码", RequestID: requestIDFrom(c),
		})
		return
	}
	result, err := h.login.Login(c.Request.Context(), request.Username, request.Password, c.ClientIP())
	if err != nil {
		h.writeLoginError(c, err)
		return
	}
	permissions, err := h.access.UserPermissions(c.Request.Context(), result.User)
	if err != nil {
		h.logger.Error("登录后读取用户权限失败", "operation", "auth_login_permissions", "request_id", requestIDFrom(c), "user_id", result.User.ID, "err", err)
		if deleteErr := h.sessions.Delete(c.Request.Context(), result.Token); deleteErr != nil {
			h.logger.Error("读取权限失败后清理登录会话失败", "operation", "auth_login_cleanup", "request_id", requestIDFrom(c), "user_id", result.User.ID, "err", deleteErr)
		}
		writeInternalError(c)
		return
	}
	h.setSessionCookie(c, result.Token, result.ExpiresAt)
	c.Set(currentUserKey, result.User)
	response := toUserResponse(result.User)
	response.Permissions = permissions
	c.JSON(http.StatusOK, gin.H{"user": response})
}

func (h authHandler) handleLogout(c *gin.Context) {
	token, cookieErr := c.Cookie(h.config.CookieName)
	if cookieErr == nil {
		if err := h.sessions.Delete(c.Request.Context(), token); err != nil {
			h.logger.Error("退出登录时删除会话失败", "operation", "auth_logout_session", "request_id", requestIDFrom(c), "err", err)
			c.JSON(http.StatusInternalServerError, errorResponse{
				Code: "logout_failed", Message: "退出登录失败，请稍后重试", RequestID: requestIDFrom(c),
			})
			return
		}
	}
	h.clearSessionCookie(c)
	c.Status(http.StatusNoContent)
}

func (h authHandler) handleMe(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		h.logger.Error("鉴权中间件未注入用户", "operation", "auth_me_context", "request_id", requestIDFrom(c))
		c.JSON(http.StatusInternalServerError, errorResponse{
			Code: "internal_error", Message: "服务暂时不可用，请稍后重试", RequestID: requestIDFrom(c),
		})
		return
	}
	permissions, err := h.access.UserPermissions(c.Request.Context(), user)
	if err != nil {
		h.logger.Error("读取当前用户权限失败", "operation", "auth_me_permissions", "request_id", requestIDFrom(c), "user_id", user.ID, "err", err)
		writeInternalError(c)
		return
	}
	response := toUserResponse(user)
	response.Permissions = permissions
	c.JSON(http.StatusOK, gin.H{"user": response})
}

func (h authHandler) handleChangePassword(c *gin.Context) {
	var request changePasswordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("修改密码请求参数无效", "operation", "auth_password_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_password_request", "请输入当前密码和新密码")
		return
	}
	user, ok := currentUser(c)
	if !ok {
		h.logger.Error("修改密码时缺少当前用户", "operation", "auth_password_context", "request_id", requestIDFrom(c))
		writeInternalError(c)
		return
	}
	if err := h.accounts.ChangePassword(c.Request.Context(), user.ID, request.CurrentPassword, request.NewPassword); err != nil {
		h.logger.Warn("修改当前用户密码失败", "operation", "auth_password_change", "request_id", requestIDFrom(c), "user_id", user.ID, "err", err)
		switch {
		case errors.Is(err, account.ErrCurrentPassword):
			writeError(c, http.StatusBadRequest, "current_password_incorrect", account.ErrCurrentPassword.Error())
		case errors.Is(err, account.ErrInvalidPassword), errors.Is(err, account.ErrPasswordUnchanged):
			writeError(c, http.StatusBadRequest, "invalid_new_password", err.Error())
		case errors.Is(err, account.ErrUserNotFound):
			unauthorized(c)
		default:
			writeInternalError(c)
		}
		return
	}
	if token, err := c.Cookie(h.config.CookieName); err == nil {
		if err := h.sessions.Delete(c.Request.Context(), token); err != nil {
			h.logger.Error("修改密码后删除当前会话失败", "operation", "auth_password_session_delete", "request_id", requestIDFrom(c), "user_id", user.ID, "err", err)
		}
	}
	h.clearSessionCookie(c)
	c.JSON(http.StatusOK, gin.H{"message": "密码已修改，请重新登录", "reauthentication_required": true})
}

func (h authHandler) writeLoginError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	code := "login_unavailable"
	message := account.ErrLoginUnavailable.Error()
	switch {
	case errors.Is(err, account.ErrInvalidCredentials):
		status, code, message = http.StatusUnauthorized, "invalid_credentials", err.Error()
	case errors.Is(err, account.ErrAccountDisabled):
		status, code, message = http.StatusForbidden, "account_disabled", err.Error()
	case errors.Is(err, account.ErrTooManyAttempts):
		status, code, message = http.StatusTooManyRequests, "too_many_attempts", err.Error()
		c.Header("Retry-After", strconv.Itoa(int(h.config.LoginWindow.Seconds())))
	}
	c.JSON(status, errorResponse{Code: code, Message: message, RequestID: requestIDFrom(c)})
}

func (h authHandler) setSessionCookie(c *gin.Context, token string, expiresAt time.Time) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: h.config.CookieName, Value: token, Path: "/", Expires: expiresAt,
		MaxAge: int(time.Until(expiresAt).Seconds()), HttpOnly: true, Secure: h.config.CookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (h authHandler) clearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: h.config.CookieName, Value: "", Path: "/", Expires: time.Unix(1, 0),
		MaxAge: -1, HttpOnly: true, Secure: h.config.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

func requireAuth(
	accounts *account.Service,
	sessions *auth.SessionStore,
	logger *slog.Logger,
	cookieName string,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(cookieName)
		if err != nil {
			logger.Debug("请求缺少登录会话", "operation", "auth_required", "request_id", requestIDFrom(c), "path", c.Request.URL.Path)
			unauthorized(c)
			return
		}
		session, err := sessions.Get(c.Request.Context(), token)
		if errors.Is(err, auth.ErrSessionNotFound) {
			logger.Warn("请求使用无效登录会话", "operation", "auth_session_invalid", "request_id", requestIDFrom(c), "path", c.Request.URL.Path)
			unauthorized(c)
			return
		}
		if err != nil {
			logger.Error("读取请求登录会话失败", "operation", "auth_session_get", "request_id", requestIDFrom(c), "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, errorResponse{
				Code: "auth_unavailable", Message: "登录服务暂时不可用，请稍后重试", RequestID: requestIDFrom(c),
			})
			return
		}
		user, err := accounts.FindByID(c.Request.Context(), session.UserID)
		if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && (!user.IsActive || session.AuthVersion != user.AuthVersion)) {
			logger.Warn("登录会话对应账户不可用", "operation", "auth_user_invalid", "request_id", requestIDFrom(c), "user_id", session.UserID)
			_ = sessions.Delete(c.Request.Context(), token)
			unauthorized(c)
			return
		}
		if err != nil {
			logger.Error("读取登录用户失败", "operation", "auth_user_get", "request_id", requestIDFrom(c), "user_id", session.UserID, "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, errorResponse{
				Code: "auth_unavailable", Message: "登录服务暂时不可用，请稍后重试", RequestID: requestIDFrom(c),
			})
			return
		}
		c.Set(currentUserKey, user)
		c.Request = c.Request.WithContext(database.WithDepartmentScope(c.Request.Context(), database.DepartmentScope{
			UserID: user.ID, DepartmentID: user.DepartmentID, AllDepartments: user.IsSuperuser,
		}))
		c.Next()
	}
}

func requirePermission(accessService *access.Service, logger *slog.Logger, permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := currentUser(c)
		if !ok {
			logger.Error("权限校验缺少当前用户", "operation", "permission_context", "request_id", requestIDFrom(c), "permission", permission)
			writeInternalError(c)
			c.Abort()
			return
		}
		allowed, err := accessService.HasPermission(c.Request.Context(), user, permission)
		if err != nil {
			logger.Error("查询用户权限失败", "operation", "permission_check", "request_id", requestIDFrom(c), "user_id", user.ID, "permission", permission, "err", err)
			writeInternalError(c)
			c.Abort()
			return
		}
		if !allowed {
			logger.Warn("用户权限不足", "operation", "permission_denied", "request_id", requestIDFrom(c), "user_id", user.ID, "permission", permission)
			c.AbortWithStatusJSON(http.StatusForbidden, errorResponse{
				Code: "permission_denied", Message: "没有执行此操作的权限", RequestID: requestIDFrom(c),
			})
			return
		}
		c.Next()
	}
}

func requireAnyPermission(accessService *access.Service, logger *slog.Logger, permissions ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := currentUser(c)
		if !ok {
			logger.Error("权限校验缺少当前用户", "operation", "permission_any_context", "request_id", requestIDFrom(c))
			writeInternalError(c)
			c.Abort()
			return
		}
		for _, permission := range permissions {
			allowed, err := accessService.HasPermission(c.Request.Context(), user, permission)
			if err != nil {
				logger.Error("查询用户权限失败", "operation", "permission_any_check", "request_id", requestIDFrom(c), "user_id", user.ID, "permission", permission, "err", err)
				writeInternalError(c)
				c.Abort()
				return
			}
			if allowed {
				c.Next()
				return
			}
		}
		logger.Warn("用户缺少任一所需权限", "operation", "permission_any_denied", "request_id", requestIDFrom(c), "user_id", user.ID, "permissions", permissions)
		c.AbortWithStatusJSON(http.StatusForbidden, errorResponse{
			Code: "permission_denied", Message: "没有执行此操作的权限", RequestID: requestIDFrom(c),
		})
	}
}

func requireSuperuser(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := currentUser(c)
		if !ok {
			logger.Error("超级管理员校验缺少当前用户", "operation", "superuser_context", "request_id", requestIDFrom(c))
			writeInternalError(c)
			c.Abort()
			return
		}
		if !user.IsSuperuser {
			logger.Warn("非超级管理员访问受限操作", "operation", "superuser_denied", "request_id", requestIDFrom(c), "user_id", user.ID, "path", c.Request.URL.Path)
			c.AbortWithStatusJSON(http.StatusForbidden, errorResponse{
				Code: "superuser_required", Message: "该操作仅允许超级管理员执行", RequestID: requestIDFrom(c),
			})
			return
		}
		c.Next()
	}
}

func unauthorized(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, errorResponse{
		Code: "authentication_required", Message: "请先登录", RequestID: requestIDFrom(c),
	})
}

func currentUser(c *gin.Context) (*model.User, bool) {
	value, exists := c.Get(currentUserKey)
	if !exists {
		return nil, false
	}
	user, ok := value.(*model.User)
	return user, ok
}

func toUserResponse(user *model.User) userResponse {
	return userResponse{
		ID: user.ID, Username: user.Username, Nickname: user.Nickname,
		DepartmentID: user.DepartmentID, IsSuperuser: user.IsSuperuser,
		LastLoginAt: user.LastLoginAt, Permissions: []string{},
	}
}
