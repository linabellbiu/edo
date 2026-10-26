package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	"gorm.io/gorm"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	clientspdy "k8s.io/client-go/transport/spdy"
	streamspdy "k8s.io/streaming/pkg/httpstream/spdy"
)

type identityTestPolicies struct {
	mu       sync.RWMutex
	enabled  bool
	revoked  map[string]bool
	policies map[string]*clusterAuthorization
}

func identityResourceTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return resourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			if request.Header.Get("Impersonate-User") == "" {
				t.Error("native review did not use the actual impersonated principal")
			}
			_, _ = io.WriteString(w, `{"apiVersion":"authorization.k8s.io/v1","kind":"SelfSubjectAccessReview","status":{"allowed":true}}`)
			return
		}
		handler(w, request)
	})
}

func identityStartResourceWatching(t *testing.T, service *Service) {
	t.Helper()
	startResourceWatchTest(t, service)
	deadline := time.Now().Add(time.Second)
	for {
		service.queries.mu.Lock()
		ready := service.queries.ctx != nil
		service.queries.mu.Unlock()
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("resource cache lifecycle did not start")
		}
		time.Sleep(time.Millisecond)
	}
}

func decodeIdentityReview(t *testing.T, request *http.Request) *authorizationv1.SelfSubjectAccessReview {
	t.Helper()
	encoded, err := io.ReadAll(request.Body)
	if err != nil {
		t.Error(err)
		return &authorizationv1.SelfSubjectAccessReview{}
	}
	object, _, err := scheme.Codecs.UniversalDeserializer().Decode(encoded, nil, nil)
	if err != nil {
		t.Errorf("decode actual Kubernetes review request: %v", err)
		return &authorizationv1.SelfSubjectAccessReview{}
	}
	result, ok := object.(*authorizationv1.SelfSubjectAccessReview)
	if !ok {
		t.Errorf("unexpected authorization object: %T", object)
		return &authorizationv1.SelfSubjectAccessReview{}
	}
	return result
}

func writeIdentityReview(w http.ResponseWriter, allowed bool) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(&authorizationv1.SelfSubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SelfSubjectAccessReview"}, Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed, Denied: !allowed}})
}

func newIdentityTestPolicies() *identityTestPolicies {
	return &identityTestPolicies{enabled: true, revoked: map[string]bool{}, policies: map[string]*clusterAuthorization{}}
}

func (p *identityTestPolicies) grant(user string, permissions map[string][]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	namespaces := []string{}
	for namespace := range permissions {
		if namespace != "" {
			namespaces = append(namespaces, namespace)
		}
	}
	slices.Sort(namespaces)
	p.policies[user] = &clusterAuthorization{Enforced: true, CacheKey: "project/" + user, Version: "version-1", Principal: "edo:project:" + user + ":version-1", Namespaces: namespaces, ClusterScope: len(permissions[""]) > 0, Permissions: permissions}
}

func (p *identityTestPolicies) resolve(ctx context.Context, _ *model.KubernetesCluster, _ bool) (*clusterAuthorization, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.enabled {
		return &clusterAuthorization{Enforced: false}, nil
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	if scope.UserID == "" || p.revoked[scope.UserID] || p.policies[scope.UserID] == nil {
		return nil, ErrRBACForbidden
	}
	return cloneClusterAuthorization(p.policies[scope.UserID]), nil
}

func identityTestContext(t *testing.T, service *Service, clusterID, user string) context.Context {
	t.Helper()
	cluster, err := service.Find(context.Background(), clusterID)
	if err != nil {
		t.Fatal(err)
	}
	return database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: cluster.ProjectID, DepartmentID: cluster.DepartmentID, UserID: user})
}

func identityTestConfigMap(name, owner string) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": name, "namespace": "default", "uid": "uid-" + name, "resourceVersion": "1"}, "data": map[string]any{"owner": owner}}
}

