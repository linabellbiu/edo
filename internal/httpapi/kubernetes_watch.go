package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"time"

	"edo/internal/access"
	"edo/internal/kube"
	"edo/internal/model"
	"github.com/gin-gonic/gin"
	"k8s.io/apimachinery/pkg/util/validation"
)

type kubernetesWatchService interface {
	SubscribeResourceChanges(context.Context, string, []string, map[string]bool) (<-chan kube.ResourceChange, error)
	Find(context.Context, string) (*model.KubernetesCluster, error)
}

type kubernetesWatchHandler struct {
	service       kubernetesWatchService
	access        *access.Service
	logger        *slog.Logger
	authorization func(*gin.Context, ...string) func() bool
	heartbeat     time.Duration
}

func streamKubernetesResources(deps Dependencies) gin.HandlerFunc {
	return (kubernetesWatchHandler{
		service: deps.Kubernetes, access: deps.Access, logger: deps.Logger,
		authorization: func(c *gin.Context, permissions ...string) func() bool {
			return streamAuthorization(deps, c, permissions...)
		},
	}).stream
}

func kubernetesReadContext(c *gin.Context) context.Context {
	ctx := c.Request.Context()
	if c.Query("fresh") == "1" {
		return kube.WithFreshResourceRead(ctx)
	}
	return ctx
}

// Read permissions are derived from the same resource registry as the proxy.
// Keeping the permission map per request also prevents a privileged subscriber's
// notifications from leaking through a shared informer to another subscriber.
func kubernetesReadAccess(ctx context.Context, service *access.Service, user *model.User) (map[string]bool, error) {
	allowed := map[string]bool{}
	for _, resource := range kube.ResourceDefinitions() {
		permission := resource.Permission + ".read"
		if _, checked := allowed[permission]; checked {
			continue
		}
		value, err := service.HasPermission(ctx, user, permission)
		if err != nil {
			return nil, err
		}
		allowed[permission] = value
	}
	return allowed, nil
}

func (h kubernetesWatchHandler) stream(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		writeError(c, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	namespaces := c.QueryArray("namespace")
	if len(namespaces) > 32 {
		clusterHandler{logger: h.logger}.resourceError(c, "kubernetes_watch_namespace", kube.ErrInvalidResource)
		return
	}
	for _, namespace := range namespaces {
		if namespace != "*" && len(validation.IsDNS1123Label(namespace)) != 0 {
			clusterHandler{logger: h.logger}.resourceError(c, "kubernetes_watch_namespace", kube.ErrInvalidResource)
			return
		}
	}
	allowed, err := kubernetesReadAccess(c.Request.Context(), h.access, user)
	if err != nil {
		clusterHandler{logger: h.logger}.resourceError(c, "kubernetes_watch_permissions", err)
		return
	}
	permissions := []string{}
	for permission, value := range allowed {
		if value {
			permissions = append(permissions, permission)
		}
	}
	if len(permissions) == 0 {
		writeError(c, http.StatusForbidden, "forbidden", "没有查看集群资源的权限")
		return
	}
	sort.Strings(permissions)
	// The shared long-lived-session guard validates login, current identity and
	// project membership. Check every original read permission as well: losing
	// only one category must not leave the original subscription authorized.
	sessionAuthorized := h.authorization(c, permissions...)
	authorized := func() bool {
		if !sessionAuthorized() {
			return false
		}
		for _, permission := range permissions {
			value, err := h.access.HasPermission(c.Request.Context(), user, permission)
			if err != nil || !value {
				return false
			}
		}
		return true
	}
	if !authorized() {
		writeError(c, http.StatusForbidden, "forbidden", "集群资源查看授权已失效")
		return
	}
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	changes, err := h.service.SubscribeResourceChanges(ctx, c.Param("id"), namespaces, allowed)
	if err != nil {
		clusterHandler{logger: h.logger}.resourceError(c, "kubernetes_watch_subscribe", err)
		return
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	controller := http.NewResponseController(c.Writer)
	write := func(event string, payload any) bool {
		if !authorized() {
			return false
		}
		if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := c.Writer.Write([]byte("event: " + event + "\ndata: " + string(data) + "\n\n")); err != nil {
			return false
		}
		c.Writer.Flush()
		return true
	}
	// "ready" only acknowledges the SSE transport. A reset/change from the
	// subscriber asks the browser to read again after informer synchronization.
	if !write("ready", struct{}{}) {
		return
	}
	interval := h.heartbeat
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case change, open := <-changes:
			if !open {
				return
			}
			if slices.Contains([]string{"reset", "degraded", "synced"}, change.Type) {
				change = kube.ResourceChange{Type: change.Type}
			} else {
				if !slices.Contains([]string{"ADDED", "MODIFIED", "DELETED"}, change.Type) {
					continue
				}
				resource, known := kube.ResourceType(change.Resource)
				if !known || !allowed[resource.Permission+".read"] ||
					(resource.Namespaced && len(namespaces) > 0 && !slices.Contains(namespaces, "*") && !slices.Contains(namespaces, change.Namespace)) {
					continue
				}
			}
			if !write("change", change) {
				return
			}
		case <-ticker.C:
			// Resolve scope and enabled state afresh even on an idle stream;
			// informer connection invalidation is also handled by the service.
			cluster, err := h.service.Find(ctx, c.Param("id"))
			if err != nil || !cluster.IsActive || !write("ping", struct{}{}) {
				return
			}
		}
	}
}
