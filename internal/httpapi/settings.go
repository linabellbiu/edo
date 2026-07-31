package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"edo/internal/auth"
	"edo/internal/config"
	"edo/internal/configuration"
	"edo/internal/database"
	"edo/internal/logging"
	"edo/internal/logretention"
)

type settingsHandler struct {
	service      *configuration.Service
	loginLimiter *auth.LoginRateLimiter
	authConfig   config.Auth
	retention    *logretention.Service
	migration    *database.TransferService
	runtimeLogs  *logging.RuntimeController
	logger       *slog.Logger
}

type databaseMigrationRequest struct {
	Driver       string `json:"driver" binding:"required,oneof=mysql postgres"`
	DSN          string `json:"dsn" binding:"required,max=4096"`
	TestToken    string `json:"test_token"`
	Confirmation string `json:"confirmation"`
}

func (h settingsHandler) databaseMigrationStatus(c *gin.Context) {
	if h.migration == nil {
		h.logger.Error("数据库迁移服务未初始化", "operation", "database_transfer_status", "request_id", requestIDFrom(c))
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, h.migration.Status())
}

func (h settingsHandler) testDatabaseMigration(c *gin.Context) {
	var request databaseMigrationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("目标数据库测试参数无效", "operation", "database_transfer_test_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_database_target", database.ErrInvalidTarget.Error())
		return
	}
	result, err := h.migration.TestTarget(c.Request.Context(), database.TransferTarget{Driver: request.Driver, DSN: request.DSN})
	if err != nil {
		h.logger.Warn("测试目标数据库失败", "operation", "database_transfer_test", "request_id", requestIDFrom(c), "driver", request.Driver, "err", err)
		switch {
		case errors.Is(err, database.ErrTransferUnsupported):
			writeError(c, http.StatusConflict, "database_transfer_unsupported", database.ErrTransferUnsupported.Error())
		case errors.Is(err, database.ErrTargetNotEmpty):
			writeError(c, http.StatusConflict, "database_target_not_empty", database.ErrTargetNotEmpty.Error())
		case errors.Is(err, database.ErrInvalidTarget):
			writeError(c, http.StatusBadRequest, "invalid_database_target", database.ErrInvalidTarget.Error())
		default:
			writeError(c, http.StatusBadGateway, "database_target_unavailable", "无法连接目标数据库，请检查地址、账号、密码和网络")
		}
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h settingsHandler) startDatabaseMigration(c *gin.Context) {
	var request databaseMigrationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("启动数据库迁移参数无效", "operation", "database_transfer_start_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_database_target", database.ErrInvalidTarget.Error())
		return
	}
	status, err := h.migration.Start(
		database.TransferTarget{Driver: request.Driver, DSN: request.DSN},
		request.TestToken,
		request.Confirmation,
	)
	if err != nil {
		h.logger.Warn("启动数据库迁移失败", "operation", "database_transfer_start", "request_id", requestIDFrom(c), "driver", request.Driver, "err", err)
		switch {
		case errors.Is(err, database.ErrTransferUnsupported), errors.Is(err, database.ErrActiveJobs), errors.Is(err, database.ErrTransferRunning):
			writeError(c, http.StatusConflict, "database_transfer_unavailable", err.Error())
		case errors.Is(err, database.ErrTargetTestRequired):
			writeError(c, http.StatusPreconditionFailed, "database_target_test_required", err.Error())
		case errors.Is(err, database.ErrInvalidTarget):
			writeError(c, http.StatusBadRequest, "invalid_database_target", database.ErrInvalidTarget.Error())
		default:
			writeInternalError(c)
		}
		return
	}
	c.JSON(http.StatusAccepted, status)
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

type logRetentionUpdateRequest struct {
	Enabled         *bool `json:"enabled" binding:"required"`
	PipelineLogDays int   `json:"pipeline_log_days" binding:"required,min=1,max=3650"`
	AuditLogDays    int   `json:"audit_log_days" binding:"required,min=1,max=3650"`
	ExpectedVersion int   `json:"expected_version" binding:"min=0"`
}

type runtimeLoggingUpdateRequest struct {
	Level             string `json:"level" binding:"required,oneof=debug info warn error"`
	HTTPAccessEnabled *bool  `json:"http_access_enabled" binding:"required"`
	ExpectedVersion   int    `json:"expected_version" binding:"min=0"`
}