func identityWaitForCachedList(t *testing.T, service *Service, ctx context.Context, clusterID string, fixture *resourceWatchFixture, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		before := fixture.listCalls.Load()
		list, err := service.ListResources(ctx, clusterID, "configmaps", "default", "")
		if err == nil && len(list.Items) == 1 && list.Items[0]["metadata"].(map[string]any)["name"] == name && fixture.watchCalls.Load() > 0 && fixture.listCalls.Load() == before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("identity-specific informer snapshot did not become available")
}

func TestRBACIdentitySeparatesClientsWatchStoresAndMetadataFences(t *testing.T) {
	a := newResourceWatchFixture(t, "configmaps", "ConfigMap", identityTestConfigMap("only-a", "alice"))
	b := newResourceWatchFixture(t, "configmaps", "ConfigMap", identityTestConfigMap("only-b", "bob"))
	server := identityResourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		switch request.Header.Get("Impersonate-User") {
		case "edo:project:alice:version-1":
			a.serveHTTP(w, request)
		case "edo:project:bob:version-1":
			b.serveHTTP(w, request)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"default": {"kubernetes.config.read"}})
	policies.grant("bob", map[string][]string{"default": {"kubernetes.config.read"}})
	service.authorizationResolver = policies.resolve
	startResourceWatchTest(t, service)
	alice, bob := identityTestContext(t, service, id, "alice"), identityTestContext(t, service, id, "bob")
	identityWaitForCachedList(t, service, alice, id, a, "only-a")
	identityWaitForCachedList(t, service, bob, id, b, "only-b")
	qa, err := service.clusterQueries(alice, id)
	if err != nil {
		t.Fatal(err)
	}
	qb, err := service.clusterQueries(bob, id)
	if err != nil || qa == qb || qa.typed == qb.typed || qa.metadata == qb.metadata || qa.watchDynamic == qb.watchDynamic {
		t.Fatalf("identities share an upstream client: %v", err)
	}
	if a.barrierCalls.Load() == 0 || b.barrierCalls.Load() == 0 {
		t.Fatal("each identity must execute its own live metadata fence")
	}
	policies.mu.Lock()
	policies.revoked["alice"] = true
	policies.mu.Unlock()
	if _, err := service.ListResources(alice, id, "configmaps", "default", ""); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("revoked user received a cached result: %v", err)
	}
	select {
	case <-qa.ctx.Done():
	default:
		t.Fatal("revoked identity retained its informer lifetime")
	}
	identityWaitForCachedList(t, service, bob, id, b, "only-b")
}

func TestRBACOldClientCannotBypassRevocationOrForgeImpersonation(t *testing.T) {
	var calls atomic.Int32
	server := identityResourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get("Impersonate-User") != "edo:project:alice:version-1" || len(request.Header.Values("Impersonate-Group")) != 0 || request.Header.Get("Impersonate-Extra-admin") != "" {
			t.Errorf("untrusted impersonation reached Kubernetes: %v", request.Header)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web","namespace":"default"}}`)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"default": {"kubernetes.workload.read"}})
	service.authorizationResolver = policies.resolve
	ctx := identityTestContext(t, service, id, "alice")
	client, err := service.Clientset(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	request := client.CoreV1().RESTClient().Get().Namespace("default").Resource("pods").Name("web").SetHeader("Impersonate-User", "system:admin").SetHeader("Impersonate-Group", "system:masters").SetHeader("Impersonate-Extra-admin", "true")
	if err := request.Do(ctx).Error(); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	policies.mu.Lock()
	policies.revoked["alice"] = true
	policies.mu.Unlock()
	if _, err := client.CoreV1().Pods("default").Get(ctx, "web", metav1.GetOptions{}); err == nil || !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("previously obtained client retained authorization: %v", err)
	}
	if calls.Load() != before {
		t.Fatal("a revoked request reached Kubernetes")
	}
}

func TestRBACLeaseCancelsExistingConnectionWithoutAnotherRequest(t *testing.T) {
	server := newKubeVersionServer(t)
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"default": {"kubernetes.workload.read"}})
	service.authorizationResolver = policies.resolve
	ctx := identityTestContext(t, service, id, "alice")
	lease, cancel, err := service.WithClusterAuthorizationLease(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	policies.mu.Lock()
	policies.revoked["alice"] = true
	policies.mu.Unlock()
	select {
	case <-lease.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("revocation left an already-open stream authorized")
	}
}

