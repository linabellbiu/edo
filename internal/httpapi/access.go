package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/account"
	"edo/internal/audit"
	"edo/internal/department"
)

type accessHandler struct {
	accounts    *account.Service
	access      *access.Service
	audits      *audit.Service
	departments *department.Service
	logger      *slog.Logger
}

type createUserRequest struct {
	Username     string   `json:"username" binding:"required,max=32"`
	Nickname     string   `json:"nickname" binding:"max=64"`
	Password     string   `json:"password" binding:"required,max=512"`
	DepartmentID string   `json:"department_id" binding:"max=36"`
	RoleIDs      []string `json:"role_ids" binding:"max=100,dive,max=36"`
}

type setUserDepartmentRequest struct {
	DepartmentID string `json:"department_id" binding:"required,max=36"`
}

type setUserStatusRequest struct {
	Active *bool `json:"active" binding:"required"`
}

type setUserRolesRequest struct {
	RoleIDs []string `json:"role_ids" binding:"max=100,dive,max=36"`
}

type setUserPermissionsRequest struct {
	Allow []string `json:"allow" binding:"max=100,dive,max=96"`
	Deny  []string `json:"deny" binding:"max=100,dive,max=96"`
}

type roleRequest struct {
	Name        string   `json:"name" binding:"required,max=64"`
	DisplayName string   `json:"display_name" binding:"required,max=64"`
	Description string   `json:"description" binding:"max=255"`
	Permissions []string `json:"permissions" binding:"max=100,dive,max=96"`
}

type managedUserResponse struct {
	userResponse
	IsActive             bool                           `json:"is_active"`
	RoleIDs              []string                       `json:"role_ids"`
	PermissionOverrides  access.UserPermissionOverrides `json:"permission_overrides"`
	EffectivePermissions []string                       `json:"effective_permissions"`
}