func (h settingsHandler) runtimeLogging(c *gin.Context) {
	defaultLevel, defaultHTTPAccess := "info", true
	if h.runtimeLogs != nil {
		defaultLevel = h.runtimeLogs.Level()
		defaultHTTPAccess = h.runtimeLogs.HTTPAccessEnabled()
	}
	settings, err := h.service.GetRuntimeLoggingSettings(c.Request.Context(), defaultLevel, defaultHTTPAccess)
	if err != nil {
		h.logger.Error("读取运行日志设置失败", "operation", "settings_runtime_logging_read", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, settings)
}

func (h settingsHandler) updateRuntimeLogging(c *gin.Context) {
	var request runtimeLoggingUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.HTTPAccessEnabled == nil {
		h.logger.Warn("修改运行日志设置参数无效", "operation", "settings_runtime_logging_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_settings", "日志级别或输出选项无效")
		return
	}
	actor, _ := currentUser(c)
	settings, err := h.service.UpdateRuntimeLoggingSettings(
		c.Request.Context(), actor.ID, request.Level, *request.HTTPAccessEnabled, request.ExpectedVersion,
	)
	if err != nil {
		h.logger.Warn("修改运行日志设置失败", "operation", "settings_runtime_logging_update", "request_id", requestIDFrom(c), "user_id", actor.ID, "err", err)
		switch {
		case errors.Is(err, configuration.ErrInvalidConfiguration):
			writeError(c, http.StatusBadRequest, "invalid_settings", "日志级别或输出选项无效")
		case errors.Is(err, configuration.ErrVersionConflict):
			writeError(c, http.StatusConflict, "settings_version_conflict", configuration.ErrVersionConflict.Error())
		default:
			writeInternalError(c)
		}
		return
	}
	if h.runtimeLogs != nil {
		if err := h.runtimeLogs.Apply(settings.Level, settings.HTTPAccessEnabled); err != nil {
			h.logger.Error("热更新运行日志设置失败", "operation", "settings_runtime_logging_apply", "request_id", requestIDFrom(c), "user_id", actor.ID, "err", err)
			writeInternalError(c)
			return
		}
	}
	c.JSON(http.StatusOK, settings)
}

func (h settingsHandler) logRetention(c *gin.Context) {
	settings, err := h.service.GetLogRetentionSettings(c.Request.Context())
	if err != nil {
		h.logger.Error("读取日志保留设置失败", "operation", "settings_log_retention_read", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, settings)
}

func (h settingsHandler) updateLogRetention(c *gin.Context) {
	var request logRetentionUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Enabled == nil {
		h.logger.Warn("修改日志保留设置参数无效", "operation", "settings_log_retention_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_settings", "日志保留时间必须在 1 到 3650 天之间")
		return
	}
	actor, _ := currentUser(c)
	settings, err := h.service.UpdateLogRetentionSettings(
		c.Request.Context(), actor.ID, *request.Enabled,
		request.PipelineLogDays, request.AuditLogDays, request.ExpectedVersion,
	)
	if err != nil {
		h.logger.Warn("修改日志保留设置失败", "operation", "settings_log_retention_update", "request_id", requestIDFrom(c), "user_id", actor.ID, "err", err)
		switch {
		case errors.Is(err, configuration.ErrInvalidConfiguration):
			writeError(c, http.StatusBadRequest, "invalid_settings", "日志保留设置无效")
		case errors.Is(err, configuration.ErrVersionConflict):
			writeError(c, http.StatusConflict, "settings_version_conflict", configuration.ErrVersionConflict.Error())
		default:
			writeInternalError(c)
		}
		return
	}
	c.JSON(http.StatusOK, settings)
}

func (h settingsHandler) cleanupLogs(c *gin.Context) {
	if h.retention == nil {
		h.logger.Error("日志保留服务未初始化", "operation", "settings_log_cleanup", "request_id", requestIDFrom(c))
		writeInternalError(c)
		return
	}
	report, err := h.retention.Cleanup(c.Request.Context())
	if err != nil {
		h.logger.Error("手动清理过期日志失败", "operation", "settings_log_cleanup", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	if !report.Enabled {
		writeError(c, http.StatusConflict, "log_retention_disabled", "请先启用自动日志清理并保存设置")
		return
	}
	c.JSON(http.StatusOK, report)
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