func TestRBACEnableClosesPreviouslyCreatedLegacyClient(t *testing.T) {
	var anonymousCalls atomic.Int32
	server := identityResourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Impersonate-User") == "" {
			anonymousCalls.Add(1)
		}
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web","namespace":"default"}}`)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.enabled = false
	policies.grant("alice", map[string][]string{"default": {"kubernetes.workload.read"}})
	service.authorizationResolver = policies.resolve
	ctx := identityTestContext(t, service, id, "alice")
	legacy, err := service.Clientset(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.CoreV1().Pods("default").Get(ctx, "web", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
	policies.mu.Lock()
	policies.enabled = true
	policies.mu.Unlock()
	if _, err := legacy.CoreV1().Pods("default").Get(ctx, "web", metav1.GetOptions{}); err == nil {
		t.Fatal("enabling enforcement left the old administrator client usable")
	}
	if anonymousCalls.Load() != 1 {
		t.Fatal("administrator request escaped after enforcement was enabled")
	}
	fresh, err := service.Clientset(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.CoreV1().Pods("default").Get(ctx, "web", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestRBACMissingActorRequiresExplicitInternalPurpose(t *testing.T) {
	server := newKubeVersionServer(t)
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	service.authorizationResolver = newIdentityTestPolicies().resolve
	for _, ctx := range []context.Context{context.Background(), WithInternalClusterAccess(context.Background(), "request-says-admin")} {
		if _, err := service.Clientset(ctx, id); !errors.Is(err, ErrRBACForbidden) {
			t.Fatalf("missing identity became an administrator: %v", err)
		}
	}
	if _, err := service.Clientset(WithInternalClusterAccess(context.Background(), "node-monitor"), id); err != nil {
		t.Fatalf("explicit collector identity was rejected: %v", err)
	}
}

func TestRBACScopedPaginationNeverReadsGlobalCollectionAndBindsCursor(t *testing.T) {
	var requests atomic.Int32
	server := identityResourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		namespace := ""
		switch request.URL.Path {
		case "/api/v1/namespaces/alpha/configmaps":
			namespace = "alpha"
		case "/api/v1/namespaces/beta/configmaps":
			namespace = "beta"
		default:
			t.Errorf("escaped authorized namespace collections: %s", request.URL)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		items := []map[string]any{}
		continuation := ""
		if namespace == "alpha" && request.URL.Query().Get("continue") == "" {
			for index := range 500 {
				items = append(items, map[string]any{"metadata": map[string]any{"name": fmt.Sprintf("a-%03d", index), "namespace": namespace}})
			}
			continuation = "native-alpha-next"
		} else {
			items = append(items, map[string]any{"metadata": map[string]any{"name": namespace + "-last", "namespace": namespace}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "ConfigMapList", "metadata": map[string]any{"continue": continuation}, "items": items})
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	for _, user := range []string{"alice", "bob"} {
		policies.grant(user, map[string][]string{"alpha": {"kubernetes.config.read"}, "beta": {"kubernetes.config.read"}})
	}
	service.authorizationResolver = policies.resolve
	alice := identityTestContext(t, service, id, "alice")
	first, err := service.ListResources(alice, id, "configmaps", "*", "")
	if err != nil || len(first.Items) != 500 || !strings.HasPrefix(first.Continue, scopedContinuationPrefix) {
		t.Fatalf("invalid first scoped page: %+v %v", first, err)
	}
	second, err := service.ListResources(alice, id, "configmaps", "*", first.Continue)
	if err != nil || len(second.Items) != 2 || second.Continue != "" {
		t.Fatalf("native continuation was not preserved across scopes: %+v %v", second, err)
	}
	before := requests.Load()
	if _, err := service.ListResources(identityTestContext(t, service, id, "bob"), id, "configmaps", "*", first.Continue); !errors.Is(err, ErrInvalidResource) {
		t.Fatalf("another identity reused a continuation: %v", err)
	}
	if requests.Load() != before {
		t.Fatal("invalid identity continuation reached Kubernetes")
	}
}

func TestRBACNamespacesUseExactGetAndDoNotExposeCollectorHistory(t *testing.T) {
	var pathsMu sync.Mutex
	paths := []string{}
	server := identityResourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		pathsMu.Lock()
		paths = append(paths, request.URL.Path)
		pathsMu.Unlock()
		switch request.URL.Path {
		case "/api/v1/namespaces/team":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"team"}}`)
		case "/api/v1/nodes/worker":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Node","metadata":{"name":"worker","uid":"node-1"}}`)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"": {"kubernetes.node.read"}, "team": {"kubernetes.workload.read"}})
	service.authorizationResolver = policies.resolve
	ctx := identityTestContext(t, service, id, "alice")
	namespaces, err := service.Namespaces(ctx, id)
	if err != nil || !slices.Equal(namespaces, []string{"team"}) {
		t.Fatalf("namespace selector exposed an unauthorized scope: %v %v", namespaces, err)
	}
	testNodeMonitor(t, service)
	pods := int64(42)
	if err := service.nodeMonitor.append(context.Background(), id, "node-1", NodeSample{CollectedAt: time.Now().UTC(), Pods: &pods, Allocation: &NodeAllocation{CPURequests: 20}, PodsState: "ok"}); err != nil {
		t.Fatal(err)
	}
	history, err := service.NodeHistory(ctx, id, "worker", "node-1", time.Hour, true)
	if err != nil || len(history.Points) != 1 || history.Points[0].Pods != nil || history.Points[0].Allocation != nil {
		t.Fatalf("collector's global workload allocation leaked: %+v %v", history, err)
	}
	policies.mu.Lock()
	delete(policies.policies["alice"].Permissions, "")
	policies.mu.Unlock()
	if _, err := service.NodeHistory(ctx, id, "worker", "node-1", time.Hour, true); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("history bypassed the cluster-level node grant: %v", err)
	}
	pathsMu.Lock()
	defer pathsMu.Unlock()
	if slices.Contains(paths, "/api/v1/namespaces") {
		t.Fatal("namespace selector performed an unrestricted namespace list")
	}
}

func TestRBACUpstreamForbiddenDoesNotRetryAsAdministrator(t *testing.T) {
	var calls atomic.Int32
	server := identityResourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get("Impersonate-User") == "" {
			t.Error("forbidden user request fell back to the administrator credential")
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403}`)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"default": {"kubernetes.config.read"}})
	service.authorizationResolver = policies.resolve
	_, err := service.GetResource(identityTestContext(t, service, id, "alice"), id, "configmaps", "default", "denied", false)
	if !errors.Is(err, ErrResourceForbidden) || calls.Load() != 1 {
		t.Fatalf("forbidden handling changed identity or retried: calls=%d err=%v", calls.Load(), err)
	}
}

func TestRBACMetricsCacheUsesIdentityAndNamespaceCollections(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	server := identityResourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/apis/metrics.k8s.io/v1/namespaces/team/pods" {
			t.Errorf("metrics escaped the authorized namespace: %s", request.URL)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		principal := request.Header.Get("Impersonate-User")
		mu.Lock()
		calls[principal]++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "metrics.k8s.io/v1", "kind": "PodMetricsList", "items": []map[string]any{{"metadata": map[string]any{"name": principal, "namespace": "team"}, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "containers": []map[string]any{{"name": "app", "usage": map[string]any{"cpu": "100m", "memory": "1Mi"}}}}}})
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	for _, user := range []string{"alice", "bob"} {
		policies.grant(user, map[string][]string{"team": {"kubernetes.workload.read"}})
	}
	service.authorizationResolver = policies.resolve
	startResourceWatchTest(t, service)
	deadline := time.Now().Add(time.Second)
	for {
		service.queries.mu.Lock()
		ready := service.queries.ctx != nil
		service.queries.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resource cache lifecycle did not start")
		}
		time.Sleep(time.Millisecond)
	}
	for _, user := range []string{"alice", "bob", "alice"} {
		result, err := service.listMetrics(identityTestContext(t, service, id, user), id, "pods", "")
		if err != nil || len(result.Items) != 1 || result.Items[0].GetName() != "edo:project:"+user+":version-1" {
			t.Fatalf("metrics cache mixed identities: user=%s result=%v err=%v", user, result, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["edo:project:alice:version-1"] != 1 || calls["edo:project:bob:version-1"] != 1 {
		t.Fatalf("sample cache did not remain per identity: %+v", calls)
	}
}

func TestRBACConfigReaderCannotUseLegacySecretMetadataCache(t *testing.T) {
	fixture := newResourceWatchFixture(t, "secrets", "Secret", map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "private", "namespace": "default", "uid": "secret-1", "resourceVersion": "1"}, "data": map[string]any{"token": "cHJpdmF0ZQ=="}})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, fixture.server)
	policies := newIdentityTestPolicies()
	policies.enabled = false
	policies.grant("alice", map[string][]string{"default": {"kubernetes.config.read"}})
	service.authorizationResolver = policies.resolve
	ctx := identityTestContext(t, service, id, "alice")
	startResourceWatchTest(t, service)
	if _, err := service.ListResources(ctx, id, "secrets", "default", ""); err != nil {
		t.Fatal(err)
	}
	policies.mu.Lock()
	policies.enabled = true
	policies.mu.Unlock()
	if result, err := service.ListResources(ctx, id, "secrets", "default", ""); result != nil || !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("config.read inherited administrator Secret list access: %+v %v", result, err)
	}
}

func TestRBACRevocationClosesSubscriptionWhenInformersAreUnavailable(t *testing.T) {
	server := identityResourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"Forbidden","code":403}`)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"default": {"kubernetes.workload.read"}})
	service.authorizationResolver = policies.resolve
	identityStartResourceWatching(t, service)
	ctx := identityTestContext(t, service, id, "alice")
	events, err := service.SubscribeResourceChanges(ctx, id, []string{"default"}, map[string]bool{"kubernetes.workload.read": true})
	if err != nil {
		t.Fatal(err)
	}
	query, err := service.clusterQueries(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		query.mu.Lock()
		paused := len(query.watchers) > 0 && query.activeRequests == 0 && len(query.activeAccess) == 0
		for _, observer := range query.watchers {
			paused = paused && observer.failed.Load()
		}
		query.mu.Unlock()
		if paused {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the native resource informers did not enter the unavailable state")
		}
		time.Sleep(time.Millisecond)
	}
	policies.mu.Lock()
	policies.revoked["alice"] = true
	policies.mu.Unlock()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, open := <-events:
			if !open {
				return
			}
		case <-timer.C:
			t.Fatal("policy revocation left a paused resource subscription open")
		}
	}
}

