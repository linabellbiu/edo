package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"zrt/internal/auth"
	"zrt/internal/config"
	"zrt/internal/configuration"
)

type settingsHandler struct {
	service      *configuration.Service
	loginLimiter *auth.LoginRateLimiter
	authConfig   config.Auth
	logger       *slog.Logger
}

func (h settingsHandler) loginLockout(c *gin.Context) {
	settings, err := h.service.GetLoginLockoutSettings(c.Request.Context())
	if err != nil {
		h.logger.Error("读取登录锁定设置失败", "operation", "settings_login_lockout_read", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, h.loginLockoutResponse(settings))
}

func (h settingsHandler) updateLoginLockout(c *gin.Context) {
	var request booleanSettingUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Enabled == nil {
		h.logger.Warn("修改登录锁定设置参数无效", "operation", "settings_login_lockout_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_settings", configuration.ErrInvalidConfiguration.Error())
		return
	}
	actor, _ := currentUser(c)
	settings, err := h.service.UpdateLoginLockoutSettings(
		c.Request.Context(), actor.ID, *request.Enabled, request.ExpectedVersion,
	)
	if err != nil {
		h.logger.Warn("修改登录锁定设置失败", "operation", "settings_login_lockout_update", "request_id", requestIDFrom(c), "err", err)
		switch {
		case errors.Is(err, configuration.ErrInvalidConfiguration):
			writeError(c, http.StatusBadRequest, "invalid_settings", configuration.ErrInvalidConfiguration.Error())
		case errors.Is(err, configuration.ErrVersionConflict):
			writeError(c, http.StatusConflict, "settings_version_conflict", configuration.ErrVersionConflict.Error())
		default:
			writeInternalError(c)
		}
		return
	}
	if h.loginLimiter != nil {
		if err := h.loginLimiter.ClearAll(c.Request.Context()); err != nil {
			h.logger.Error("清理登录失败计数失败", "operation", "settings_login_lockout_clear", "request_id", requestIDFrom(c), "err", err)
		}
	}
	c.JSON(http.StatusOK, h.loginLockoutResponse(settings))
}

func (h settingsHandler) loginLockoutResponse(settings configuration.LoginLockoutSettings) gin.H {
	return gin.H{
		"enabled": settings.Enabled, "version": settings.Version,
		"max_failures":   h.authConfig.LoginMaxFailure,
		"window_seconds": int(h.authConfig.LoginWindow.Seconds()),
	}
}

type booleanSettingUpdateRequest struct {
	Enabled         *bool `json:"enabled" binding:"required"`
	ExpectedVersion int   `json:"expected_version" binding:"min=0"`
}

func (h settingsHandler) externalGitWebhook(c *gin.Context) {
	settings, err := h.service.GetExternalGitWebhookSettings(c.Request.Context())
	if err != nil {
		h.logger.Error("读取外部 Git Webhook 设置失败", "operation", "settings_git_webhook_read", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, externalGitWebhookResponse(settings))
}

func (h settingsHandler) updateExternalGitWebhook(c *gin.Context) {
	var request booleanSettingUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Enabled == nil {
		h.logger.Warn("修改外部 Git Webhook 设置参数无效", "operation", "settings_git_webhook_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_settings", configuration.ErrInvalidConfiguration.Error())
		return
	}
	actor, _ := currentUser(c)
	settings, err := h.service.UpdateExternalGitWebhookSettings(
		c.Request.Context(), actor.ID, *request.Enabled, request.ExpectedVersion,
	)
	if err != nil {
		h.logger.Warn("修改外部 Git Webhook 设置失败", "operation", "settings_git_webhook_update", "request_id", requestIDFrom(c), "err", err)
		switch {
		case errors.Is(err, configuration.ErrInvalidConfiguration):
			writeError(c, http.StatusBadRequest, "invalid_settings", configuration.ErrInvalidConfiguration.Error())
		case errors.Is(err, configuration.ErrVersionConflict):
			writeError(c, http.StatusConflict, "settings_version_conflict", configuration.ErrVersionConflict.Error())
		default:
			writeInternalError(c)
		}
		return
	}
	c.JSON(http.StatusOK, externalGitWebhookResponse(settings))
}

func externalGitWebhookResponse(settings configuration.ExternalGitWebhookSettings) gin.H {
	return gin.H{
		"enabled":        settings.Enabled,
		"version":        settings.Version,
		"path_template":  "/api/v1/webhooks/git/{repository_id}",
		"max_body_bytes": maxWebhookBodyBytes,
		"providers":      []string{"generic", "github", "gitlab", "gitea", "gitee"},
		"events":         []string{"branch_push", "tag_push", "pull_request"},
	}
}
