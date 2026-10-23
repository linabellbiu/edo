package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"edo/internal/access"
	"edo/internal/assistant"
	"edo/internal/audit"
	"edo/internal/model"
	"github.com/gin-gonic/gin"
)

type assistantHandler struct {
	service    *assistant.Service
	audits     *audit.Service
	logger     *slog.Logger
	streamAuth func(*gin.Context) func() bool
}

func registerAssistantRoutes(protected *gin.RouterGroup, deps Dependencies) {
	h := assistantHandler{service: deps.Assistant, audits: deps.Audits, logger: deps.Logger, streamAuth: func(c *gin.Context) func() bool {
		// 模型取消后的清理可能晚于请求结束，不能持有会被 Gin 复用的上下文。
		captured := c.Copy()
		read := streamAuthorization(deps, captured, access.PermissionAssistantRead)
		execute := streamAuthorization(deps, captured, access.PermissionAssistantExecute)
		return func() bool { return read() && execute() }
	}}
	available := func(c *gin.Context) {
		if h.service == nil {
			h.logger.Error("助手服务未初始化", "operation", "assistant_unavailable", "request_id", requestIDFrom(c))
			writeError(c, http.StatusServiceUnavailable, "assistant_unavailable", "助手暂不可用，请稍后重试")
			c.Abort()
		}
	}
	settings := protected.Group("/settings/assistant", available)
	settings.GET("", requirePermission(deps.Access, deps.Logger, access.PermissionConfigRead), h.settings)
	settings.PUT("", requirePermission(deps.Access, deps.Logger, access.PermissionConfigUpdate), auditAction(deps.Audits, deps.Logger, "assistant.settings.update", "settings"), h.saveSettings)
	settings.POST("/test", requirePermission(deps.Access, deps.Logger, access.PermissionConfigUpdate), auditAction(deps.Audits, deps.Logger, "assistant.settings.test", "settings"), h.testSettings)
	chat := protected.Group("/assistant", available, requirePermission(deps.Access, deps.Logger, access.PermissionAssistantRead))
	chat.GET("/status", h.status)
	chat.GET("/capabilities", h.capabilities)
	chat.GET("/conversations", h.list)
	chat.POST("/conversations", requirePermission(deps.Access, deps.Logger, access.PermissionAssistantExecute), h.create)
	chat.GET("/conversations/:id", h.detail)
	chat.POST("/conversations/:id/messages", requirePermission(deps.Access, deps.Logger, access.PermissionAssistantExecute), h.message)
	chat.POST("/actions/:id/confirm", requirePermission(deps.Access, deps.Logger, access.PermissionAssistantExecute), h.confirm)
	chat.POST("/actions/:id/reject", requirePermission(deps.Access, deps.Logger, access.PermissionAssistantExecute), h.reject)
}

func (h assistantHandler) fail(c *gin.Context, operation string, err error) {
	h.logger.Warn("助手请求失败", "operation", operation, "resource_id", c.Param("id"), "request_id", requestIDFrom(c), "err", err)
	status, code, message := assistantHTTPError(err)
	writeError(c, status, code, message)
}