func TestRBACNativeRevocationClosesCoalescedLogStreams(t *testing.T) {
	var revoked atomic.Bool
	var reviews atomic.Int32
	closed := make(chan struct{}, 2)
	server := resourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			check := decodeIdentityReview(t, request)
			attributes := check.Spec.ResourceAttributes
			if request.Header.Get("Impersonate-User") != "edo:project:alice:version-1" || attributes == nil || attributes.Namespace != "default" || attributes.Resource != "pods" || attributes.Subresource != "log" || attributes.Name != "web" || attributes.Verb != "get" {
				t.Errorf("native stream check used the wrong identity or operation: %+v", check.Spec)
			}
			reviews.Add(1)
			writeIdentityReview(w, !revoked.Load())
			return
		}
		if request.URL.Path == "/api/v1/namespaces/default/pods/web/log" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "hello\n")
			w.(http.Flusher).Flush()
			<-request.Context().Done()
			closed <- struct{}{}
			return
		}
		http.NotFound(w, request)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"default": {"kubernetes.workload.read"}})
	service.authorizationResolver = policies.resolve
	identityStartResourceWatching(t, service)
	ctx := identityTestContext(t, service, id, "alice")
	for range 2 {
		stream, err := service.PodLogs(ctx, id, "default", "web", "app", 100, true, true)
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		if _, err := io.ReadFull(stream, make([]byte, 6)); err != nil {
			t.Fatal(err)
		}
	}
	if len(service.queries.authorizationSlots) != 1 {
		t.Fatalf("identical principal/operation streams were not coalesced: %d", len(service.queries.authorizationSlots))
	}
	revoked.Store(true)
	for range 2 {
		select {
		case <-closed:
		case <-time.After(3 * time.Second):
			t.Fatal("native RBAC revocation left an existing log stream open")
		}
	}
	if reviews.Load() != 1 || len(service.queries.authorizationSlots) != 0 {
		t.Fatalf("native checks were not coalesced or released: calls=%d slots=%d", reviews.Load(), len(service.queries.authorizationSlots))
	}
}

