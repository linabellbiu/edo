package httpapi

import (
	"errors"
	"net/http"

	"edo/internal/configuration"
	"github.com/gin-gonic/gin"
)

func (h settingsHandler) githubLogo(c *gin.Context) {
	settings, err := h.service.GetGitHubLogoSettings(c.Request.Context())
	if err != nil {
		h.logger.Error("读取 GitHub 标识设置失败", "operation", "settings_github_logo_read", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, settings)
}

func (h settingsHandler) updateGitHubLogo(c *gin.Context) {
	var request booleanSettingUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Enabled == nil {
		h.logger.Warn("修改 GitHub 标识设置参数无效", "operation", "settings_github_logo_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_settings", configuration.ErrInvalidConfiguration.Error())
		return
	}
	actor, _ := currentUser(c)
	settings, err := h.service.UpdateGitHubLogoSettings(c.Request.Context(), actor.ID, *request.Enabled, request.ExpectedVersion)
	if err != nil {
		h.logger.Error("修改 GitHub 标识设置失败", "operation", "settings_github_logo_update", "request_id", requestIDFrom(c), "actor_id", actor.ID, "err", err)
		switch {
		case errors.Is(err, configuration.ErrVersionConflict):
			writeError(c, http.StatusConflict, "settings_version_conflict", configuration.ErrVersionConflict.Error())
		case errors.Is(err, configuration.ErrInvalidConfiguration):
			writeError(c, http.StatusBadRequest, "invalid_settings", configuration.ErrInvalidConfiguration.Error())
		default:
			writeInternalError(c)
		}
		return
	}
	c.JSON(http.StatusOK, settings)
}
