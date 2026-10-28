package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"edo/internal/account"
	"edo/internal/audit"
	"edo/internal/identity"
	"edo/internal/model"
	"edo/internal/secret"
)

type identityHandler struct {
	service *identity.Service
	auth    authHandler
	audits  *audit.Service
	logger  *slog.Logger
}

type ldapLoginRequest struct {
	Username string `json:"username" binding:"required,max=255"`
	Password string `json:"password" binding:"required,max=512"`
}

type providerStatusRequest struct {
	IsActive *bool `json:"is_active" binding:"required"`
}

type emailLoginRequest struct {
	Email string `json:"email" binding:"required,max=254"`
	Code  string `json:"code" binding:"required,len=6,numeric"`
}

func (h identityHandler) sendEmailCode(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var request struct {
		Email string `json:"email" binding:"required,max=254"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("邮箱验证码请求无效", "operation", "auth_email_code_validate", "request_id", requestIDFrom(c))
		writeError(c, http.StatusBadRequest, "invalid_request", "请输入有效的邮箱地址")
		return
	}
	if err := h.service.SendEmailCode(c.Request.Context(), c.Param("id"), request.Email, c.ClientIP()); err != nil {
		h.writeLoginError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "若该邮箱符合登录条件，验证码将发送到你的邮箱，5 分钟内有效", "retry_after": 60})
}

func (h identityHandler) loginEmail(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var request emailLoginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("邮箱登录请求无效", "operation", "auth_email_login_validate", "request_id", requestIDFrom(c))
		writeError(c, http.StatusBadRequest, "invalid_request", "请输入邮箱和六位验证码")
		return
	}
	result, err := h.service.LoginEmail(c.Request.Context(), c.Param("id"), request.Email, request.Code, c.ClientIP())
	if err != nil {
		h.writeLoginError(c, err)
		return
	}
	permissions, err := h.auth.access.UserPermissions(c.Request.Context(), result.User)
	if err != nil {
		_ = h.auth.sessions.Delete(c.Request.Context(), result.Token)
		h.logger.Error("读取邮箱登录用户权限失败", "operation", "auth_email_permissions", "user_id", result.User.ID, "err", err)
		writeInternalError(c)
		return
	}
	h.auth.setSessionCookie(c, result.Token, result.ExpiresAt)
	c.Set(currentUserKey, result.User)
	response := toUserResponse(result.User)
	response.Permissions = permissions
	c.JSON(http.StatusOK, gin.H{"user": response})
}

func (h identityHandler) listPublic(c *gin.Context) {
	providers, err := h.service.ListPublic(c.Request.Context())
	if err != nil {
		h.logger.Error("读取可用登录方式失败", "operation", "identity_public_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"providers": providers})
}

func (h identityHandler) list(c *gin.Context) {
	providers, err := h.service.List(c.Request.Context())
	if err != nil {
		h.logger.Error("读取登录方式失败", "operation", "identity_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"providers": providers})
}

func (h identityHandler) roleChoices(c *gin.Context) {
	actor, _ := currentUser(c)
	roles, err := h.service.RoleChoices(c.Request.Context(), actor)
	if err != nil {
		h.writeProviderError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"roles": roles})
}

func (h identityHandler) create(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	var request identity.ProviderInput
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("登录方式配置请求无效", "operation", "identity_provider_create_validate", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", "请检查登录方式配置")
		return
	}
	user, _ := currentUser(c)
	provider, err := h.service.CreateAs(c.Request.Context(), user, request)
	if err != nil {
		h.writeProviderError(c, err)
		return
	}
	setAuditResourceID(c, provider.ID)
	c.JSON(http.StatusCreated, gin.H{"provider": provider})
}

func (h identityHandler) update(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	var request identity.ProviderInput
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("登录方式配置请求无效", "operation", "identity_provider_update_validate", "provider_id", c.Param("id"), "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", "请检查登录方式配置")
		return
	}
	actor, _ := currentUser(c)
	provider, err := h.service.UpdateAs(c.Request.Context(), actor, c.Param("id"), request)
	if err != nil {
		h.writeProviderError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"provider": provider})
}

func (h identityHandler) setStatus(c *gin.Context) {
	var request providerStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.IsActive == nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "请明确是否启用")
		return
	}
	actor, _ := currentUser(c)
	if err := h.service.SetActiveAs(c.Request.Context(), actor, c.Param("id"), *request.IsActive); err != nil {
		h.writeProviderError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h identityHandler) loginLDAP(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8*1024)
	var request ldapLoginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "请输入用户名和密码")
		return
	}
	result, err := h.service.LoginLDAP(c.Request.Context(), c.Param("id"), request.Username, request.Password, c.ClientIP())
	if err != nil {
		h.writeLoginError(c, err)
		return
	}
	permissions, err := h.auth.access.UserPermissions(c.Request.Context(), result.User)
	if err != nil {
		_ = h.auth.sessions.Delete(c.Request.Context(), result.Token)
		writeInternalError(c)
		return
	}
	h.auth.setSessionCookie(c, result.Token, result.ExpiresAt)
	c.Set(currentUserKey, result.User)
	response := toUserResponse(result.User)
	response.Permissions = permissions
	c.JSON(http.StatusOK, gin.H{"user": response})
}

func (h identityHandler) startOAuth(c *gin.Context) {
	location, err := h.service.StartOAuth(c.Request.Context(), c.Param("id"), c.Query("return_to"), c.ClientIP())
	if err != nil {
		h.writeLoginError(c, err)
		return
	}
	parsed, err := url.Parse(location)
	if err != nil || parsed.Query().Get("state") == "" {
		h.logger.Error("生成外部登录跳转失败", "operation", "identity_oauth_start", "provider_id", c.Param("id"), "err", err)
		writeInternalError(c)
		return
	}
	state := parsed.Query().Get("state")
	http.SetCookie(c.Writer, &http.Cookie{Name: oauthCookieName(state), Value: state, Path: "/api/v1/auth/oauth", MaxAge: 600, HttpOnly: true, Secure: h.auth.config.CookieSecure, SameSite: http.SameSiteLaxMode})
	c.Redirect(http.StatusFound, location)
}

func (h identityHandler) callbackOAuth(c *gin.Context) {
	state := c.Query("state")
	cookie, cookieErr := c.Cookie(oauthCookieName(state))
	if cookieErr != nil || len(state) < 32 || len(state) > 128 || subtle.ConstantTimeCompare([]byte(cookie), []byte(state)) != 1 {
		h.logger.Warn("外部登录浏览器状态不匹配", "operation", "identity_oauth_browser_state", "provider_id", c.Param("id"), "request_id", requestIDFrom(c))
		h.recordOAuthAudit(c, "", model.AuditFailed, "expired")
		c.Redirect(http.StatusFound, externalLoginErrorURL("/", "expired"))
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: oauthCookieName(state), Value: "", Path: "/api/v1/auth/oauth", MaxAge: -1, HttpOnly: true, Secure: h.auth.config.CookieSecure, SameSite: http.SameSiteLaxMode})
	providerError := c.Query("error")
	codeValue := c.Query("code")
	if codeValue == "" {
		codeValue = c.Query("authCode")
	}
	if providerError != "" {
		codeValue = ""
	}
	result, returnTo, err := h.service.CompleteOAuth(c.Request.Context(), c.Param("id"), c.Query("state"), codeValue, c.ClientIP())
	if err != nil || providerError != "" {
		h.logger.Warn("外部登录回调失败", "operation", "identity_oauth_callback", "provider_id", c.Param("id"), "request_id", requestIDFrom(c), "err", err)
		code := "failed"
		if errors.Is(err, identity.ErrInvalidState) {
			code = "expired"
		} else if errors.Is(err, identity.ErrProvisioningDisabled) {
			code = "not_bound"
		} else if errors.Is(err, identity.ErrUnverifiedEmail) {
			code = "email_unverified"
		} else if providerError != "" {
			code = "cancelled"
		}
		h.recordOAuthAudit(c, "", model.AuditFailed, code)
		c.Redirect(http.StatusFound, externalLoginErrorURL(returnTo, code))
		return
	}
	h.auth.setSessionCookie(c, result.Token, result.ExpiresAt)
	h.recordOAuthAudit(c, result.User.ID, model.AuditSucceeded, "")
	c.Redirect(http.StatusFound, returnTo)
}

func (h identityHandler) writeProviderError(c *gin.Context, err error) {
	h.logger.Warn("登录方式配置操作失败", "operation", "identity_provider_save", "provider_id", c.Param("id"), "request_id", requestIDFrom(c), "err", err)
	switch {
	case errors.Is(err, identity.ErrProviderAccessDenied):
		writeError(c, http.StatusForbidden, "identity_provider_permission_denied", identity.ErrProviderAccessDenied.Error())
	case errors.Is(err, identity.ErrInvalidProvider):
		c.JSON(http.StatusBadRequest, errorResponse{Code: "invalid_provider", Message: err.Error(), RequestID: requestIDFrom(c)})
	case errors.Is(err, identity.ErrProviderNotFound):
		c.JSON(http.StatusNotFound, errorResponse{Code: "provider_not_found", Message: err.Error(), RequestID: requestIDFrom(c)})
	case errors.Is(err, secret.ErrUnavailable):
		c.JSON(http.StatusServiceUnavailable, errorResponse{Code: "secret_unavailable", Message: "请先配置 EDO 密钥加密功能", RequestID: requestIDFrom(c)})
	default:
		h.logger.Error("保存登录方式失败", "operation", "identity_provider_save", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
	}
}

func (h identityHandler) writeLoginError(c *gin.Context, err error) {
	h.logger.Warn("登录方式验证失败", "operation", "identity_login", "provider_id", c.Param("id"), "request_id", requestIDFrom(c), "err", err)
	switch {
	case errors.Is(err, identity.ErrEmailRateLimited):
		c.Header("Retry-After", "60")
		writeError(c, http.StatusTooManyRequests, "email_rate_limited", identity.ErrEmailRateLimited.Error())
	case errors.Is(err, identity.ErrInvalidEmail):
		writeError(c, http.StatusBadRequest, "invalid_email", identity.ErrInvalidEmail.Error())
	case errors.Is(err, identity.ErrInvalidEmailCode):
		writeError(c, http.StatusUnauthorized, "invalid_email_code", identity.ErrInvalidEmailCode.Error())
	case errors.Is(err, identity.ErrEmailUnavailable):
		writeError(c, http.StatusServiceUnavailable, "email_unavailable", identity.ErrEmailUnavailable.Error())
	case errors.Is(err, identity.ErrInvalidCredentials), errors.Is(err, account.ErrInvalidCredentials):
		c.JSON(http.StatusUnauthorized, errorResponse{Code: "invalid_credentials", Message: "用户名或密码错误", RequestID: requestIDFrom(c)})
	case errors.Is(err, account.ErrTooManyAttempts):
		h.auth.writeLoginError(c, err)
	case errors.Is(err, account.ErrAccountDisabled), errors.Is(err, identity.ErrProvisioningDisabled), errors.Is(err, identity.ErrProviderDisabled):
		c.JSON(http.StatusForbidden, errorResponse{Code: "login_forbidden", Message: err.Error(), RequestID: requestIDFrom(c)})
	case errors.Is(err, identity.ErrProviderNotFound):
		c.JSON(http.StatusNotFound, errorResponse{Code: "provider_not_found", Message: err.Error(), RequestID: requestIDFrom(c)})
	default:
		h.logger.Error("外部登录失败", "operation", "identity_login", "request_id", requestIDFrom(c), "err", err)
		c.JSON(http.StatusServiceUnavailable, errorResponse{Code: "external_login_unavailable", Message: "外部登录暂时不可用，请稍后重试", RequestID: requestIDFrom(c)})
	}
}

func (h identityHandler) recordOAuthAudit(c *gin.Context, actorID string, result model.AuditResult, reason string) {
	if err := h.audits.Record(c.Request.Context(), audit.RecordInput{
		ActorUserID: actorID, Action: "auth.oauth.login", ResourceType: "session", ResourceID: c.Param("id"),
		Result: result, RequestID: requestIDFrom(c), ClientIP: c.ClientIP(), UserAgent: truncateText(c.Request.UserAgent(), 512),
		Metadata: map[string]any{"provider_id": c.Param("id"), "reason": reason},
	}); err != nil {
		h.logger.Error("记录 OAuth 登录审计失败", "operation", "identity_oauth_audit", "request_id", requestIDFrom(c), "err", err)
	}
}

func externalLoginErrorURL(returnTo, code string) string {
	query := url.Values{"external_error": []string{code}}
	if strings.HasPrefix(returnTo, "/") && !strings.HasPrefix(returnTo, "//") {
		query.Set("redirect", returnTo)
	}
	return "/login?" + query.Encode()
}

func oauthCookieName(state string) string {
	digest := sha256.Sum256([]byte(state))
	return "edo_oauth_" + hex.EncodeToString(digest[:8])
}