func TestRBACNativeRevocationKeepsExecLeaseAfterSPDYHandshakeCloses(t *testing.T) {
	var revoked atomic.Bool
	var reviews atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			check := decodeIdentityReview(t, request)
			attributes := check.Spec.ResourceAttributes
			if request.Header.Get("Impersonate-User") != "edo:project:alice:version-1" || attributes == nil || attributes.Resource != "pods" || attributes.Namespace != "default" || attributes.Name != "web" || attributes.Subresource != "exec" || attributes.Verb != "create" {
				t.Errorf("upgraded exec lease did not retain its native operation: %+v", check.Spec)
			}
			reviews.Add(1)
			writeIdentityReview(w, !revoked.Load())
			return
		}
		if request.URL.Path == "/api/v1/namespaces/default/pods/web/exec" {
			w.Header().Set("X-Stream-Protocol-Version", "v4.channel.k8s.io")
			connection := streamspdy.NewResponseUpgrader().UpgradeResponse(w, request, nil)
			if connection != nil {
				defer connection.Close()
				<-connection.CloseChan()
			}
			return
		}
		http.NotFound(w, request)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"default": {"kubernetes.workload.read", "kubernetes.workload.update"}})
	service.authorizationResolver = policies.resolve
	identityStartResourceWatching(t, service)
	lease, cancel, err := service.WithClusterAuthorizationLease(identityTestContext(t, service, id, "alice"), id)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	configuration, err := service.RESTConfig(lease, id)
	if err != nil {
		t.Fatal(err)
	}
	transport, upgrader, err := clientspdy.RoundTripperFor(configuration)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(lease, http.MethodPost, server.URL+"/api/v1/namespaces/default/pods/web/exec", nil)
	if err != nil {
		t.Fatal(err)
	}
	connection, _, err := clientspdy.Negotiate(upgrader, &http.Client{Transport: transport}, request, "v4.channel.k8s.io")
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	// client-go has already closed the handshake response body. The upgraded
	// operation must still have its own permission lease until exec ends.
	if len(service.queries.authorizationSlots) != 1 {
		t.Fatal("closing the handshake response dropped the upgraded exec lease")
	}
	revoked.Store(true)
	select {
	case <-lease.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("native revocation left an upgraded exec lease valid")
	}
	if reviews.Load() != 1 {
		t.Fatalf("the upgraded exec operation did not get one deduplicated review: %d", reviews.Load())
	}
}

