package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"edo/internal/kube"
)

func (h clusterHandler) kubernetesConnectionStatuses(c *gin.Context) {
	ids := []string{}
	if raw := strings.TrimSpace(c.Query("ids")); raw != "" {
		ids = strings.Split(raw, ",")
		valid := len(ids) <= 500
		for i := range ids {
			ids[i] = strings.TrimSpace(ids[i])
			if _, err := uuid.Parse(ids[i]); err != nil {
				valid = false
			}
		}
		if !valid {
			h.logger.Warn("集群连接状态查询参数无效", "operation", "kubernetes_connection_status_bind", "request_id", requestIDFrom(c), "err", kube.ErrInvalidCluster)
			writeError(c, http.StatusBadRequest, "invalid_request", "请选择有效的集群后重试")
			return
		}
	}
	statuses, err := h.kube.ConnectionStatuses(c.Request.Context(), ids, c.Query("refresh") == "true")
	if err != nil {
		h.logger.Error("查询 Kubernetes 集群连接状态失败", "operation", "kubernetes_connection_status", "request_id", requestIDFrom(c), "err", err)
		if errors.Is(err, kube.ErrRBACForbidden) {
			writeError(c, http.StatusForbidden, "kubernetes_forbidden", "无权查看集群连接状态")
		} else if errors.Is(err, kube.ErrConnectionStatusLimit) {
			writeError(c, http.StatusBadRequest, "invalid_request", kube.ErrConnectionStatusLimit.Error())
		} else {
			writeInternalError(c)
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"statuses": statuses})
}