func assistantHTTPError(err error) (int, string, string) {
	status, code, message := http.StatusInternalServerError, "assistant_error", "助手暂不可用，请稍后重试"
	switch {
	case errors.Is(err, assistant.ErrForbidden):
		status, code, message = http.StatusForbidden, "assistant_denied", assistant.ErrForbidden.Error()
	case errors.Is(err, assistant.ErrNotFound):
		status, code, message = http.StatusNotFound, "assistant_not_found", assistant.ErrNotFound.Error()
	case errors.Is(err, assistant.ErrInvalidInput):
		status, code, message = http.StatusBadRequest, "assistant_invalid", assistant.ErrInvalidInput.Error()
	case errors.Is(err, assistant.ErrDisabled):
		status, code, message = http.StatusConflict, "assistant_disabled", assistant.ErrDisabled.Error()
	case errors.Is(err, assistant.ErrUnavailable):
		status, code, message = http.StatusConflict, "assistant_unconfigured", assistant.ErrUnavailable.Error()
	case errors.Is(err, assistant.ErrConflict):
		status, code, message = http.StatusConflict, "assistant_conflict", assistant.ErrConflict.Error()
	case errors.Is(err, assistant.ErrBusy):
		status, code, message = http.StatusConflict, "assistant_busy", assistant.ErrBusy.Error()
	case errors.Is(err, assistant.ErrActionExpired):
		status, code, message = http.StatusGone, "assistant_action_expired", assistant.ErrActionExpired.Error()
	case errors.Is(err, assistant.ErrActionState):
		status, code, message = http.StatusConflict, "assistant_action_state", assistant.ErrActionState.Error()
	case errors.Is(err, assistant.ErrModelRequest):
		status, code, message = http.StatusBadGateway, "assistant_model_failed", assistant.ErrModelRequest.Error()
	case errors.Is(err, assistant.ErrCanceled):
		status, code, message = http.StatusConflict, "assistant_canceled", assistant.ErrCanceled.Error()
	case errors.Is(err, assistant.ErrOutputLimit):
		status, code, message = http.StatusUnprocessableEntity, "assistant_output_limit", assistant.ErrOutputLimit.Error()
	case errors.Is(err, assistant.ErrStorage):
		status, code, message = http.StatusServiceUnavailable, "assistant_storage_failed", assistant.ErrStorage.Error()
	}
	return status, code, message
}

