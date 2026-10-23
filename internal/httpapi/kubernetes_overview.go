package httpapi

import (
	"net/http"

	"edo/internal/access"
	"edo/internal/kube"
	"github.com/gin-gonic/gin"
)

func (h clusterHandler) overview(service *access.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := currentUser(c)
		if !ok {
			h.logger.Error("集群统计缺少当前用户", "operation", "kubernetes_overview")
			writeInternalError(c)
			return
		}
		allowed := map[string]bool{}
		for _, category := range []string{"node", "workload", "config", "storage", "network"} {
			permission := "kubernetes." + category + ".read"
			value, err := service.HasPermission(c.Request.Context(), user, permission)
			if err != nil {
				h.logger.Error("查询集群统计权限失败", "operation", "kubernetes_overview_permission", "user_id", user.ID, "err", err)
				writeInternalError(c)
				return
			}
			allowed[permission] = value
		}
		result, err := h.kube.Overview(c.Request.Context(), c.Param("id"), c.Query("namespace"), allowed)
		if err != nil {
			h.resourceError(c, "kubernetes_overview", err)
			return
		}
		for resource, err := range result.Errors {
			h.logger.Warn("集群部分统计不可用", "operation", "kubernetes_overview", "cluster_id", c.Param("id"), "resource", resource, "err", err)
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, result)
	}
}

func (h clusterHandler) nodeJoin(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	var input kube.NodeJoinInput
	if err := c.ShouldBindJSON(&input); err != nil {
		h.resourceError(c, "kubernetes_node_join_bind", kube.ErrInvalidResource)
		return
	}
	result, err := h.kube.CreateNodeJoin(c.Request.Context(), c.Param("id"), input)
	if err != nil {
		h.resourceError(c, "kubernetes_node_join", err)
		return
	}
	setAuditResourceID(c, c.Param("id")+"/node-joins/"+result.ID)
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, result)
}
func (h clusterHandler) nodeJoinRevoke(c *gin.Context) {
	var input kube.ResourceIdentity
	if err := c.ShouldBindJSON(&input); err != nil {
		h.resourceError(c, "kubernetes_node_join_revoke_bind", kube.ErrInvalidResource)
		return
	}
	if err := h.kube.RevokeNodeJoin(c.Request.Context(), c.Param("id"), c.Param("join"), input); err != nil {
		h.resourceError(c, "kubernetes_node_join_revoke", err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h clusterHandler) nodeDelete(c *gin.Context) {
	var input kube.NodeRemoval
	if err := c.ShouldBindJSON(&input); err != nil {
		h.resourceError(c, "kubernetes_node_remove_bind", kube.ErrInvalidResource)
		return
	}
	if err := h.kube.DeleteNode(c.Request.Context(), c.Param("id"), c.Param("name"), input); err != nil {
		h.resourceError(c, "kubernetes_node_remove", err)
		return
	}
	setAuditResourceID(c, c.Param("id")+"/nodes/"+c.Param("name"))
	c.Status(http.StatusNoContent)
}