func TestRBACNativeRevocationRejectsMetricsSampleCache(t *testing.T) {
	var revoked atomic.Bool
	var samples, reviews atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			check := decodeIdentityReview(t, request)
			attributes := check.Spec.ResourceAttributes
			if request.Header.Get("Impersonate-User") != "edo:project:alice:version-1" || attributes == nil || attributes.Namespace != "team" || attributes.Group != "metrics.k8s.io" || attributes.Resource != "pods" || attributes.Verb != "list" {
				t.Errorf("cache review did not use the user's metrics scope: %+v", check.Spec)
			}
			reviews.Add(1)
			writeIdentityReview(w, !revoked.Load())
			return
		}
		if request.URL.Path == "/apis/metrics.k8s.io/v1/namespaces/team/pods" {
			samples.Add(1)
			_, _ = io.WriteString(w, `{"apiVersion":"metrics.k8s.io/v1","kind":"PodMetricsList","items":[{"metadata":{"name":"web","namespace":"team"}}]}`)
			return
		}
		http.NotFound(w, request)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"team": {"kubernetes.workload.read"}})
	service.authorizationResolver = policies.resolve
	identityStartResourceWatching(t, service)
	ctx := identityTestContext(t, service, id, "alice")
	if _, err := service.listMetrics(ctx, id, "pods", "team"); err != nil {
		t.Fatal(err)
	}
	if reviews.Load() != 0 {
		t.Fatal("ordinary native metrics request added an unnecessary review")
	}
	revoked.Store(true)
	if result, err := service.listMetrics(ctx, id, "pods", "team"); result != nil || !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("stale native permission returned cached metrics: %v %v", result, err)
	}
	if samples.Load() != 1 || reviews.Load() != 1 {
		t.Fatalf("cached response did not get its own current authorization fence: data=%d reviews=%d", samples.Load(), reviews.Load())
	}
}

