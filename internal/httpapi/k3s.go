package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"edo/internal/access"
	"edo/internal/k3s"
	"edo/internal/kube"
)

type k3sHandler struct {
	service *k3s.Service
	access  *access.Service
	logger  *slog.Logger
}

func registerK3sRoutes(protected *gin.RouterGroup, deps Dependencies) {
	h := k3sHandler{service: deps.K3s, access: deps.Access, logger: deps.Logger}
	canInstall := requireAnyPermission(deps.Access, deps.Logger, access.PermissionClusterCreate, "kubernetes.node.create")
	protected.GET("/kubernetes/k3s/options", canInstall, h.options)
	protected.POST("/kubernetes/k3s/plans", canInstall, auditAction(deps.Audits, deps.Logger, "kubernetes.k3s.plan", "k3s_installation"), h.preview)
	protected.POST("/kubernetes/k3s/installations", canInstall, auditAction(deps.Audits, deps.Logger, "kubernetes.k3s.install", "k3s_installation"), h.execute)
	protected.GET("/kubernetes/k3s/installations", canInstall, h.list)
	protected.GET("/kubernetes/k3s/installations/:id", canInstall, h.detail)
}

func (h k3sHandler) available(c *gin.Context) bool {
	c.Header("Cache-Control", "no-store")
	if h.service == nil {
		writeError(c, http.StatusServiceUnavailable, "k3s_unavailable", "K3s 安装服务暂不可用")
		return false
	}
	return true
}

func (h k3sHandler) options(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, k3s.OptionsCatalog())
}

func (h k3sHandler) authorize(c *gin.Context, action string) bool {
	actor, ok := currentUser(c)
	if !ok || h.access == nil {
		writeError(c, http.StatusForbidden, "permission_denied", "没有执行 K3s 安装的权限")
		return false
	}
	required := []string{access.PermissionClusterExecute, access.PermissionClusterUpdate}
	switch action {
	case "create":
		required = append(required, access.PermissionClusterCreate)
	case "join":
		required = append(required, "kubernetes.node.create")
	default:
		writeError(c, http.StatusBadRequest, "invalid_k3s_action", "请选择创建集群或添加节点")
		return false
	}
	for _, permission := range required {
		allowed, err := h.access.HasPermission(c.Request.Context(), actor, permission)
		if err != nil {
			h.writeError(c, "k3s_permission", err)
			return false
		}
		if !allowed {
			writeError(c, http.StatusForbidden, "permission_denied", "没有执行 K3s 安装的权限")
			return false
		}
	}
	return true
}

func (h k3sHandler) preview(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256*1024)
	var input k3s.Input
	if err := c.ShouldBindJSON(&input); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_k3s_plan", "请检查安装配置")
		return
	}
	if !h.authorize(c, input.Action) || !h.available(c) {
		return
	}
	actor, _ := currentUser(c)
	plan, err := h.service.Preview(c.Request.Context(), actor.ID, input)
	if err != nil {
		h.writeError(c, "k3s_plan", err)
		return
	}
	setAuditResourceID(c, plan.ID)
	c.JSON(http.StatusCreated, plan)
}

func (h k3sHandler) execute(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input struct {
		PlanID string `json:"plan_id" binding:"required,max=36"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_k3s_plan", "请提供有效的安装计划")
		return
	}
	if !h.available(c) {
		return
	}
	plan, err := h.service.Get(c.Request.Context(), input.PlanID)
	if err != nil {
		h.writeError(c, "k3s_plan_lookup", err)
		return
	}
	if !h.authorize(c, plan.Action) {
		return
	}
	actor, _ := currentUser(c)
	installation, err := h.service.Execute(c.Request.Context(), actor.ID, input.PlanID)
	if err != nil {
		h.writeError(c, "k3s_install", err)
		return
	}
	setAuditResourceID(c, installation.ID)
	c.JSON(http.StatusAccepted, installation)
}

func (h k3sHandler) list(c *gin.Context) {
	if !h.available(c) {
		return
	}
	limit := 20
	if value := c.Query("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 50 {
			writeError(c, http.StatusBadRequest, "invalid_k3s_limit", "记录数量须为 1 至 50")
			return
		}
	}
	action := c.Query("action")
	if action != "" && action != "create" && action != "join" {
		writeError(c, http.StatusBadRequest, "invalid_k3s_action", "请选择创建集群或添加节点")
		return
	}
	items, err := h.service.List(c.Request.Context(), c.Query("cluster_id"), limit, action)
	if err != nil {
		h.writeError(c, "k3s_installations_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"installations": items})
}

func (h k3sHandler) detail(c *gin.Context) {
	if !h.available(c) {
		return
	}
	installation, err := h.service.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, "k3s_installation_get", err)
		return
	}
	c.JSON(http.StatusOK, installation)
}

func (h k3sHandler) writeError(c *gin.Context, operation string, err error) {
	status, code, message := http.StatusInternalServerError, "internal_error", "K3s 安装服务暂时不可用"
	switch {
	case errors.Is(err, k3s.ErrInvalidInput):
		status, code, message = http.StatusBadRequest, "invalid_k3s_config", k3s.ErrInvalidInput.Error()
		if detail := k3s.ValidationMessage(err); detail != "" {
			message = detail
		}
	case errors.Is(err, k3s.ErrPermission):
		status, code, message = http.StatusForbidden, "permission_denied", k3s.ErrPermission.Error()
	case errors.Is(err, k3s.ErrNotFound), errors.Is(err, kube.ErrClusterNotFound):
		status, code, message = http.StatusNotFound, "k3s_installation_not_found", k3s.ErrNotFound.Error()
	case errors.Is(err, kube.ErrClusterExists):
		status, code, message = http.StatusConflict, "kubernetes_cluster_exists", kube.ErrClusterExists.Error()
	case errors.Is(err, kube.ErrJoinUnsupported):
		status, code, message = http.StatusBadRequest, "k3s_join_unsupported", "当前集群不支持 K3s 主机安装，请使用对应的节点加入方式"
	case errors.Is(err, k3s.ErrPlanExpired):
		status, code, message = http.StatusConflict, "k3s_plan_expired", k3s.ErrPlanExpired.Error()
	case errors.Is(err, k3s.ErrPlanConsumed):
		status, code, message = http.StatusConflict, "k3s_plan_consumed", k3s.ErrPlanConsumed.Error()
	case errors.Is(err, k3s.ErrHostBusy):
		status, code, message = http.StatusConflict, "k3s_host_busy", k3s.ErrHostBusy.Error()
	case errors.Is(err, k3s.ErrHostChanged):
		status, code, message = http.StatusConflict, "k3s_host_changed", k3s.ErrHostChanged.Error()
	case errors.Is(err, k3s.ErrExistingInstallation):
		status, code, message = http.StatusConflict, "k3s_already_installed", k3s.ErrExistingInstallation.Error()
	case errors.Is(err, k3s.ErrReadiness):
		status, code, message = http.StatusGatewayTimeout, "k3s_not_ready", k3s.ErrReadiness.Error()
	case errors.Is(err, k3s.ErrRemote):
		status, code, message = http.StatusBadGateway, "k3s_remote_failed", k3s.ErrRemote.Error()
	}
	if status >= 500 && h.logger != nil {
		h.logger.Error("K3s 安装请求失败", "operation", operation, "request_id", requestIDFrom(c), "err", err)
	}
	writeError(c, status, code, message)
}
