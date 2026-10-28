package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"edo/internal/access"
	"edo/internal/k3s"
)

func registerK3sUninstallRoutes(protected *gin.RouterGroup, deps Dependencies) {
	h := k3sHandler{service: deps.K3s, access: deps.Access, logger: deps.Logger}
	canUninstall := requirePermission(deps.Access, deps.Logger, access.PermissionK3sUninstall)
	canRead := requirePermission(deps.Access, deps.Logger, access.PermissionClusterRead)
	protected.GET("/kubernetes/k3s/uninstall-targets", canUninstall, canRead, h.uninstallTargets)
	protected.POST("/kubernetes/k3s/uninstall/plans", canUninstall, canRead, auditAction(deps.Audits, deps.Logger, "kubernetes.k3s.uninstall.plan", "k3s_installation"), h.previewUninstall)
	protected.POST("/kubernetes/k3s/uninstallations", canUninstall, canRead, auditAction(deps.Audits, deps.Logger, "kubernetes.k3s.uninstall.execute", "k3s_installation"), h.executeUninstall)
}

func (h k3sHandler) uninstallAvailable(c *gin.Context) bool {
	c.Header("Cache-Control", "no-store")
	if h.service == nil {
		h.uninstallError(c, "k3s_uninstall_available", errors.New("K3s 卸载服务尚未初始化"))
		return false
	}
	return true
}

func (h k3sHandler) uninstallTargets(c *gin.Context) {
	if !h.uninstallAvailable(c) {
		return
	}
	var query struct {
		HostID string `form:"host_id" binding:"required,max=36"`
	}
	if err := c.ShouldBindQuery(&query); err != nil {
		h.uninstallError(c, "k3s_uninstall_targets_bind", errors.Join(k3s.ErrInvalidInput, err))
		return
	}
	actor, _ := currentUser(c)
	targets, err := h.service.ListUninstallTargets(c.Request.Context(), actor.ID, query.HostID)
	if err != nil {
		h.uninstallError(c, "k3s_uninstall_targets", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"targets": targets})
}

func (h k3sHandler) previewUninstall(c *gin.Context) {
	if !h.uninstallAvailable(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input struct {
		InstallationID string `json:"installation_id" binding:"required,max=36"`
		HostID         string `json:"host_id" binding:"required,max=36"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		h.uninstallError(c, "k3s_uninstall_plan_bind", errors.Join(k3s.ErrInvalidInput, err))
		return
	}
	controller := http.NewResponseController(c.Writer)
	if err := controller.SetWriteDeadline(time.Now().Add(k3s.PreflightTimeout + 15*time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		h.uninstallError(c, "k3s_uninstall_plan_deadline", err)
		return
	}
	actor, _ := currentUser(c)
	plan, err := h.service.PreviewUninstall(c.Request.Context(), actor.ID, k3s.UninstallInput{InstallationID: input.InstallationID, HostID: input.HostID})
	if err != nil {
		h.uninstallError(c, "k3s_uninstall_plan", err)
		return
	}
	setAuditResourceID(c, plan.ID)
	c.JSON(http.StatusCreated, plan)
}

func (h k3sHandler) executeUninstall(c *gin.Context) {
	if !h.uninstallAvailable(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input struct {
		PlanID       string `json:"plan_id" binding:"required,max=36"`
		Confirmation string `json:"confirmation" binding:"max=16"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		h.uninstallError(c, "k3s_uninstall_execute_bind", errors.Join(k3s.ErrInvalidInput, err))
		return
	}
	if input.Confirmation != "确认" {
		h.uninstallError(c, "k3s_uninstall_confirmation", k3s.ErrUninstallConfirmation)
		return
	}
	controller := http.NewResponseController(c.Writer)
	if err := controller.SetWriteDeadline(time.Now().Add(k3s.PreflightTimeout + 15*time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		h.uninstallError(c, "k3s_uninstall_execute_deadline", err)
		return
	}
	actor, _ := currentUser(c)
	installation, err := h.service.ExecuteUninstall(c.Request.Context(), actor.ID, input.PlanID, input.Confirmation)
	if err != nil {
		h.uninstallError(c, "k3s_uninstall_execute", err)
		return
	}
	setAuditResourceID(c, installation.ID)
	c.JSON(http.StatusAccepted, installation)
}

func (h k3sHandler) uninstallError(c *gin.Context, operation string, err error) {
	status, code, message := http.StatusInternalServerError, "k3s_uninstall_unavailable", "K3s 卸载服务暂时不可用"
	switch {
	case errors.Is(err, k3s.ErrPermission), errors.Is(err, k3s.ErrUninstallPermission):
		status, code, message = http.StatusForbidden, "permission_denied", "没有卸载 K3s 的权限，或当前项目权限已变化"
	case errors.Is(err, k3s.ErrUninstallConfirmation):
		status, code, message = http.StatusBadRequest, "k3s_uninstall_confirmation", "请输入“确认”后再执行卸载"
	case errors.Is(err, k3s.ErrInvalidInput):
		status, code, message = http.StatusBadRequest, "invalid_k3s_uninstall", "请检查 K3s 卸载配置"
	case errors.Is(err, k3s.ErrNotFound):
		status, code, message = http.StatusNotFound, "k3s_uninstall_not_found", "卸载目标或任务不存在"
	case errors.Is(err, k3s.ErrPlanExpired):
		status, code, message = http.StatusConflict, "k3s_uninstall_plan_expired", "卸载计划已过期，请重新预览"
	case errors.Is(err, k3s.ErrPlanConsumed):
		status, code, message = http.StatusConflict, "k3s_uninstall_plan_consumed", "卸载计划已提交，请查看任务结果"
	case errors.Is(err, k3s.ErrHostBusy):
		status, code, message = http.StatusConflict, "k3s_host_busy", "目标主机正在执行其他集群任务"
	case errors.Is(err, k3s.ErrHostChanged), errors.Is(err, k3s.ErrUninstallChanged):
		status, code, message = http.StatusConflict, "k3s_uninstall_changed", "卸载目标、连接或脚本已变化，请重新预览"
	case errors.Is(err, k3s.ErrUninstallUnsupported):
		status, code, message = http.StatusConflict, "k3s_uninstall_unsupported", "当前目标无法安全使用官方卸载脚本，请核对安装记录和主机状态"
	case errors.Is(err, k3s.ErrUninstallIncomplete):
		status, code, message = http.StatusConflict, "k3s_uninstall_incomplete", "此前卸载结果未确认，当前禁止再次安装或卸载，请查看任务记录并核对现场"
	case errors.Is(err, context.DeadlineExceeded):
		status, code, message = http.StatusGatewayTimeout, "k3s_uninstall_timeout", "卸载检查超时，请稍后重新预览"
	case errors.Is(err, context.Canceled):
		status, code, message = http.StatusRequestTimeout, "k3s_uninstall_canceled", "卸载请求已取消"
	}
	if h.logger != nil {
		actor, _ := currentUser(c)
		actorID := ""
		if actor != nil {
			actorID = actor.ID
		}
		h.logger.Warn("K3s 卸载请求失败", "operation", operation, "request_id", requestIDFrom(c), "user_id", actorID, "status", status, "err", err)
	}
	writeError(c, status, code, message)
}