func TestRBACNativeMetricsCacheHitsCannotShareAnOlderAuthorizationFence(t *testing.T) {
	firstReview := make(chan struct{})
	releaseFirst := make(chan struct{})
	defer close(releaseFirst)
	var reviews atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			_ = decodeIdentityReview(t, request)
			if reviews.Add(1) == 1 {
				// This review started before revocation but its response is delayed.
				// A later cache reader must not join this older authorization fence.
				close(firstReview)
				select {
				case <-request.Context().Done():
				case <-releaseFirst:
				}
				return
			}
			writeIdentityReview(w, false)
			return
		}
		if request.URL.Path == "/apis/metrics.k8s.io/v1/namespaces/team/pods" {
			_, _ = io.WriteString(w, `{"apiVersion":"metrics.k8s.io/v1","kind":"PodMetricsList","items":[{"metadata":{"name":"web","namespace":"team"}}]}`)
			return
		}
		http.NotFound(w, request)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"team": {"kubernetes.workload.read"}})
	service.authorizationResolver = policies.resolve
	identityStartResourceWatching(t, service)
	ctx, cancel := context.WithTimeout(identityTestContext(t, service, id, "alice"), 3*time.Second)
	defer cancel()
	if _, err := service.listMetrics(ctx, id, "pods", "team"); err != nil {
		t.Fatal(err)
	}
	firstResult := make(chan error, 1)
	go func() {
		_, err := service.listMetrics(ctx, id, "pods", "team")
		firstResult <- err
	}()
	select {
	case <-firstReview:
	case <-ctx.Done():
		t.Fatal("the first cache read never reached its native fence")
	}
	if result, err := service.listMetrics(ctx, id, "pods", "team"); result != nil || !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("later cache read reused an older authorization decision: %v %v", result, err)
	}
	if err := <-firstResult; !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("revocation did not also invalidate the older pending reader: %v", err)
	}
	if reviews.Load() != 2 {
		t.Fatalf("cache hits did not execute separate current native fences: %d", reviews.Load())
	}
}

func TestRBACNodeBootstrapChecksNativeScopeBeforeUsingControlPlaneIdentity(t *testing.T) {
	var revoked atomic.Bool
	var reviews, privilegedRequests atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			check := decodeIdentityReview(t, request)
			attributes := check.Spec.ResourceAttributes
			if request.Header.Get("Impersonate-User") != "edo:project:alice:version-1" || attributes == nil || attributes.Resource != "nodes" || attributes.Verb != "list" || attributes.Namespace != "" {
				t.Errorf("bootstrap did not check its actual compiled native prerequisite: %+v", check.Spec)
			}
			reviews.Add(1)
			writeIdentityReview(w, !revoked.Load())
			return
		}
		privilegedRequests.Add(1)
		http.NotFound(w, request)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"": {"kubernetes.node.create"}})
	service.authorizationResolver = policies.resolve
	ctx := identityTestContext(t, service, id, "alice")
	if err := service.RequireClusterPermission(ctx, id, "", "kubernetes.node.create"); err != nil {
		t.Fatalf("the delegated bootstrap prerequisite was rejected: %v", err)
	}
	revoked.Store(true)
	// Even an already supplied internal marker cannot authorize a fresh join.
	if result, err := service.CreateNodeJoin(WithInternalClusterAccess(ctx, "node-bootstrap"), id, NodeJoinInput{Provider: "k3s", Minutes: 5}); result != nil || !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("revoked native scope reached privileged node bootstrap: %+v %v", result, err)
	}
	if reviews.Load() != 2 || privilegedRequests.Load() != 0 {
		t.Fatalf("bootstrap bypassed the native capability fence: reviews=%d privileged=%d", reviews.Load(), privilegedRequests.Load())
	}
}

