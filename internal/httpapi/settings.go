package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	artifactmanager "edo/internal/artifact"
	"edo/internal/auth"
	"edo/internal/config"
	"edo/internal/configuration"
	"edo/internal/database"
	"edo/internal/logging"
	"edo/internal/logretention"
	"edo/internal/manageddirectory"
	"edo/internal/repository"
	"edo/internal/variablecatalog"
)

type settingsHandler struct {
	service      *configuration.Service
	loginLimiter *auth.LoginRateLimiter
	authConfig   config.Auth
	retention    *logretention.Service
	migration    *database.TransferService
	runtimeLogs  *logging.RuntimeController
	repositories *repository.Service
	artifacts    *artifactmanager.Service
	logger       *slog.Logger
}

type runtimeDirectoryUpdateRequest struct {
	WorkspaceDirectory     string `json:"workspace_directory" binding:"required,max=4096"`
	BuildDirectory         string `json:"build_directory" binding:"required,max=4096"`
	CacheDirectory         string `json:"cache_directory" binding:"required,max=4096"`
	LocalArtifactDirectory string `json:"local_artifact_directory" binding:"required,max=4096"`
	ExpectedVersion        int    `json:"expected_version" binding:"min=0"`
}

type directoryUsageResponse struct {
	Path  string `json:"path"`
	Files int64  `json:"files"`
	Bytes int64  `json:"bytes"`
}

type runtimeDirectoryResponse struct {
	configuration.RuntimeDirectorySettings
	WorkspaceUsage directoryUsageResponse `json:"workspace_usage"`
	BuildUsage     directoryUsageResponse `json:"build_usage"`
	CacheUsage     directoryUsageResponse `json:"cache_usage"`
	ArtifactUsage  directoryUsageResponse `json:"artifact_usage"`
}

type databaseMigrationRequest struct {
	Driver    string `json:"driver" binding:"required,oneof=mysql postgres"`
	Host      string `json:"host" binding:"max=255"`
	Port      int    `json:"port" binding:"min=0,max=65535"`
	Username  string `json:"username" binding:"max=256"`
	Password  string `json:"password" binding:"max=1024"`
	Database  string `json:"database" binding:"max=256"`
	DSN       string `json:"dsn" binding:"max=4096"`
	TestToken string `json:"test_token"`
}

func (h settingsHandler) builtinVariables(c *gin.Context) {
	c.JSON(http.StatusOK, variablecatalog.Snapshot())
}

func (r databaseMigrationRequest) transferTarget() (database.TransferTarget, error) {
	if r.DSN != "" {
		if r.Host != "" || r.Port != 0 || r.Username != "" || r.Password != "" || r.Database != "" {
			return database.TransferTarget{}, database.ErrInvalidTarget
		}
		return database.TransferTarget{Driver: r.Driver, DSN: r.DSN}, nil
	}
	return database.BuildTransferTarget(database.TransferConnection{
		Driver: r.Driver, Host: r.Host, Port: r.Port, Username: r.Username,
		Password: r.Password, Database: r.Database,
	})
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
	target, err := request.transferTarget()
	if err != nil {
		h.logger.Warn("目标数据库连接参数无效", "operation", "database_transfer_test_target", "request_id", requestIDFrom(c), "driver", request.Driver, "err", err)
		writeError(c, http.StatusBadRequest, "invalid_database_target", database.ErrInvalidTarget.Error())
		return
	}
	result, err := h.migration.TestTarget(c.Request.Context(), target)
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
	target, err := request.transferTarget()
	if err != nil {
		h.logger.Warn("目标数据库连接参数无效", "operation", "database_transfer_start_target", "request_id", requestIDFrom(c), "driver", request.Driver, "err", err)
		writeError(c, http.StatusBadRequest, "invalid_database_target", database.ErrInvalidTarget.Error())
		return
	}
	status, err := h.migration.Start(target, request.TestToken)
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
	FileEnabled       *bool  `json:"file_enabled" binding:"required"`
	FileDirectory     string `json:"file_directory" binding:"required,max=1024"`
	MaxFileSizeMB     int    `json:"max_file_size_mb" binding:"required,min=1,max=10240"`
	CompressAfterDays int    `json:"compress_after_days" binding:"required,min=1,max=3650"`
	ExpectedVersion   int    `json:"expected_version" binding:"min=0"`
}

