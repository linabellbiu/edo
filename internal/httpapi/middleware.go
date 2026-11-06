package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"edo/internal/audit"
	"edo/internal/logging"
	"edo/internal/model"
)

const requestIDKey = "request_id"
const auditResourceIDKey = "audit_resource_id"
const auditMetadataKey = "audit_metadata"

func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if !validRequestID(id) {
			id = uuid.NewString()
		}
		c.Set(requestIDKey, id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

func accessLog(logger *slog.Logger, runtimeLogs *logging.RuntimeController) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		if runtimeLogs != nil && !runtimeLogs.HTTPAccessEnabled() {
			return
		}
		logger.Info("HTTP 请求完成",
			"operation", "http_request",
			"request_id", requestIDFrom(c),
			"method", c.Request.Method,
			"path", c.FullPath(),
			"status", c.Writer.Status(),
			"latency_ms", time.Since(started).Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}

func recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("HTTP 请求发生未处理异常",
					"operation", "http_recovery",
					"request_id", requestIDFrom(c),
					"method", c.Request.Method,
					"path", c.Request.URL.Path,
					"err", fmt.Sprint(recovered),
					"stack", string(debug.Stack()),
				)
				c.AbortWithStatusJSON(http.StatusInternalServerError, errorResponse{
					Code:      "internal_error",
					Message:   "服务暂时不可用，请稍后重试",
					RequestID: requestIDFrom(c),
				})
			}
		}()
		c.Next()
	}
}

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "same-origin")
		c.Next()
	}
}

func auditAction(audits *audit.Service, logger *slog.Logger, action, resourceType string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		result := model.AuditSucceeded
		if c.Writer.Status() == http.StatusForbidden {
			result = model.AuditDenied
		} else if c.Writer.Status() >= http.StatusBadRequest {
			result = model.AuditFailed
		}
		actorID := ""
		if actor, ok := currentUser(c); ok {
			actorID = actor.ID
		}
		resourceID := c.Param("id")
		if value, exists := c.Get(auditResourceIDKey); exists {
			if id, ok := value.(string); ok {
				resourceID = id
			}
		}
		// 撤权、断线和超时同样必须留审计；只剥离取消信号，保留原用户与项目范围。
		auditContext, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second)
		defer cancel()
		metadata := map[string]any{"method": c.Request.Method, "path": c.FullPath(), "status": c.Writer.Status()}
		if value, exists := c.Get(auditMetadataKey); exists {
			if extra, ok := value.(map[string]any); ok {
				for key, value := range extra {
					metadata[key] = value
				}
			}
		}
		if err := audits.Record(auditContext, audit.RecordInput{
			ActorUserID: actorID, Action: action, ResourceType: resourceType,
			ResourceID: resourceID, Result: result, RequestID: requestIDFrom(c),
			ClientIP: c.ClientIP(), UserAgent: truncateText(c.Request.UserAgent(), 512),
			Metadata: metadata,
		}); err != nil {
			logger.Error("记录操作审计失败", "operation", "audit_record", "request_id", requestIDFrom(c), "action", action, "resource_type", resourceType, "resource_id", resourceID, "err", err)
		}
	}
}

func setAuditResourceID(c *gin.Context, resourceID string) {
	c.Set(auditResourceIDKey, resourceID)
}

// 仅由业务处理器传入有界身份及确认信息，不采集请求正文或运行配置。
func setAuditMetadata(c *gin.Context, extra map[string]any) {
	current := map[string]any{}
	if value, exists := c.Get(auditMetadataKey); exists {
		if stored, ok := value.(map[string]any); ok {
			current = stored
		}
	}
	for key, value := range extra {
		current[key] = value
	}
	c.Set(auditMetadataKey, current)
}

func truncateText(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func requestIDFrom(c *gin.Context) string {
	value, _ := c.Get(requestIDKey)
	id, _ := value.(string)
	return id
}

func validRequestID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}