func (h accessHandler) listUsers(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	users, err := h.accounts.List(c.Request.Context(), limit, offset)
	if err != nil {
		h.logger.Error("查询用户列表失败", "operation", "user_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	result := make([]managedUserResponse, 0, len(users))
	departmentNames := map[string]string{}
	if h.departments != nil {
		items, departmentErr := h.departments.List(c.Request.Context())
		if departmentErr != nil {
			h.logger.Error("查询用户所属部门失败", "operation", "user_list_departments", "request_id", requestIDFrom(c), "err", departmentErr)
			writeInternalError(c)
			return
		}
		for i := range items {
			departmentNames[items[i].ID] = items[i].Name
		}
	}
	for i := range users {
		roleIDs, err := h.access.UserRoleIDs(c.Request.Context(), users[i].ID)
		if err != nil {
			h.logger.Error("查询用户角色失败", "operation", "user_list_roles", "request_id", requestIDFrom(c), "user_id", users[i].ID, "err", err)
			writeInternalError(c)
			return
		}
		overrides, err := h.access.UserPermissionOverrides(c.Request.Context(), users[i].ID)
		if err != nil {
			h.logger.Error("查询用户权限覆盖失败", "operation", "user_list_permissions", "request_id", requestIDFrom(c), "user_id", users[i].ID, "err", err)
			writeInternalError(c)
			return
		}
		effective, err := h.access.UserPermissions(c.Request.Context(), &users[i])
		if err != nil {
			h.logger.Error("查询用户有效权限失败", "operation", "user_list_effective_permissions", "request_id", requestIDFrom(c), "user_id", users[i].ID, "err", err)
			writeInternalError(c)
			return
		}
		userView := toUserResponse(&users[i])
		userView.DepartmentName = departmentNames[users[i].DepartmentID]
		result = append(result, managedUserResponse{
			userResponse: userView, IsActive: users[i].IsActive, RoleIDs: roleIDs,
			PermissionOverrides: overrides, EffectivePermissions: effective,
		})
	}
	c.JSON(http.StatusOK, gin.H{"users": result})
}

func (h accessHandler) createUser(c *gin.Context) {
	var request createUserRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建用户请求参数无效", "operation", "user_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", "用户信息格式无效")
		return
	}
	actor, _ := currentUser(c)
	if request.DepartmentID == "" && actor != nil {
		request.DepartmentID = actor.DepartmentID
	}
	if actor == nil || (!actor.IsSuperuser && actor.DepartmentID != request.DepartmentID) {
		h.logger.Warn("拒绝跨部门创建用户", "operation", "user_create_department", "request_id", requestIDFrom(c), "department_id", request.DepartmentID)
		writeError(c, http.StatusForbidden, "department_scope_denied", "只能在当前所属部门创建用户")
		return
	}
	departmentName := ""
	if h.departments != nil {
		departmentView, err := h.departments.Get(c.Request.Context(), request.DepartmentID)
		if err != nil || !departmentView.IsActive {
			h.logger.Warn("创建用户选择的部门不可用", "operation", "user_create_department", "request_id", requestIDFrom(c), "department_id", request.DepartmentID, "err", err)
			writeError(c, http.StatusBadRequest, "invalid_department", "请选择有效的启用部门")
			return
		}
		departmentName = departmentView.Name
	}
	user, err := h.access.CreateUserInDepartment(c.Request.Context(), request.Username, request.Nickname, request.Password, request.DepartmentID, request.RoleIDs)
	if err != nil {
		h.logger.Warn("创建用户失败", "operation", "user_create", "request_id", requestIDFrom(c), "username", request.Username, "err", err)
		switch {
		case errors.Is(err, account.ErrUsernameExists):
			writeError(c, http.StatusConflict, "username_exists", account.ErrUsernameExists.Error())
		case errors.Is(err, account.ErrInvalidUser):
			writeError(c, http.StatusBadRequest, "invalid_user", "用户名或昵称格式无效")
		case errors.Is(err, account.ErrInvalidPassword):
			writeError(c, http.StatusBadRequest, "invalid_password", "密码至少需要 12 个字符，且不能超过 128 个字符")
		case errors.Is(err, access.ErrInvalidUserRoles):
			writeError(c, http.StatusBadRequest, "invalid_roles", access.ErrInvalidUserRoles.Error())
		default:
			writeInternalError(c)
		}
		return
	}
	setAuditResourceID(c, user.ID)
	c.JSON(http.StatusCreated, gin.H{"user": managedUserResponse{
		userResponse: func() userResponse {
			value := toUserResponse(user)
			value.DepartmentName = departmentName
			return value
		}(), IsActive: user.IsActive, RoleIDs: request.RoleIDs,
		PermissionOverrides: access.UserPermissionOverrides{Allow: []string{}, Deny: []string{}}, EffectivePermissions: []string{},
	}})
}

func (h accessHandler) setUserDepartment(c *gin.Context) {
	var request setUserDepartmentRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("调整用户部门请求参数无效", "operation", "user_department_bind", "request_id", requestIDFrom(c), "user_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_department", "请选择有效的部门")
		return
	}
	actor, _ := currentUser(c)
	if actor == nil || !actor.IsSuperuser {
		h.logger.Warn("拒绝非超级管理员调整用户部门", "operation", "user_department_denied", "request_id", requestIDFrom(c), "user_id", c.Param("id"))
		writeError(c, http.StatusForbidden, "department_scope_denied", "只有超级管理员可以调整用户所属部门")
		return
	}
	if err := h.accounts.SetDepartment(c.Request.Context(), c.Param("id"), request.DepartmentID); err != nil {
		h.logger.Warn("调整用户部门失败", "operation", "user_department_update", "request_id", requestIDFrom(c), "user_id", c.Param("id"), "department_id", request.DepartmentID, "err", err)
		switch {
		case errors.Is(err, account.ErrUserNotFound):
			writeError(c, http.StatusNotFound, "user_not_found", account.ErrUserNotFound.Error())
		case errors.Is(err, account.ErrSuperuserImmutable):
			writeError(c, http.StatusConflict, "superuser_immutable", account.ErrSuperuserImmutable.Error())
		case errors.Is(err, account.ErrUserCredentialUsed):
			writeError(c, http.StatusConflict, "user_credential_in_use", account.ErrUserCredentialUsed.Error())
		case errors.Is(err, account.ErrInvalidUser):
			writeError(c, http.StatusBadRequest, "invalid_department", "请选择有效的启用部门")
		default:
			writeInternalError(c)
		}
		return
	}
	c.Status(http.StatusNoContent)
}

