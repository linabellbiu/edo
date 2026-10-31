package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"edo/internal/audit"
	"edo/internal/database"
	"edo/internal/model"
)

const projectScopeRejectionKey = "project_scope_rejection"

type projectScopeRejection struct {
	projectID string
	reason    string
}

func markProjectScopeRejection(c *gin.Context, projectID, reason string) {
	c.Set(projectScopeRejectionKey, projectScopeRejection{projectID: projectAuditUUID(projectID), reason: reason})
}

func projectAuditUUID(value string) string {
	if len(value) != 36 {
		return ""
	}
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return ""
	}
	return id.String()
}

// 范围拒绝早于路由审计；只补已认证写请求，不为读请求或已准入请求重复记账。
func auditRejectedProjectScope(audits *audit.Service, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		value, exists := c.Get(projectScopeRejectionKey)
		rejection, marked := value.(projectScopeRejection)
		if !exists || !marked {
			return
		}
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			return
		}
		actor, authenticated := currentUser(c)
		if !authenticated {
			return
		}
		status := c.Writer.Status()
		if status != http.StatusBadRequest && status != http.StatusForbidden {
			return
		}
		metadata := map[string]any{
			"method": c.Request.Method, "path": c.FullPath(), "status": status, "reason": rejection.reason,
		}
		if targetID := projectAuditUUID(c.Param("id")); targetID != "" {
			metadata["target_id"] = targetID
		}
		result := model.AuditDenied
		if status == http.StatusBadRequest {
			result = model.AuditFailed
		}
		// 未通过准入的项目不能成为业务数据范围，审计仅采用已认证用户的部门。
		ctx := database.WithDepartmentScope(c.Request.Context(), database.DepartmentScope{UserID: actor.ID, DepartmentID: actor.DepartmentID})
		var err error
		if audits == nil {
			err = errors.New("审计服务不可用")
		} else {
			err = audits.Record(ctx, audit.RecordInput{
				ActorUserID: actor.ID, Action: "project.access.denied", ResourceType: "project",
				ResourceID: rejection.projectID, Result: result, RequestID: requestIDFrom(c), Metadata: metadata,
			})
		}
		if err != nil {
			logger.Error("记录项目范围拒绝审计失败", "operation", "project_scope_audit", "request_id", requestIDFrom(c), "user_id", actor.ID, "project_id", rejection.projectID, "method", c.Request.Method, "path", c.FullPath(), "status", status, "reason", rejection.reason, "err", err)
		}
	}
}
