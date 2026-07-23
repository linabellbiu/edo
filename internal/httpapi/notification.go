package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"zrt/internal/model"
	"zrt/internal/notification"
	"zrt/internal/secret"
)

type notificationHandler struct {
	service *notification.Service
	logger  *slog.Logger
}

type notificationChannelRequest struct {
	Name      string                        `json:"name" binding:"required,max=128"`
	Type      model.NotificationChannelType `json:"type" binding:"required,max=16"`
	Endpoint  *string                       `json:"endpoint"`
	Token     *string                       `json:"token"`
	AllowHTTP bool                          `json:"allow_http"`
}

func (h notificationHandler) listChannels(c *gin.Context) {
	channels, err := h.service.ListChannels(c.Request.Context())
	if err != nil {
		h.logger.Error("查询通知渠道失败", "operation", "notification_channel_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"channels": channels})
}

func (h notificationHandler) createChannel(c *gin.Context) {
	var request notificationChannelRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Endpoint == nil {
		h.logger.Warn("创建通知渠道参数无效", "operation", "notification_channel_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_notification_channel", notification.ErrInvalidChannel.Error())
		return
	}
	actor, _ := currentUser(c)
	channel, err := h.service.CreateChannel(c.Request.Context(), actor.ID, toNotificationChannelInput(request))
	if err != nil {
		h.writeError(c, "notification_channel_create", err)
		return
	}
	setAuditResourceID(c, channel.ID)
	c.JSON(http.StatusCreated, gin.H{"channel": channel})
}

func (h notificationHandler) updateChannel(c *gin.Context) {
	var request notificationChannelRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新通知渠道参数无效", "operation", "notification_channel_update_bind", "request_id", requestIDFrom(c), "channel_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_notification_channel", notification.ErrInvalidChannel.Error())
		return
	}
	channel, err := h.service.UpdateChannel(c.Request.Context(), c.Param("id"), toNotificationChannelInput(request))
	if err != nil {
		h.writeError(c, "notification_channel_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"channel": channel})
}

func (h notificationHandler) setChannelStatus(c *gin.Context) {
	var request runtimeStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改通知渠道状态参数无效", "operation", "notification_channel_status_bind", "request_id", requestIDFrom(c), "channel_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_notification_channel", notification.ErrInvalidChannel.Error())
		return
	}
	if err := h.service.SetChannelActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeError(c, "notification_channel_status", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h notificationHandler) testChannel(c *gin.Context) {
	actor, _ := currentUser(c)
	item, err := h.service.Enqueue(c.Request.Context(), notification.EnqueueInput{
		ChannelID: c.Param("id"), Title: "ZRT 通知测试",
		Message: "这是一条由 ZRT 发起的通知渠道连通性测试。", Severity: model.NotificationInfo,
		Source: "manual_test", SourceID: actor.ID,
	})
	if err != nil {
		h.writeError(c, "notification_channel_test", err)
		return
	}
	setAuditResourceID(c, item.ID)
	c.JSON(http.StatusAccepted, gin.H{"notification": item})
}

func (h notificationHandler) list(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	items, err := h.service.List(c.Request.Context(), limit)
	if err != nil {
		h.logger.Error("查询通知记录失败", "operation", "notification_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"notifications": items})
}

func (h notificationHandler) writeError(c *gin.Context, operation string, err error) {
	h.logger.Warn("通知操作失败", "operation", operation, "request_id", requestIDFrom(c), "channel_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, notification.ErrInvalidChannel):
		writeError(c, http.StatusBadRequest, "invalid_notification_channel", notification.ErrInvalidChannel.Error())
	case errors.Is(err, notification.ErrInvalidNotification):
		writeError(c, http.StatusBadRequest, "invalid_notification", notification.ErrInvalidNotification.Error())
	case errors.Is(err, notification.ErrChannelExists):
		writeError(c, http.StatusConflict, "notification_channel_exists", notification.ErrChannelExists.Error())
	case errors.Is(err, notification.ErrChannelNotFound):
		writeError(c, http.StatusNotFound, "notification_channel_not_found", notification.ErrChannelNotFound.Error())
	case errors.Is(err, secret.ErrUnavailable):
		writeError(c, http.StatusServiceUnavailable, "secret_encryption_unavailable", secret.ErrUnavailable.Error())
	default:
		writeInternalError(c)
	}
}

func toNotificationChannelInput(request notificationChannelRequest) notification.ChannelInput {
	return notification.ChannelInput{
		Name: request.Name, Type: request.Type, Endpoint: request.Endpoint,
		Token: request.Token, AllowHTTP: request.AllowHTTP,
	}
}