func (h accessHandler) setUserStatus(c *gin.Context) {
	var request setUserStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改用户状态请求参数无效", "operation", "user_status_bind", "request_id", requestIDFrom(c), "user_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", "用户状态格式无效")
		return
	}
	actor, _ := currentUser(c)
	if actor != nil && actor.ID == c.Param("id") && !*request.Active {
		h.logger.Warn("用户尝试停用自身账户", "operation", "user_status_self", "request_id", requestIDFrom(c), "user_id", actor.ID)
		writeError(c, http.StatusConflict, "cannot_disable_self", "不能停用当前登录账户")
		return
	}
	if err := h.accounts.SetActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.logger.Warn("修改用户状态失败", "operation", "user_status", "request_id", requestIDFrom(c), "user_id", c.Param("id"), "err", err)
		switch {
		case errors.Is(err, account.ErrUserNotFound):
			writeError(c, http.StatusNotFound, "user_not_found", account.ErrUserNotFound.Error())
		case errors.Is(err, account.ErrSuperuserImmutable):
			writeError(c, http.StatusConflict, "superuser_immutable", account.ErrSuperuserImmutable.Error())
		default:
			writeInternalError(c)
		}
		return
	}
	c.Status(http.StatusNoContent)
}

func (h accessHandler) deleteUser(c *gin.Context) {
	actor, _ := currentUser(c)
	if actor != nil && actor.ID == c.Param("id") {
		h.logger.Warn("用户尝试删除当前登录账户", "operation", "user_delete_self", "request_id", requestIDFrom(c), "user_id", actor.ID)
		writeError(c, http.StatusConflict, "cannot_delete_self", "不能删除当前登录账户")
		return
	}
	if err := h.access.DeleteUser(c.Request.Context(), c.Param("id")); err != nil {
		h.logger.Warn("删除用户失败", "operation", "user_delete", "request_id", requestIDFrom(c), "user_id", c.Param("id"), "err", err)
		switch {
		case errors.Is(err, account.ErrUserNotFound):
			writeError(c, http.StatusNotFound, "user_not_found", account.ErrUserNotFound.Error())
		case errors.Is(err, account.ErrSuperuserImmutable):
			writeError(c, http.StatusConflict, "superuser_immutable", "超级管理员不能删除")
		case errors.Is(err, account.ErrUserCredentialUsed):
			writeError(c, http.StatusConflict, "user_credential_in_use", account.ErrUserCredentialUsed.Error())
		default:
			writeInternalError(c)
		}
		return
	}
	c.Status(http.StatusNoContent)
}

func (h accessHandler) setUserRoles(c *gin.Context) {
	var request setUserRolesRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("配置用户角色请求参数无效", "operation", "user_roles_bind", "request_id", requestIDFrom(c), "user_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", access.ErrInvalidUserRoles.Error())
		return
	}
	user, err := h.accounts.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.logger.Warn("配置用户角色时查询用户失败", "operation", "user_roles_find", "request_id", requestIDFrom(c), "user_id", c.Param("id"), "err", err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(c, http.StatusNotFound, "user_not_found", account.ErrUserNotFound.Error())
		} else {
			writeInternalError(c)
		}
		return
	}
	if user.IsSuperuser {
		h.logger.Warn("拒绝修改超级管理员角色", "operation", "user_roles_superuser", "request_id", requestIDFrom(c), "user_id", user.ID)
		writeError(c, http.StatusConflict, "superuser_immutable", "超级管理员不需要分配角色")
		return
	}
	if err := h.access.SetUserRoles(c.Request.Context(), user.ID, request.RoleIDs); err != nil {
		h.logger.Warn("配置用户角色失败", "operation", "user_roles", "request_id", requestIDFrom(c), "user_id", user.ID, "err", err)
		if errors.Is(err, access.ErrInvalidUserRoles) {
			writeError(c, http.StatusBadRequest, "invalid_roles", access.ErrInvalidUserRoles.Error())
		} else {
			writeInternalError(c)
		}
		return
	}
	c.Status(http.StatusNoContent)
}

