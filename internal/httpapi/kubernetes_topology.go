package httpapi

import (
	"errors"
	"net/http"

	"edo/internal/access"
	"edo/internal/kube"
	"github.com/gin-gonic/gin"
)

func (h clusterHandler) topology(service *access.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := currentUser(c)
		if !ok {
			writeError(c, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		allowed := map[string]bool{}
		for _, permission := range []string{"kubernetes.node.read", "kubernetes.workload.read", access.PermissionClusterRead, access.PermissionDeploymentRead} {
			value, err := service.HasPermission(c.Request.Context(), user, permission)
			if err != nil {
				h.resourceError(c, "kubernetes_topology_permission", err)
				return
			}
			allowed[permission] = value
		}
		if !allowed["kubernetes.node.read"] {
			h.resourceError(c, "kubernetes_topology_permission", kube.ErrResourceForbidden)
			return
		}
		result, err := h.kube.NodeTopology(c.Request.Context(), c.Param("id"), allowed, allowed[access.PermissionClusterRead] || allowed[access.PermissionDeploymentRead])
		if err != nil {
			if errors.Is(err, kube.ErrTopologyIncomplete) {
				writeError(c, http.StatusBadGateway, "kubernetes_topology_incomplete", kube.ErrTopologyIncomplete.Error())
				return
			}
			h.resourceError(c, "kubernetes_topology", err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, result)
	}
}
