package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"edo/internal/access"
	"edo/internal/kube"
	"github.com/gin-gonic/gin"
)

func requireKubernetesResource(service *access.Service, logger *slog.Logger, action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		def, ok := kube.ResourceType(c.Param("resource"))
		if !ok {
			logger.Warn("Kubernetes 资源类型无效", "operation", "kubernetes_resource_permission", "resource", c.Param("resource"), "request_id", requestIDFrom(c))
			writeError(c, http.StatusBadRequest, "invalid_kubernetes_resource", kube.ErrInvalidResource.Error())
			c.Abort()
			return
		}
		permission := def.Permission + "." + action
		setAuditResourceID(c, c.Param("id")+"/"+c.Param("resource")+"/"+c.Query("namespace")+"/"+c.Param("name"))
		if action == "execute" && def.Resource == "nodes" {
			permission = "kubernetes.node.update"
		}
		requirePermission(service, logger, permission)(c)
	}
}

func (h clusterHandler) resourceList(c *gin.Context) {
	result, err := h.kube.ListResources(kubernetesReadContext(c), c.Param("id"), c.Param("resource"), c.Query("namespace"), c.Query("continue"))
	if err != nil {
		h.resourceError(c, "kubernetes_resource_list", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h clusterHandler) resourceGet(c *gin.Context) { h.getKubernetesResource(c, false) }
func (h clusterHandler) resourceReveal(c *gin.Context) {
	if c.Param("resource") != "secrets" {
		h.resourceError(c, "kubernetes_secret_reveal", kube.ErrInvalidResource)
		return
	}
	h.getKubernetesResource(c, true)
}
func (h clusterHandler) getKubernetesResource(c *gin.Context, reveal bool) {
	result, err := h.kube.GetResource(c.Request.Context(), c.Param("id"), c.Param("resource"), c.Query("namespace"), c.Param("name"), reveal)
	if err != nil {
		h.resourceError(c, "kubernetes_resource_get", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"object": result})
}

func (h clusterHandler) resourceSave(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2*1024*1024)
	var object map[string]any
	if err := c.ShouldBindJSON(&object); err != nil {
		h.resourceError(c, "kubernetes_resource_bind", kube.ErrInvalidResource)
		return
	}
	result, err := h.kube.SaveResource(c.Request.Context(), c.Param("id"), c.Param("resource"), c.Query("namespace"), c.Param("name"), object, c.Request.Method == http.MethodPost)
	if err != nil {
		h.resourceError(c, "kubernetes_resource_save", err)
		return
	}
	setAuditResourceID(c, c.Param("id")+"/"+c.Param("resource")+"/"+c.Query("namespace")+"/"+c.Param("name"))
	status := http.StatusOK
	if c.Request.Method == http.MethodPost {
		status = http.StatusCreated
	}
	c.JSON(status, gin.H{"object": result})
}

func (h clusterHandler) resourceDelete(c *gin.Context) {
	var identity kube.ResourceIdentity
	if err := c.ShouldBindJSON(&identity); err != nil {
		h.resourceError(c, "kubernetes_resource_delete_bind", kube.ErrInvalidResource)
		return
	}
	if err := h.kube.DeleteResource(c.Request.Context(), c.Param("id"), c.Param("resource"), c.Query("namespace"), c.Param("name"), identity); err != nil {
		h.resourceError(c, "kubernetes_resource_delete", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h clusterHandler) resourceValidate(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2*1024*1024)
	var object map[string]any
	if err := c.ShouldBindJSON(&object); err != nil {
		h.logger.Warn("Kubernetes 校验请求解析失败", "operation", "kubernetes_resource_validate_bind", "cluster_id", c.Param("id"), "resource", c.Param("resource"), "name", c.Param("name"), "request_id", requestIDFrom(c), "err", err)
		h.resourceError(c, "kubernetes_resource_validate_bind", kube.ErrInvalidResource)
		return
	}
	if err := h.kube.ValidateResource(c.Request.Context(), c.Param("id"), c.Param("resource"), c.Query("namespace"), c.Param("name"), object); err != nil {
		h.resourceError(c, "kubernetes_resource_validate", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	// 校验响应不返回资源，避免通过更新权限读取 Secret。
	c.JSON(http.StatusOK, gin.H{"valid": true})
}

func (h clusterHandler) resourceValidateCreate(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2*1024*1024)
	var object map[string]any
	if err := c.ShouldBindJSON(&object); err != nil {
		h.logger.Warn("Kubernetes 新建校验请求解析失败", "operation", "kubernetes_resource_create_validate_bind", "cluster_id", c.Param("id"), "resource", c.Param("resource"), "name", c.Param("name"), "request_id", requestIDFrom(c), "err", err)
		h.resourceError(c, "kubernetes_resource_create_validate_bind", kube.ErrInvalidResource)
		return
	}
	if err := h.kube.ValidateResourceCreate(c.Request.Context(), c.Param("id"), c.Param("resource"), c.Query("namespace"), c.Param("name"), object); err != nil {
		h.resourceError(c, "kubernetes_resource_create_validate", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	// 创建校验只返回结果，不回显集群默认化后的对象或 Secret 内容。
	c.JSON(http.StatusOK, gin.H{"valid": true})
}

func (h clusterHandler) resourceAction(c *gin.Context) {
	var input kube.ResourceAction
	if err := c.ShouldBindJSON(&input); err != nil {
		h.resourceError(c, "kubernetes_resource_action_bind", kube.ErrInvalidResource)
		return
	}
	result, err := h.kube.ResourceAction(c.Request.Context(), c.Param("id"), c.Param("resource"), c.Query("namespace"), c.Param("name"), input)
	if err != nil {
		h.resourceError(c, "kubernetes_resource_action", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"object": result})
}

func (h clusterHandler) resourceEvents(c *gin.Context) {
	if c.Param("resource") == "secrets" {
		h.resourceError(c, "kubernetes_secret_events", kube.ErrInvalidResource)
		return
	}
	result, err := h.kube.ResourceEvents(c.Request.Context(), c.Param("id"), c.Param("resource"), c.Query("namespace"), c.Param("name"), c.Query("uid"))
	if err != nil {
		h.resourceError(c, "kubernetes_resource_events", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": result})
}

func (h clusterHandler) resourceError(c *gin.Context, operation string, err error) {
	if writeKubernetesAuthorizationError(c, err) {
		return
	}
	h.logger.Error("Kubernetes 资源操作失败", "operation", operation, "cluster_id", c.Param("id"), "resource", c.Param("resource"), "namespace", c.Query("namespace"), "name", c.Param("name"), "request_id", requestIDFrom(c), "err", err)
	status, code, message := http.StatusBadGateway, "kubernetes_unavailable", kube.ErrClusterUnreachable.Error()
	switch {
	case errors.Is(err, kube.ErrNodeHistoryRange):
		status, code, message = http.StatusBadRequest, "invalid_node_metrics_range", kube.ErrNodeHistoryRange.Error()
	case errors.Is(err, kube.ErrNodeRemovalUnsafe), errors.Is(err, kube.ErrJoinUnsupported):
		status, code, message = http.StatusBadRequest, "kubernetes_node_operation_blocked", err.Error()
	case errors.Is(err, kube.ErrInvalidResource), errors.Is(err, kube.ErrResourceProtected):
		status, code, message = http.StatusBadRequest, "invalid_kubernetes_resource", kube.ErrInvalidResource.Error()
		if errors.Is(err, kube.ErrResourceProtected) {
			message = kube.ErrResourceProtected.Error()
		}
	case errors.Is(err, kube.ErrResourceNotFound), errors.Is(err, kube.ErrClusterNotFound):
		status, code, message = http.StatusNotFound, "kubernetes_resource_not_found", kube.ErrResourceNotFound.Error()
	case errors.Is(err, kube.ErrResourceConflict):
		status, code, message = http.StatusConflict, "kubernetes_resource_conflict", kube.ErrResourceConflict.Error()
	case errors.Is(err, kube.ErrResourceForbidden):
		status, code, message = http.StatusForbidden, "kubernetes_resource_forbidden", kube.ErrResourceForbidden.Error()
	}
	writeError(c, status, code, message)
}
