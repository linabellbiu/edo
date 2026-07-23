package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"zrt/internal/audit"
	"zrt/internal/model"
)

func sameOrigin(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}
		if strings.EqualFold(c.GetHeader("Sec-Fetch-Site"), "cross-site") {
			logger.Warn("拒绝跨站状态变更请求", "operation", "csrf_check", "request_id", requestIDFrom(c), "host", c.Request.Host)
			c.AbortWithStatusJSON(http.StatusForbidden, errorResponse{
				Code: "cross_site_request_denied", Message: "跨站请求已被拒绝", RequestID: requestIDFrom(c),
			})
			return
		}
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, c.Request.Host) {
			logger.Warn("拒绝来源不匹配的状态变更请求", "operation", "csrf_origin", "request_id", requestIDFrom(c), "host", c.Request.Host)
			c.AbortWithStatusJSON(http.StatusForbidden, errorResponse{
				Code: "origin_mismatch", Message: "请求来源校验失败", RequestID: requestIDFrom(c),
			})
			return
		}
		c.Next()
	}
}

const requestIDKey = "request_id"
const auditResourceIDKey = "audit_resource_id"

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

func accessLog(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
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
		if err := audits.Record(c.Request.Context(), audit.RecordInput{
			ActorUserID: actorID, Action: action, ResourceType: resourceType,
			ResourceID: resourceID, Result: result, RequestID: requestIDFrom(c),
			ClientIP: c.ClientIP(), UserAgent: truncateText(c.Request.UserAgent(), 512),
			Metadata: map[string]any{
				"method": c.Request.Method, "path": c.FullPath(), "status": c.Writer.Status(),
			},
		}); err != nil {
			logger.Error("记录操作审计失败", "operation", "audit_record", "request_id", requestIDFrom(c), "action", action, "resource_type", resourceType, "resource_id", resourceID, "err", err)
		}
	}
}

func setAuditResourceID(c *gin.Context, resourceID string) {
	c.Set(auditResourceIDKey, resourceID)
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
