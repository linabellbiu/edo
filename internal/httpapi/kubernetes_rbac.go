package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"edo/internal/access"
	"edo/internal/kube"
)

const kubernetesRBACRead = "kubernetes.rbac.read"
const kubernetesRBACManage = "kubernetes.rbac.update"

type kubernetesRBACHandler struct{ deps Dependencies }

type kubernetesRBACGrantRequest struct {
	ID               string   `json:"id" binding:"omitempty,max=36"`
	RoleID           string   `json:"role_id" binding:"required,max=36"`
	Namespace        string   `json:"namespace" binding:"max=63"`
	ClusterScope     bool     `json:"cluster_scope"`
	Permissions      []string `json:"permissions" binding:"required,min=1,max=64,dive,max=96"`
	ExpectedRevision *uint64  `json:"expected_revision" binding:"required"`
}

func (r kubernetesRBACGrantRequest) input() kube.RBACGrantInput {
	return kube.RBACGrantInput{RoleID: r.RoleID, Namespace: r.Namespace, ClusterScope: r.ClusterScope, Permissions: r.Permissions, ExpectedRevision: *r.ExpectedRevision}
}

type kubernetesRBACRevisionRequest struct {
	ExpectedRevision *uint64 `json:"expected_revision" binding:"required"`
}

type kubernetesRBACRole struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Permissions []string `json:"permissions"`
}

type kubernetesRBACResponse struct {
	*kube.RBACState
	Roles      []kubernetesRBACRole       `json:"roles"`
	Catalog    []kubernetesRBACPermission `json:"catalog"`
	Namespaces []string                   `json:"namespaces"`
}

type kubernetesRBACPermission struct {
	access.Permission
	ResourceScopes []string `json:"resource_scopes"`
}

func registerKubernetesRBACRoutes(protected *gin.RouterGroup, deps Dependencies) {
	h := kubernetesRBACHandler{deps: deps}
	path := "/kubernetes/clusters/:id/rbac"
	read := requireAnyPermission(deps.Access, deps.Logger, kubernetesRBACRead, kubernetesRBACManage)
	manage := requirePermission(deps.Access, deps.Logger, kubernetesRBACManage)
	protected.GET(path, read, h.list)
	protected.POST(path+"/grants/preview", manage, h.preview)
	protected.POST(path+"/grants", manage, auditAction(deps.Audits, deps.Logger, "kubernetes.rbac.grant.create", "kubernetes_rbac"), h.save)
	protected.PUT(path+"/grants/:grant", manage, auditAction(deps.Audits, deps.Logger, "kubernetes.rbac.grant.update", "kubernetes_rbac"), h.save)
	protected.DELETE(path+"/grants/:grant", manage, auditAction(deps.Audits, deps.Logger, "kubernetes.rbac.grant.delete", "kubernetes_rbac"), h.remove)
	protected.PUT(path+"/mode", manage, auditAction(deps.Audits, deps.Logger, "kubernetes.rbac.mode.update", "kubernetes_rbac"), h.mode)
	protected.POST(path+"/sync", manage, auditAction(deps.Audits, deps.Logger, "kubernetes.rbac.sync", "kubernetes_rbac"), h.sync)
}

func (h kubernetesRBACHandler) available(c *gin.Context) bool {
	c.Header("Cache-Control", "no-store")
	if h.deps.Kubernetes == nil || h.deps.Access == nil {
		writeError(c, http.StatusServiceUnavailable, "kubernetes_rbac_unavailable", "集群授权服务暂不可用")
		return false
	}
	return true
}

func (h kubernetesRBACHandler) respond(c *gin.Context, state *kube.RBACState) {
	roles, err := h.deps.Access.ListRoles(c.Request.Context())
	if err != nil {
		h.fail(c, "kubernetes_rbac_roles", err)
		return
	}
	result := kubernetesRBACResponse{RBACState: state, Roles: []kubernetesRBACRole{}, Catalog: []kubernetesRBACPermission{}, Namespaces: []string{}}
	for _, role := range roles {
		valid, hasKubernetes := len(role.Permissions) > 0, false
		for _, code := range role.Permissions {
			valid = valid && access.ProjectPermission(code)
			hasKubernetes = hasKubernetes || strings.HasPrefix(code, "kubernetes.") && !strings.HasPrefix(code, "kubernetes.rbac.")
		}
		if valid && hasKubernetes {
			result.Roles = append(result.Roles, kubernetesRBACRole{ID: role.ID, Name: role.Name, DisplayName: role.DisplayName, Permissions: role.Permissions})
		}
	}
	for _, permission := range access.Catalog() {
		if strings.HasPrefix(permission.Code, "kubernetes.") && !strings.HasPrefix(permission.Code, "kubernetes.rbac.") {
			result.Catalog = append(result.Catalog, kubernetesRBACPermission{Permission: permission, ResourceScopes: kube.RBACPermissionScopes(permission.Code)})
		}
	}
	seen := map[string]bool{}
	for _, grant := range state.Grants {
		if grant.Namespace != "" && !seen[grant.Namespace] {
			result.Namespaces = append(result.Namespaces, grant.Namespace)
			seen[grant.Namespace] = true
		}
	}
	c.JSON(http.StatusOK, result)
}

func (h kubernetesRBACHandler) list(c *gin.Context) {
	if !h.available(c) {
		return
	}
	state, err := h.deps.Kubernetes.ListClusterRBAC(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.fail(c, "kubernetes_rbac_list", err)
		return
	}
	h.respond(c, state)
}

