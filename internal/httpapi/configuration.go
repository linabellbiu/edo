package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"edo/internal/configuration"
	"edo/internal/model"
	"edo/internal/secret"
)

type configurationHandler struct {
	service *configuration.Service
	logger  *slog.Logger
}

type configurationCreateRequest struct {
	Namespace   string                `json:"namespace" binding:"required,max=64"`
	Environment model.EnvironmentType `json:"environment" binding:"required,max=16"`
	Key         string                `json:"key" binding:"required,max=128"`
	Value       *string               `json:"value" binding:"required"`
	IsSecret    bool                  `json:"is_secret"`
}

type configurationUpdateRequest struct {
	Value           *string `json:"value" binding:"required"`
	IsSecret        bool    `json:"is_secret"`
	ExpectedVersion int     `json:"expected_version" binding:"required,min=1"`
}

type configurationStatusRequest struct {
	Active          *bool `json:"active" binding:"required"`
	ExpectedVersion int   `json:"expected_version" binding:"required,min=1"`
}

func (h configurationHandler) list(c *gin.Context) {
	items, err := h.service.List(c.Request.Context(), c.Query("namespace"), model.EnvironmentType(c.Query("environment")))
	if err != nil {
		h.logger.Error("查询配置失败", "operation", "configuration_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"configurations": items})
}

func (h configurationHandler) create(c *gin.Context) {
	var request configurationCreateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建配置参数无效", "operation", "configuration_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_configuration", configuration.ErrInvalidConfiguration.Error())
		return
	}
	actor, _ := currentUser(c)
	item, err := h.service.Create(c.Request.Context(), actor.ID, configuration.Input{
		Namespace: request.Namespace, Environment: request.Environment, Key: request.Key,
		Value: request.Value, IsSecret: request.IsSecret,
	})
	if err != nil {
		h.writeError(c, "configuration_create", err)
		return
	}
	setAuditResourceID(c, item.ID)
	c.JSON(http.StatusCreated, gin.H{"configuration": item})
}

func (h configurationHandler) update(c *gin.Context) {
	var request configurationUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新配置参数无效", "operation", "configuration_update_bind", "request_id", requestIDFrom(c), "configuration_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_configuration", configuration.ErrInvalidConfiguration.Error())
		return
	}
	actor, _ := currentUser(c)
	item, err := h.service.Update(c.Request.Context(), c.Param("id"), actor.ID, configuration.UpdateInput{
		Value: request.Value, IsSecret: request.IsSecret, ExpectedVersion: request.ExpectedVersion,
	})
	if err != nil {
		h.writeError(c, "configuration_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"configuration": item})
}

func (h configurationHandler) setStatus(c *gin.Context) {
	var request configurationStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改配置状态参数无效", "operation", "configuration_status_bind", "request_id", requestIDFrom(c), "configuration_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_configuration", configuration.ErrInvalidConfiguration.Error())
		return
	}
	actor, _ := currentUser(c)
	item, err := h.service.SetActive(c.Request.Context(), c.Param("id"), actor.ID, *request.Active, request.ExpectedVersion)
	if err != nil {
		h.writeError(c, "configuration_status", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"configuration": item})
}

func (h configurationHandler) revisions(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	items, err := h.service.Revisions(c.Request.Context(), c.Param("id"), limit)
	if err != nil {
		h.writeError(c, "configuration_revisions", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"revisions": items})
}

func (h configurationHandler) writeError(c *gin.Context, operation string, err error) {
	h.logger.Warn("配置操作失败", "operation", operation, "request_id", requestIDFrom(c), "configuration_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, configuration.ErrInvalidConfiguration):
		writeError(c, http.StatusBadRequest, "invalid_configuration", configuration.ErrInvalidConfiguration.Error())
	case errors.Is(err, configuration.ErrConfigurationExists):
		writeError(c, http.StatusConflict, "configuration_exists", configuration.ErrConfigurationExists.Error())
	case errors.Is(err, configuration.ErrConfigurationNotFound):
		writeError(c, http.StatusNotFound, "configuration_not_found", configuration.ErrConfigurationNotFound.Error())
	case errors.Is(err, configuration.ErrVersionConflict):
		writeError(c, http.StatusConflict, "configuration_version_conflict", configuration.ErrVersionConflict.Error())
	case errors.Is(err, secret.ErrUnavailable):
		writeError(c, http.StatusServiceUnavailable, "secret_encryption_unavailable", secret.ErrUnavailable.Error())
	default:
		writeInternalError(c)
	}
}