func (h settingsHandler) runtimeLogging(c *gin.Context) {
	defaultLevel, defaultHTTPAccess := "info", true
	fileDefaults := configuration.RuntimeLoggingSettings{
		FileEnabled: true, FileDirectory: logging.DefaultFileDirectory,
		MaxFileSizeMB: logging.DefaultMaxFileSizeMB, CompressAfterDays: logging.DefaultCompressAfterDays,
	}
	if h.runtimeLogs != nil {
		defaultLevel = h.runtimeLogs.Level()
		defaultHTTPAccess = h.runtimeLogs.HTTPAccessEnabled()
		current := h.runtimeLogs.FileSettings()
		fileDefaults.FileEnabled = current.Enabled
		fileDefaults.FileDirectory = current.Directory
		fileDefaults.MaxFileSizeMB = current.MaxFileSizeMB
		fileDefaults.CompressAfterDays = current.CompressAfterDays
	}
	settings, err := h.service.GetRuntimeLoggingSettings(c.Request.Context(), defaultLevel, defaultHTTPAccess, fileDefaults)
	if err != nil {
		h.logger.Error("读取运行日志设置失败", "operation", "settings_runtime_logging_read", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, settings)
}

func (h settingsHandler) updateRuntimeLogging(c *gin.Context) {
	var request runtimeLoggingUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.HTTPAccessEnabled == nil || request.FileEnabled == nil {
		h.logger.Warn("修改运行日志设置参数无效", "operation", "settings_runtime_logging_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_settings", "日志级别或文件切分设置无效")
		return
	}
	fileSettings, err := logging.NormalizeFileSettings(logging.FileSettings{
		Enabled: *request.FileEnabled, Directory: request.FileDirectory,
		MaxFileSizeMB: request.MaxFileSizeMB, CompressAfterDays: request.CompressAfterDays,
	})
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_settings", "日志级别或文件切分设置无效")
		return
	}
	actor, _ := currentUser(c)
	settings, err := h.service.UpdateRuntimeLoggingSettings(
		c.Request.Context(), actor.ID, request.Level, *request.HTTPAccessEnabled,
		fileSettings.Enabled, fileSettings.Directory, fileSettings.MaxFileSizeMB, fileSettings.CompressAfterDays,
		request.ExpectedVersion,
	)
	if err != nil {
		h.logger.Warn("修改运行日志设置失败", "operation", "settings_runtime_logging_update", "request_id", requestIDFrom(c), "user_id", actor.ID, "err", err)
		switch {
		case errors.Is(err, configuration.ErrInvalidConfiguration):
			writeError(c, http.StatusBadRequest, "invalid_settings", "日志级别或文件切分设置无效")
		case errors.Is(err, configuration.ErrVersionConflict):
			writeError(c, http.StatusConflict, "settings_version_conflict", configuration.ErrVersionConflict.Error())
		default:
			writeInternalError(c)
		}
		return
	}
	if h.runtimeLogs != nil {
		if err := h.runtimeLogs.ApplySettings(settings.Level, settings.HTTPAccessEnabled, logging.FileSettings{
			Enabled: settings.FileEnabled, Directory: settings.FileDirectory,
			MaxFileSizeMB: settings.MaxFileSizeMB, CompressAfterDays: settings.CompressAfterDays,
		}); err != nil {
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

func (h settingsHandler) runtimeDirectories(c *gin.Context) {
	if h.repositories == nil || h.artifacts == nil {
		h.logger.Error("运行目录服务未初始化", "operation", "settings_runtime_directories_read", "request_id", requestIDFrom(c))
		writeInternalError(c)
		return
	}
	repositoryDirectories := h.repositories.Directories()
	settings, err := h.service.GetRuntimeDirectorySettings(c.Request.Context(), configuration.RuntimeDirectorySettings{
		WorkspaceDirectory:     repositoryDirectories.WorkspaceDirectory,
		BuildDirectory:         h.artifacts.BuildRoot(),
		CacheDirectory:         repositoryDirectories.CacheDirectory,
		LocalArtifactDirectory: h.artifacts.Root(),
	})
	if err != nil {
		h.logger.Error("读取运行目录设置失败", "operation", "settings_runtime_directories_read", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	h.writeRuntimeDirectoryResponse(c, settings, "settings_runtime_directories_read_usage")
}

func (h settingsHandler) updateRuntimeDirectories(c *gin.Context) {
	if h.repositories == nil || h.artifacts == nil {
		h.logger.Error("运行目录服务未初始化", "operation", "settings_runtime_directories_update", "request_id", requestIDFrom(c))
		writeInternalError(c)
		return
	}
	var request runtimeDirectoryUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("修改运行目录参数无效", "operation", "settings_runtime_directories_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_settings", "工作区、构建、缓存或本地产物目录无效")
		return
	}
	preparedRepositories, err := h.repositories.PrepareDirectories(request.WorkspaceDirectory, request.CacheDirectory)
	if err != nil {
		h.writeRuntimeDirectoryError(c, "settings_runtime_directories_prepare_repository", err)
		return
	}
	buildDirectory, err := h.artifacts.PrepareBuildDirectory(request.BuildDirectory)
	if err != nil {
		h.writeRuntimeDirectoryError(c, "settings_runtime_directories_prepare_build", err)
		return
	}
	artifactDirectory, err := manageddirectory.Prepare(request.LocalArtifactDirectory, "artifacts", false)
	if err != nil {
		h.writeRuntimeDirectoryError(c, "settings_runtime_directories_prepare_artifact", err)
		return
	}
	if err := manageddirectory.ValidateSeparate(
		preparedRepositories.WorkspaceDirectory,
		buildDirectory,
		preparedRepositories.CacheDirectory,
		artifactDirectory,
	); err != nil {
		h.writeRuntimeDirectoryError(c, "settings_runtime_directories_validate", err)
		return
	}
	change, err := h.artifacts.StageDirectory(c.Request.Context(), artifactDirectory)
	if err != nil {
		h.writeRuntimeDirectoryError(c, "settings_runtime_directories_stage_artifact", err)
		return
	}
	defer change.Abort()
	actor, _ := currentUser(c)
	settings, err := h.service.UpdateRuntimeDirectorySettings(c.Request.Context(), actor.ID, configuration.RuntimeDirectorySettings{
		WorkspaceDirectory:     preparedRepositories.WorkspaceDirectory,
		BuildDirectory:         buildDirectory,
		CacheDirectory:         preparedRepositories.CacheDirectory,
		LocalArtifactDirectory: artifactDirectory,
	}, request.ExpectedVersion)
	if err != nil {
		h.logger.Warn("保存运行目录设置失败", "operation", "settings_runtime_directories_update", "request_id", requestIDFrom(c), "user_id", actor.ID, "err", err)
		switch {
		case errors.Is(err, configuration.ErrInvalidConfiguration):
			writeError(c, http.StatusBadRequest, "invalid_settings", "工作区、构建、缓存或本地产物目录无效")
		case errors.Is(err, configuration.ErrVersionConflict):
			writeError(c, http.StatusConflict, "settings_version_conflict", configuration.ErrVersionConflict.Error())
		default:
			writeInternalError(c)
		}
		return
	}
	h.repositories.ApplyDirectories(preparedRepositories)
	h.artifacts.ApplyBuildDirectory(buildDirectory)
	if _, err := change.Commit(); err != nil {
		// 新目录已经生效，这里只是旧目录空间回收失败；保留副本比回滚后丢失并发写入更安全。
		h.logger.Warn("运行目录已切换，但旧产物目录未完全清理", "operation", "settings_runtime_directories_cleanup_previous", "request_id", requestIDFrom(c), "user_id", actor.ID, "err", err)
	}
	h.writeRuntimeDirectoryResponse(c, settings, "settings_runtime_directories_update_usage")
}

func (h settingsHandler) cleanupRepositoryWorkspaces(c *gin.Context) {
	if h.repositories == nil {
		h.logger.Error("仓库服务未初始化", "operation", "settings_repository_workspace_cleanup", "request_id", requestIDFrom(c))
		writeInternalError(c)
		return
	}
	report, err := h.repositories.ClearWorkspaceDirectory()
	if err != nil {
		h.logger.Warn("清理仓库工作区目录失败", "operation", "settings_repository_workspace_cleanup", "request_id", requestIDFrom(c), "err", err)
		if errors.Is(err, repository.ErrDirectoryBusy) {
			writeError(c, http.StatusConflict, "directory_busy", repository.ErrDirectoryBusy.Error())
		} else {
			writeInternalError(c)
		}
		return
	}
	c.JSON(http.StatusOK, report)
}

func (h settingsHandler) cleanupBuilds(c *gin.Context) {
	if h.artifacts == nil {
		h.logger.Error("制品服务未初始化", "operation", "settings_build_cleanup", "request_id", requestIDFrom(c))
		writeInternalError(c)
		return
	}
	report, err := h.artifacts.ClearBuildDirectory()
	if err != nil {
		h.logger.Warn("清理构建目录失败", "operation", "settings_build_cleanup", "request_id", requestIDFrom(c), "err", err)
		if errors.Is(err, artifactmanager.ErrBuildDirectoryBusy) {
			writeError(c, http.StatusConflict, "directory_busy", artifactmanager.ErrBuildDirectoryBusy.Error())
		} else {
			writeInternalError(c)
		}
		return
	}
	c.JSON(http.StatusOK, report)
}

func (h settingsHandler) cleanupRepositoryCache(c *gin.Context) {
	if h.repositories == nil {
		h.logger.Error("仓库服务未初始化", "operation", "settings_repository_cache_cleanup", "request_id", requestIDFrom(c))
		writeInternalError(c)
		return
	}
	report, err := h.repositories.ClearCacheDirectory()
	if err != nil {
		h.logger.Warn("清理仓库缓存目录失败", "operation", "settings_repository_cache_cleanup", "request_id", requestIDFrom(c), "err", err)
		if errors.Is(err, repository.ErrDirectoryBusy) {
			writeError(c, http.StatusConflict, "directory_busy", repository.ErrDirectoryBusy.Error())
		} else {
			writeInternalError(c)
		}
		return
	}
	c.JSON(http.StatusOK, report)
}

func (h settingsHandler) cleanupLocalArtifacts(c *gin.Context) {
	if h.artifacts == nil {
		h.logger.Error("制品服务未初始化", "operation", "settings_local_artifact_cleanup", "request_id", requestIDFrom(c))
		writeInternalError(c)
		return
	}
	report, err := h.artifacts.ClearLocalArtifacts(c.Request.Context())
	if err != nil {
		h.logger.Error("清理本地产物失败", "operation", "settings_local_artifact_cleanup", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, report)
}

func (h settingsHandler) writeRuntimeDirectoryResponse(c *gin.Context, settings configuration.RuntimeDirectorySettings, operation string) {
	repositoryDirectories := h.repositories.Directories()
	workspaceUsage, err := h.repositories.WorkspaceUsage()
	if err != nil {
		h.logger.Error("统计仓库工作区失败", "operation", operation, "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	buildUsage, err := h.artifacts.BuildDirectoryUsage()
	if err != nil {
		h.logger.Error("统计构建目录失败", "operation", operation, "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	cacheUsage, err := h.repositories.CacheUsage()
	if err != nil {
		h.logger.Error("统计仓库缓存失败", "operation", operation, "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	artifactUsage, err := h.artifacts.LocalArtifactUsage()
	if err != nil {
		h.logger.Error("统计本地产物失败", "operation", operation, "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, runtimeDirectoryResponse{
		RuntimeDirectorySettings: settings,
		WorkspaceUsage:           directoryUsageResponse{Path: repositoryDirectories.WorkspaceDirectory, Files: workspaceUsage.Files, Bytes: workspaceUsage.Bytes},
		BuildUsage:               directoryUsageResponse{Path: h.artifacts.BuildRoot(), Files: buildUsage.Files, Bytes: buildUsage.Bytes},
		CacheUsage:               directoryUsageResponse{Path: repositoryDirectories.CacheDirectory, Files: cacheUsage.Files, Bytes: cacheUsage.Bytes},
		ArtifactUsage:            directoryUsageResponse{Path: h.artifacts.Root(), Files: artifactUsage.Files, Bytes: artifactUsage.Bytes},
	})
}

func (h settingsHandler) writeRuntimeDirectoryError(c *gin.Context, operation string, err error) {
	h.logger.Warn("运行目录不可用", "operation", operation, "request_id", requestIDFrom(c), "err", err)
	switch {
	case errors.Is(err, manageddirectory.ErrDirectoryNotEmpty):
		writeError(c, http.StatusConflict, "directory_not_empty", "新目录必须为空，或选择此前由 EDO 管理的同用途目录")
	case errors.Is(err, manageddirectory.ErrDirectoryOverlap):
		writeError(c, http.StatusBadRequest, "directory_overlap", manageddirectory.ErrDirectoryOverlap.Error())
	case errors.Is(err, manageddirectory.ErrInvalidDirectory):
		writeError(c, http.StatusBadRequest, "invalid_directory", "目录不能是根目录、用户主目录、系统临时目录或 EDO 工作目录")
	default:
		writeError(c, http.StatusBadRequest, "directory_unavailable", "目录不可用，请检查路径和 EDO 服务进程权限")
	}
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