func (h kubernetesRBACHandler) bindGrant(c *gin.Context) (kubernetesRBACGrantRequest, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32*1024)
	var request kubernetesRBACGrantRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_kubernetes_rbac", kube.ErrRBACInvalid.Error())
		return request, false
	}
	return request, true
}

func (h kubernetesRBACHandler) preview(c *gin.Context) {
	if !h.available(c) {
		return
	}
	request, ok := h.bindGrant(c)
	if !ok {
		return
	}
	actor, _ := currentUser(c)
	preview, err := h.deps.Kubernetes.PreviewRoleGrant(c.Request.Context(), actor.ID, c.Param("id"), request.ID, request.input())
	if err != nil {
		h.fail(c, "kubernetes_rbac_preview", err)
		return
	}
	c.JSON(http.StatusOK, preview)
}

func (h kubernetesRBACHandler) save(c *gin.Context) {
	if !h.available(c) {
		return
	}
	request, ok := h.bindGrant(c)
	if !ok {
		return
	}
	actor, _ := currentUser(c)
	state, err := h.deps.Kubernetes.SaveRoleGrant(c.Request.Context(), actor.ID, c.Param("id"), c.Param("grant"), request.input())
	if err != nil {
		h.fail(c, "kubernetes_rbac_save", err)
		return
	}
	setAuditResourceID(c, c.Param("id")+"/"+c.Param("grant"))
	h.respond(c, state)
}

func (h kubernetesRBACHandler) remove(c *gin.Context) {
	if !h.available(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var request kubernetesRBACRevisionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, 400, "invalid_kubernetes_rbac", kube.ErrRBACInvalid.Error())
		return
	}
	actor, _ := currentUser(c)
	state, err := h.deps.Kubernetes.DeleteRoleGrant(c.Request.Context(), actor.ID, c.Param("id"), c.Param("grant"), *request.ExpectedRevision)
	if err != nil {
		h.fail(c, "kubernetes_rbac_delete", err)
		return
	}
	setAuditResourceID(c, c.Param("id")+"/"+c.Param("grant"))
	h.respond(c, state)
}

func (h kubernetesRBACHandler) mode(c *gin.Context) {
	if !h.available(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var request struct {
		Enabled          *bool   `json:"enabled" binding:"required"`
		ExpectedRevision *uint64 `json:"expected_revision" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, 400, "invalid_kubernetes_rbac", kube.ErrRBACInvalid.Error())
		return
	}
	actor, _ := currentUser(c)
	state, err := h.deps.Kubernetes.SetClusterRBACEnabled(c.Request.Context(), actor.ID, c.Param("id"), *request.Enabled, *request.ExpectedRevision)
	if err != nil {
		h.fail(c, "kubernetes_rbac_mode", err)
		return
	}
	setAuditResourceID(c, c.Param("id"))
	h.respond(c, state)
}

func (h kubernetesRBACHandler) sync(c *gin.Context) {
	if !h.available(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var request kubernetesRBACRevisionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, 400, "invalid_kubernetes_rbac", kube.ErrRBACInvalid.Error())
		return
	}
	// A user-triggered sync can include several members and namespaces. Extend
	// only this request; the controller uses the same bounded operation budget.
	controller := http.NewResponseController(c.Writer)
	if err := controller.SetWriteDeadline(time.Now().Add(135 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		h.fail(c, "kubernetes_rbac_deadline", err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	actor, _ := currentUser(c)
	state, err := h.deps.Kubernetes.SyncClusterRBAC(ctx, actor.ID, c.Param("id"), *request.ExpectedRevision)
	if err != nil {
		h.fail(c, "kubernetes_rbac_sync", err)
		return
	}
	setAuditResourceID(c, c.Param("id"))
	h.respond(c, state)
}

func (h kubernetesRBACHandler) fail(c *gin.Context, operation string, err error) {
	if writeKubernetesAuthorizationError(c, err) {
		return
	}
	switch {
	case errors.Is(err, kube.ErrClusterNotFound), errors.Is(err, kube.ErrRBACNotFound):
		writeError(c, 404, "kubernetes_rbac_not_found", "集群或授权不存在")
	case errors.Is(err, kube.ErrRBACConflict):
		writeError(c, 409, "kubernetes_rbac_conflict", kube.ErrRBACConflict.Error())
	case errors.Is(err, kube.ErrRBACInvalid):
		writeError(c, 400, "invalid_kubernetes_rbac", kube.ErrRBACInvalid.Error())
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, 504, "kubernetes_rbac_timeout", "集群授权同步超时，请查看同步状态后重试")
	default:
		if h.deps.Logger != nil {
			h.deps.Logger.Error("集群授权操作失败", "operation", operation, "cluster_id", c.Param("id"), "request_id", requestIDFrom(c), "err", err)
		}
		writeError(c, 502, "kubernetes_rbac_unavailable", "集群授权操作未完成，请检查集群凭据及同步状态")
	}
}

func writeKubernetesAuthorizationError(c *gin.Context, err error) bool {
	switch {
	case errors.Is(err, kube.ErrRBACForbidden):
		writeError(c, 403, "kubernetes_rbac_forbidden", kube.ErrRBACForbidden.Error())
		return true
	case errors.Is(err, kube.ErrRBACSyncPending):
		writeError(c, 503, "kubernetes_rbac_pending", kube.ErrRBACSyncPending.Error())
		return true
	}
	return false
}