func (h accessHandler) setUserPermissions(c *gin.Context) {
	var request setUserPermissionsRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("配置用户权限请求参数无效", "operation", "user_permissions_bind", "request_id", requestIDFrom(c), "user_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", access.ErrInvalidUserPermissions.Error())
		return
	}
	if err := h.access.SetUserPermissions(c.Request.Context(), c.Param("id"), access.UserPermissionOverrides{
		Allow: request.Allow, Deny: request.Deny,
	}); err != nil {
		h.logger.Warn("配置用户权限失败", "operation", "user_permissions", "request_id", requestIDFrom(c), "user_id", c.Param("id"), "err", err)
		if errors.Is(err, access.ErrInvalidUserPermissions) {
			writeError(c, http.StatusBadRequest, "invalid_user_permissions", access.ErrInvalidUserPermissions.Error())
		} else {
			writeInternalError(c)
		}
		return
	}
	c.Status(http.StatusNoContent)
}

func (h accessHandler) listRoles(c *gin.Context) {
	roles, err := h.access.ListRoles(c.Request.Context())
	if err != nil {
		h.logger.Error("查询角色列表失败", "operation", "role_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"roles": roles})
}

func (h accessHandler) createRole(c *gin.Context) {
	var request roleRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建角色请求参数无效", "operation", "role_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", access.ErrInvalidRole.Error())
		return
	}
	role, err := h.access.CreateRole(c.Request.Context(), toRoleInput(request))
	if err != nil {
		h.writeRoleError(c, "role_create", err)
		return
	}
	setAuditResourceID(c, role.ID)
	c.JSON(http.StatusCreated, gin.H{"role": role})
}

func (h accessHandler) updateRole(c *gin.Context) {
	var request roleRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新角色请求参数无效", "operation", "role_update_bind", "request_id", requestIDFrom(c), "role_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", access.ErrInvalidRole.Error())
		return
	}
	role, err := h.access.UpdateRole(c.Request.Context(), c.Param("id"), toRoleInput(request))
	if err != nil {
		h.writeRoleError(c, "role_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"role": role})
}

func (h accessHandler) deleteRole(c *gin.Context) {
	if err := h.access.DeleteRole(c.Request.Context(), c.Param("id")); err != nil {
		h.writeRoleError(c, "role_delete", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h accessHandler) listAuditLogs(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	var before time.Time
	if value := c.Query("before"); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			h.logger.Warn("审计日志游标无效", "operation", "audit_list_cursor", "request_id", requestIDFrom(c), "err", err)
			writeError(c, http.StatusBadRequest, "invalid_cursor", "审计日志分页游标无效")
			return
		}
		before = parsed
	}
	logs, err := h.audits.List(c.Request.Context(), limit, before)
	if err != nil {
		h.logger.Error("查询审计日志失败", "operation", "audit_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"audit_logs": logs})
}

func (h accessHandler) writeRoleError(c *gin.Context, operation string, err error) {
	h.logger.Warn("角色操作失败", "operation", operation, "request_id", requestIDFrom(c), "role_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, access.ErrInvalidRole):
		writeError(c, http.StatusBadRequest, "invalid_role", access.ErrInvalidRole.Error())
	case errors.Is(err, access.ErrInvalidPermission):
		writeError(c, http.StatusBadRequest, "invalid_permission", access.ErrInvalidPermission.Error())
	case errors.Is(err, access.ErrRoleNameExists):
		writeError(c, http.StatusConflict, "role_name_exists", access.ErrRoleNameExists.Error())
	case errors.Is(err, access.ErrRoleNotFound):
		writeError(c, http.StatusNotFound, "role_not_found", access.ErrRoleNotFound.Error())
	case errors.Is(err, access.ErrRoleInUse):
		writeError(c, http.StatusConflict, "role_in_use", access.ErrRoleInUse.Error())
	default:
		writeInternalError(c)
	}
}

func toRoleInput(request roleRequest) access.RoleInput {
	return access.RoleInput{
		Name: request.Name, DisplayName: request.DisplayName,
		Description: request.Description, Permissions: request.Permissions,
	}
}

func writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, errorResponse{Code: code, Message: message, RequestID: requestIDFrom(c)})
}

func writeInternalError(c *gin.Context) {
	writeError(c, http.StatusInternalServerError, "internal_error", "服务暂时不可用，请稍后重试")
}
