package httpapi

import (
	"edo/internal/access"
	"edo/internal/database"
	"errors"
	"github.com/gin-gonic/gin"
	"log/slog"
	"net/http"
	"strings"
)

type projectHandler struct {
	service *access.Service
	logger  *slog.Logger
}
type projectRequest struct {
	Name         string `json:"name" binding:"required,max=96"`
	Description  string `json:"description" binding:"max=255"`
	DepartmentID string `json:"department_id"`
	IsActive     bool   `json:"is_active"`
}

func projectScope(service *access.Service, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if service == nil {
			return
		}
		path := strings.TrimPrefix(c.Request.URL.Path, "/api/v1")
		business := false
		for _, prefix := range []string{"/repositories", "/applications", "/workflow-templates", "/workflow-presets", "/workflow-runtime-versions", "/build-plans", "/image-registries", "/deployment-plans", "/release-plans", "/pipeline-runs", "/artifacts", "/docker", "/kubernetes", "/hosts", "/environments", "/deployments", "/terminals", "/dns", "/notification-channels", "/notifications", "/schedules", "/tasks", "/monitor-rules", "/events"} {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				business = true
				break
			}
		}
		if (path == "/auth/me" || path == "/settings/builtin-variables" || path == "/roles" || strings.HasPrefix(path, "/roles/") || path == "/users" || strings.HasPrefix(path, "/users/") || path == "/permissions") && (c.GetHeader("X-EDO-Project-ID") != "" || c.Query("project_id") != "") {
			business = true
		}
		platformContext := path == "/roles" || strings.HasPrefix(path, "/roles/") || path == "/users" || strings.HasPrefix(path, "/users/") || path == "/permissions" || path == "/settings/builtin-variables"
		if !business {
			return
		}
		user, ok := currentUser(c)
		if !ok {
			return
		}
		id := c.GetHeader("X-EDO-Project-ID")
		if id == "" {
			id = c.Query("project_id")
		}
		if id == "" {
			logger.Warn("业务请求缺少项目范围", "operation", "project_scope", "user_id", user.ID)
			writeError(c, 400, "project_required", "请先选择项目空间")
			c.Abort()
			return
		}
		project, err := service.ResolveProject(c.Request.Context(), user, id)
		if err != nil || !project.IsActive {
			if platformContext {
				logger.Debug("平台操作不采用不可访问的项目上下文", "operation", "platform_scope", "project_id", id, "err", err)
				return
			}
			logger.Warn("项目访问被拒绝", "operation", "project_scope", "user_id", user.ID, "project_id", id, "err", err)
			writeError(c, http.StatusForbidden, "project_denied", "项目不可用或没有访问权限")
			c.Abort()
			return
		}
		scope := database.DepartmentScope{UserID: user.ID, DepartmentID: project.DepartmentID, ProjectID: project.ID, AllDepartments: user.IsSuperuser}
		c.Request = c.Request.WithContext(database.WithDepartmentScope(c.Request.Context(), scope))
	}
}

func (h projectHandler) fail(c *gin.Context, operation string, err error) {
	h.logger.Warn("项目或组织操作失败", "operation", operation, "project_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, access.ErrProjectNotFound):
		writeError(c, 404, "project_not_found", access.ErrProjectNotFound.Error())
	case errors.Is(err, access.ErrAccessDelegationDenied), errors.Is(err, access.ErrSelfAccessUpdate):
		writeError(c, 403, "permission_denied", err.Error())
	case errors.Is(err, access.ErrProjectInvalid), errors.Is(err, access.ErrProjectMember):
		writeError(c, 400, "invalid_project", err.Error())
	case errors.Is(err, access.ErrProjectConflict), errors.Is(err, access.ErrProjectInUse), errors.Is(err, access.ErrProjectDefault):
		writeError(c, 409, "project_conflict", err.Error())
	default:
		writeInternalError(c)
	}
}
func (h projectHandler) list(c *gin.Context) {
	user, _ := currentUser(c)
	items, err := h.service.Projects(c.Request.Context(), user)
	if err != nil {
		h.fail(c, "project_list", err)
		return
	}
	c.JSON(200, gin.H{"projects": items})
}
func (h projectHandler) create(c *gin.Context) {
	var request projectRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("项目创建参数无效", "operation", "project_create_bind", "err", err)
		h.fail(c, "project_create_bind", access.ErrProjectInvalid)
		return
	}
	user, _ := currentUser(c)
	item, err := h.service.CreateProject(c.Request.Context(), user, request.Name, request.Description, request.DepartmentID)
	if err != nil {
		h.fail(c, "project_create", err)
		return
	}
	setAuditResourceID(c, item.ID)
	c.JSON(201, gin.H{"project": item})
}
func (h projectHandler) update(c *gin.Context) {
	var request projectRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("项目修改参数无效", "operation", "project_update_bind", "err", err)
		h.fail(c, "project_update_bind", access.ErrProjectInvalid)
		return
	}
	user, _ := currentUser(c)
	if err := h.service.UpdateProject(c.Request.Context(), user, c.Param("id"), request.Name, request.Description, request.IsActive); err != nil {
		h.fail(c, "project_update", err)
		return
	}
	c.Status(204)
}
func (h projectHandler) remove(c *gin.Context) {
	user, _ := currentUser(c)
	if err := h.service.DeleteProject(c.Request.Context(), user, c.Param("id")); err != nil {
		h.fail(c, "project_delete", err)
		return
	}
	c.Status(204)
}
func (h projectHandler) detail(c *gin.Context) {
	user, _ := currentUser(c)
	item, err := h.service.ProjectAccess(c.Request.Context(), user, c.Param("id"))
	if err != nil {
		h.fail(c, "project_detail", err)
		return
	}
	c.JSON(200, item)
}
func (h projectHandler) member(c *gin.Context) {
	var request struct {
		RoleID string `json:"role_id"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		h.fail(c, "project_member_bind", access.ErrProjectMember)
		return
	}
	user, _ := currentUser(c)
	if err := h.service.SetProjectMember(c.Request.Context(), user, c.Param("id"), c.Param("user_id"), request.RoleID); err != nil {
		h.fail(c, "project_member_save", err)
		return
	}
	c.Status(204)
}
func (h projectHandler) organization(c *gin.Context) {
	item, err := h.service.Organization(c.Request.Context())
	if err != nil {
		h.fail(c, "organization_read", err)
		return
	}
	c.JSON(200, gin.H{"organization": item})
}
func (h projectHandler) saveOrganization(c *gin.Context) {
	var request projectRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.fail(c, "organization_bind", access.ErrProjectInvalid)
		return
	}
	if err := h.service.UpdateOrganization(c.Request.Context(), request.Name, request.Description); err != nil {
		h.fail(c, "organization_update", err)
		return
	}
	c.Status(204)
}