func TestRBACHistoryRechecksNativePermissionAfterReadingStoredSamples(t *testing.T) {
	var revoked atomic.Bool
	var reviews atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			check := decodeIdentityReview(t, request)
			if request.Header.Get("Impersonate-User") != "edo:project:alice:version-1" || check.Spec.ResourceAttributes == nil || check.Spec.ResourceAttributes.Resource != "nodes" {
				t.Errorf("history review used an administrator or wrong resource: %+v", check.Spec)
			}
			reviews.Add(1)
			writeIdentityReview(w, !revoked.Load())
			return
		}
		if request.URL.Path == "/api/v1/nodes/worker" {
			// The GET was authorized, but the native binding changes before the
			// stored samples are returned. A post-read fence must catch this.
			revoked.Store(true)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Node","metadata":{"name":"worker","uid":"node-1"}}`)
			return
		}
		http.NotFound(w, request)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"": {"kubernetes.node.read"}})
	service.authorizationResolver = policies.resolve
	ctx := identityTestContext(t, service, id, "alice")
	testNodeMonitor(t, service)
	if err := service.nodeMonitor.append(context.Background(), id, "node-1", NodeSample{CollectedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if result, err := service.NodeHistory(ctx, id, "worker", "node-1", time.Hour, true); !errors.Is(err, ErrRBACForbidden) || len(result.Points) != 0 {
		t.Fatalf("native revocation leaked persisted collector history: %+v %v", result, err)
	}
	if reviews.Load() < 2 {
		t.Fatal("history did not execute its own post-read native fence")
	}
}

func TestRBACTransactionClientDoesNotPoisonSharedConnectionCache(t *testing.T) {
	for _, enforced := range []bool{false, true} {
		t.Run(fmt.Sprintf("enforced=%t", enforced), func(t *testing.T) {
			testRBACTransactionClientDoesNotPoisonSharedConnectionCache(t, enforced)
		})
	}
}

func testRBACTransactionClientDoesNotPoisonSharedConnectionCache(t *testing.T, enforced bool) {
	server := identityResourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web","namespace":"default"}}`)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.enabled = enforced
	policies.grant("alice", map[string][]string{"default": {"kubernetes.workload.read"}})
	service.authorizationResolver = policies.resolve
	identityStartResourceWatching(t, service)
	databaseHandle, err := service.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	databaseHandle.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(identityTestContext(t, service, id, "alice"), 3*time.Second)
	defer cancel()
	if err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		client, err := service.WithTransaction(tx).Clientset(ctx, id)
		if err != nil {
			return err
		}
		if _, err := client.CoreV1().Pods("default").Get(ctx, "web", metav1.GetOptions{}); err != nil {
			return err
		}
		service.queries.mu.Lock()
		defer service.queries.mu.Unlock()
		if len(service.queries.clusters) != 0 {
			t.Error("transaction-bound client entered the shared cache")
		}
		return nil
	}); err != nil {
		t.Fatalf("single-connection transaction deadlocked or failed: %v", err)
	}
	client, err := service.Clientset(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("default").Get(ctx, "web", metav1.GetOptions{}); err != nil {
		t.Fatalf("committed transaction poisoned a later client: %v", err)
	}
}
