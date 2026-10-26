package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/kube"
	"edo/internal/model"
	"github.com/gin-gonic/gin"
)

type kubernetesWatchStub struct {
	changes   <-chan kube.ResourceChange
	subscribe func(context.Context, string, []string, map[string]bool) error
	find      func(context.Context, string) (*model.KubernetesCluster, error)
}

func (s kubernetesWatchStub) SubscribeResourceChanges(ctx context.Context, id string, namespaces []string, allowed map[string]bool) (<-chan kube.ResourceChange, error) {
	if s.subscribe != nil {
		if err := s.subscribe(ctx, id, namespaces, allowed); err != nil {
			return nil, err
		}
	}
	return s.changes, nil
}

func (s kubernetesWatchStub) Find(ctx context.Context, id string) (*model.KubernetesCluster, error) {
	if s.find != nil {
		return s.find(ctx, id)
	}
	return &model.KubernetesCluster{ID: id, IsActive: true}, nil
}

type kubernetesWatchRecorder struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
}

func (w *kubernetesWatchRecorder) Flush() {
	w.ResponseRecorder.Flush()
	select {
	case w.flushed <- struct{}{}:
	default:
	}
}

func watchTestContext(f *streamBoundaryFixture, path string) (*gin.Context, *kubernetesWatchRecorder, context.CancelFunc) {
	w := &kubernetesWatchRecorder{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{}, 8)}
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithCancel(f.scope())
	c.Request = httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	c.Request.AddCookie(f.cookie)
	c.Set(currentUserKey, f.user)
	c.Params = gin.Params{{Key: "id", Value: "cluster"}}
	return c, w, cancel
}

func watchTestHandler(f *streamBoundaryFixture, service kubernetesWatchService) kubernetesWatchHandler {
	return kubernetesWatchHandler{
		service: service, access: f.deps.Access, logger: f.deps.Logger,
		authorization: func(c *gin.Context, permissions ...string) func() bool {
			return streamAuthorization(f.deps, c, permissions...)
		},
	}
}

func TestKubernetesWatchFiltersResourcePermissionsAndNamespaceMetadata(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	if err := f.deps.Access.SetUserPermissions(context.Background(), f.user.ID, access.UserPermissionOverrides{Deny: []string{
		"kubernetes.node.read", "kubernetes.config.read", "kubernetes.storage.read", "kubernetes.network.read",
	}}); err != nil {
		t.Fatal(err)
	}
	changes := make(chan kube.ResourceChange, 7)
	changes <- kube.ResourceChange{Resource: "pods", Namespace: "team", Name: "visible-pod", Type: "MODIFIED"}
	changes <- kube.ResourceChange{Resource: "pods", Namespace: "private", Name: "private-pod", Type: "ADDED"}
	changes <- kube.ResourceChange{Resource: "secrets", Namespace: "team", Name: "private-secret", Type: "MODIFIED"}
	changes <- kube.ResourceChange{Resource: "nodes", Name: "private-node", Type: "DELETED"}
	changes <- kube.ResourceChange{Type: "reset"}
	changes <- kube.ResourceChange{Type: "degraded", Name: "private-control-identity"}
	changes <- kube.ResourceChange{Type: "synced", Name: "private-control-identity"}
	close(changes)
	service := kubernetesWatchStub{changes: changes, subscribe: func(ctx context.Context, id string, namespaces []string, allowed map[string]bool) error {
		scope, _ := database.DepartmentScopeFromContext(ctx)
		if scope.ProjectID != f.project.ID || id != "cluster" || !reflect.DeepEqual(namespaces, []string{"team"}) {
			t.Fatal("订阅未继承当前项目、集群或命名空间")
		}
		if !allowed["kubernetes.workload.read"] || allowed["kubernetes.config.read"] || allowed["kubernetes.node.read"] {
			t.Fatal("订阅未使用当前资源权限", allowed)
		}
		return nil
	}}
	c, response, cancel := watchTestContext(f, "/api/v1/kubernetes/clusters/cluster/watch?namespace=team")
	defer cancel()
	watchTestHandler(f, service).stream(c)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatal("资源变更流未禁用缓存或未使用SSE", response.Code, response.Header())
	}
	if !strings.Contains(body, "event: ready") || !strings.Contains(body, "visible-pod") || !strings.Contains(body, `"type":"reset"`) || !strings.Contains(body, `"type":"degraded"`) || !strings.Contains(body, `"type":"synced"`) {
		t.Fatal("流缺少就绪、可见资源或重新同步事件", body)
	}
	if strings.Contains(body, "private-") || strings.Contains(body, "data\":") || strings.Contains(body, "kubeconfig") {
		t.Fatal("流包含范围外或敏感内容", body)
	}
}