func (h assistantHandler) settings(c *gin.Context) {
	result, err := h.service.Settings(c.Request.Context())
	if err != nil {
		h.fail(c, "assistant_settings_read", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func (h assistantHandler) settingsInput(c *gin.Context) (assistant.SettingsInput, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	var input assistant.SettingsInput
	if err := c.ShouldBindJSON(&input); err != nil {
		h.logger.Warn("助手设置格式无效", "operation", "assistant_settings_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "assistant_invalid", "请检查模型配置格式")
		return input, false
	}
	return input, true
}

func (h assistantHandler) saveSettings(c *gin.Context) {
	input, ok := h.settingsInput(c)
	if !ok {
		return
	}
	user, _ := currentUser(c)
	result, err := h.service.SaveSettings(c.Request.Context(), user.ID, input)
	if err != nil {
		h.fail(c, "assistant_settings_save", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func (h assistantHandler) testSettings(c *gin.Context) {
	input, ok := h.settingsInput(c)
	if !ok {
		return
	}
	result, err := h.service.TestSettings(c.Request.Context(), input)
	if err != nil {
		h.fail(c, "assistant_settings_test", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func (h assistantHandler) status(c *gin.Context) {
	result, err := h.service.Status(c.Request.Context())
	if err != nil {
		h.fail(c, "assistant_status", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func (h assistantHandler) capabilities(c *gin.Context) {
	user, _ := currentUser(c)
	items, err := h.service.Capabilities(c.Request.Context(), user)
	if err != nil {
		h.fail(c, "assistant_capabilities", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"capabilities": items})
}

func (h assistantHandler) list(c *gin.Context) {
	user, _ := currentUser(c)
	items, err := h.service.ListConversations(c.Request.Context(), user)
	if err != nil {
		h.fail(c, "assistant_conversations", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"conversations": items})
}

func (h assistantHandler) create(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	var input struct {
		Title string `json:"title" binding:"max=128"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		h.fail(c, "assistant_conversation_bind", assistant.ErrInvalidInput)
		return
	}
	user, _ := currentUser(c)
	item, err := h.service.CreateConversation(c.Request.Context(), user, input.Title)
	if err != nil {
		h.fail(c, "assistant_conversation_create", err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"conversation": item})
}

func (h assistantHandler) detail(c *gin.Context) {
	user, _ := currentUser(c)
	item, err := h.service.Conversation(c.Request.Context(), user, c.Param("id"))
	if err != nil {
		h.fail(c, "assistant_conversation_read", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, item)
}

func (h assistantHandler) confirm(c *gin.Context) {
	user, _ := currentUser(c)
	item, err := h.service.ConfirmAction(c.Request.Context(), user, c.Param("id"))
	if err != nil {
		h.fail(c, "assistant_action_confirm", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"action": item})
}

func (h assistantHandler) reject(c *gin.Context) {
	user, _ := currentUser(c)
	item, err := h.service.RejectAction(c.Request.Context(), user, c.Param("id"))
	if err != nil {
		h.fail(c, "assistant_action_reject", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"action": item})
}

func (h assistantHandler) message(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 40*1024)
	var input struct {
		Content string `json:"content" binding:"required,max=8000"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || strings.TrimSpace(input.Content) == "" {
		h.fail(c, "assistant_message_bind", assistant.ErrInvalidInput)
		return
	}
	user, _ := currentUser(c)
	if _, err := h.service.Conversation(c.Request.Context(), user, c.Param("id")); err != nil {
		h.fail(c, "assistant_message_scope", err)
		return
	}
	status, err := h.service.Status(c.Request.Context())
	if err != nil {
		h.fail(c, "assistant_message_status", err)
		return
	}
	if !status.Enabled {
		h.fail(c, "assistant_message_disabled", assistant.ErrDisabled)
		return
	}
	if !status.Configured {
		h.fail(c, "assistant_message_unconfigured", assistant.ErrUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	authorized := h.streamAuth(c)
	if !authorized() {
		h.fail(c, "assistant_stream_authorization", assistant.ErrForbidden)
		return
	}
	ctx = assistant.WithSessionAuthorization(ctx, authorized)
	controller := http.NewResponseController(c.Writer)
	if err := controller.SetWriteDeadline(time.Now().Add(6 * time.Minute)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		h.logger.Warn("助手响应超时设置失败", "operation", "assistant_stream_deadline", "request_id", requestIDFrom(c), "err", err)
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	write := func(event assistant.Event) bool {
		if !authorized() {
			cancel()
			return false
		}
		data, err := json.Marshal(event)
		if err != nil {
			h.logger.Error("助手事件序列化失败", "operation", "assistant_event_encode", "err", err)
			return false
		}
		if _, err := c.Writer.Write(append(append([]byte("data: "), data...), '\n', '\n')); err != nil {
			h.logger.Debug("助手连接已关闭", "operation", "assistant_stream_write", "err", err)
			return false
		}
		c.Writer.Flush()
		return true
	}
	if !write(assistant.Event{Type: "start"}) {
		return
	}
	events := make(chan assistant.Event, 32)
	finished := make(chan error, 1)
	conversationID := c.Param("id")
	go func() {
		var chatErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				h.logger.Error("助手执行异常已中止", "operation", "assistant_chat_panic", "conversation_id", conversationID)
				chatErr = assistant.ErrModelRequest
			}
			finished <- chatErr
			close(events)
		}()
		chatErr = h.service.Chat(ctx, user, conversationID, input.Content, func(event assistant.Event) {
			select {
			case events <- event:
			case <-ctx.Done():
			}
		})
	}()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	var runErr error
	defer func() { h.recordChat(c, user, runErr) }()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				runErr = <-finished
				if runErr != nil && ctx.Err() == nil {
					h.logger.Warn("助手对话未完成", "operation", "assistant_chat", "conversation_id", c.Param("id"), "request_id", requestIDFrom(c), "err", runErr)
					_, _, message := assistantHTTPError(runErr)
					write(assistant.Event{Type: "error", Error: message})
				}
				return
			}
			if !write(event) {
				runErr = context.Canceled
				return
			}
		case <-ticker.C:
			if !authorized() {
				runErr = assistant.ErrForbidden
				return
			}
			if _, err := c.Writer.Write([]byte(": heartbeat\n\n")); err != nil {
				runErr = context.Canceled
				return
			}
			c.Writer.Flush()
		case <-ctx.Done():
			runErr = ctx.Err()
			return
		}
	}
}

func (h assistantHandler) recordChat(c *gin.Context, user *model.User, err error) {
	if h.audits == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second)
	defer cancel()
	result := model.AuditSucceeded
	if err != nil {
		result = model.AuditFailed
	}
	if err := h.audits.Record(ctx, audit.RecordInput{ActorUserID: user.ID, Action: "assistant.chat", ResourceType: "assistant_conversation", ResourceID: c.Param("id"), Result: result, RequestID: requestIDFrom(c), Metadata: map[string]any{"canceled": errors.Is(err, context.Canceled), "permission_denied": errors.Is(err, assistant.ErrForbidden)}}); err != nil {
		h.logger.Error("助手对话审计失败", "operation", "assistant_chat_audit", "conversation_id", c.Param("id"), "err", err)
	}
}