func TestKubernetesWatchClosesAfterPartialPermissionOrSessionRevocation(t *testing.T) {
	for _, test := range []struct {
		name   string
		revoke func(*testing.T, *streamBoundaryFixture)
	}{
		{"只撤销一类资源读取", func(t *testing.T, f *streamBoundaryFixture) {
			if err := f.deps.Access.SetUserPermissions(context.Background(), f.user.ID, access.UserPermissionOverrides{Deny: []string{"kubernetes.workload.read"}}); err != nil {
				t.Fatal(err)
			}
		}},
		{"会话注销", func(t *testing.T, f *streamBoundaryFixture) {
			if err := f.deps.Sessions.Delete(context.Background(), f.cookie.Value); err != nil {
				t.Fatal(err)
			}
		}},
		{"项目成员被移除", removeStreamBoundaryMembership},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newStreamBoundaryFixture(t)
			changes := make(chan kube.ResourceChange, 1)
			c, response, cancel := watchTestContext(f, "/api/v1/kubernetes/clusters/cluster/watch")
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); watchTestHandler(f, kubernetesWatchStub{changes: changes}).stream(c) }()
			select {
			case <-response.flushed:
			case <-time.After(3 * time.Second):
				t.Fatal("SSE未建立")
			}
			test.revoke(t, f)
			changes <- kube.ResourceChange{Resource: "pods", Namespace: "team", Name: "after-revocation", Type: "MODIFIED"}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("撤权后资源连接仍未关闭")
			}
			if strings.Contains(response.Body.String(), "after-revocation") {
				t.Fatal("撤权后仍发送资源元数据", response.Body.String())
			}
		})
	}
}

func TestKubernetesWatchCancelsSubscriptionOnClientDisconnect(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	c, response, cancel := watchTestContext(f, "/api/v1/kubernetes/clusters/cluster/watch")
	defer cancel()
	var subscription context.Context
	service := kubernetesWatchStub{changes: make(chan kube.ResourceChange), subscribe: func(ctx context.Context, _ string, _ []string, _ map[string]bool) error {
		subscription = ctx
		return nil
	}}
	done := make(chan struct{})
	go func() { defer close(done); watchTestHandler(f, service).stream(c) }()
	select {
	case <-response.flushed:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE未建立")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("客户端断开后资源订阅仍未退出")
	}
	if subscription == nil || subscription.Err() != context.Canceled {
		t.Fatal("订阅未继承客户端取消")
	}
}

func TestKubernetesWatchRejectsInvalidNamespaceAndCrossProjectSubscription(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	for _, query := range []string{"namespace=../private", "namespace=", strings.Repeat("namespace=team&", 33)} {
		c, response, cancel := watchTestContext(f, "/api/v1/kubernetes/clusters/cluster/watch?"+query)
		handler := watchTestHandler(f, kubernetesWatchStub{subscribe: func(context.Context, string, []string, map[string]bool) error {
			t.Fatal("非法命名空间启动了资源订阅")
			return nil
		}})
		handler.stream(c)
		cancel()
		if response.Code != http.StatusBadRequest {
			t.Fatal("非法命名空间未被拒绝", response.Code, response.Body.String())
		}
	}
	c, response, cancel := watchTestContext(f, "/api/v1/kubernetes/clusters/cluster/watch")
	defer cancel()
	watchTestHandler(f, kubernetesWatchStub{subscribe: func(context.Context, string, []string, map[string]bool) error {
		return kube.ErrClusterNotFound
	}}).stream(c)
	if response.Code != http.StatusNotFound || strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatal("项目范围外集群启动了SSE", response.Code, response.Body.String())
	}
}

func TestKubernetesWatchIdleHeartbeatRechecksClusterEnabledState(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	c, response, cancel := watchTestContext(f, "/api/v1/kubernetes/clusters/cluster/watch")
	defer cancel()
	var checks atomic.Int32
	handler := watchTestHandler(f, kubernetesWatchStub{
		changes: make(chan kube.ResourceChange),
		find: func(ctx context.Context, id string) (*model.KubernetesCluster, error) {
			checks.Add(1)
			scope, _ := database.DepartmentScopeFromContext(ctx)
			if scope.ProjectID != f.project.ID || id != "cluster" {
				t.Error("空闲连接检查丢失项目范围")
			}
			return &model.KubernetesCluster{ID: id, IsActive: false}, nil
		},
	})
	handler.heartbeat = 10 * time.Millisecond
	done := make(chan struct{})
	go func() { defer close(done); handler.stream(c) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("集群停用后空闲连接未关闭")
	}
	if checks.Load() != 1 || !strings.Contains(response.Body.String(), "event: ready") || strings.Contains(response.Body.String(), "event: ping") {
		t.Fatal("空闲连接继续推送停用集群事件", response.Body.String())
	}
}

func TestKubernetesWatchRouteRequiresSessionAndResourceReadPermission(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	if err := f.deps.Access.SetUserPermissions(context.Background(), f.user.ID, access.UserPermissionOverrides{Deny: []string{
		"kubernetes.node.read", "kubernetes.workload.read", "kubernetes.config.read", "kubernetes.storage.read", "kubernetes.network.read",
	}}); err != nil {
		t.Fatal(err)
	}
	for _, authenticated := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/kubernetes/clusters/cluster/watch", nil)
		request.Header.Set("X-EDO-Project-ID", f.project.ID)
		want := http.StatusUnauthorized
		if authenticated {
			request.AddCookie(f.cookie)
			want = http.StatusForbidden
		}
		response := httptest.NewRecorder()
		f.router.ServeHTTP(response, request)
		if response.Code != want || strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
			t.Fatal("无会话或仅集群读取权限建立了资源流", response.Code, response.Body.String())
		}
	}
}
