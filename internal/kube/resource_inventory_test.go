package kube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackgroundInventoryKeepsOrdinaryReadsOffUpstream(t *testing.T) {
	var resources atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		resources.Add(1)
		if r.Method != http.MethodGet || r.URL.Query().Get("watch") != "" {
			t.Errorf("后台采集不应写入资源或为页面开启监听: %s %s", r.Method, r.URL)
		}
		switch r.URL.Path {
		case "/api/v1/nodes":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","metadata":{"resourceVersion":"15"},"items":[{"apiVersion":"v1","kind":"Node","metadata":{"name":"worker","uid":"worker-1","resourceVersion":"15"}}]}`)
		case "/api/v1/namespaces":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NamespaceList","metadata":{"resourceVersion":"15"},"items":[{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"default"}}]}`)
		case "/api/v1/secrets":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"SecretList","items":[{"apiVersion":"v1","kind":"Secret","metadata":{"name":"private","namespace":"default","annotations":{"token":"hidden"}},"data":{"token":"c2VjcmV0"}}]}`)
		default:
			t.Errorf("页面请求触达了上游资源: %s", r.URL)
			http.NotFound(w, r)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	cluster, err := service.Find(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureInventoryMonitoring()
	ctx, cancel := context.WithCancel(WithInventoryRead(context.Background()))
	defer cancel()
	if _, err := service.ListResources(ctx, id, "nodes", "", ""); !errors.Is(err, ErrInventoryPending) {
		t.Fatalf("空快照未返回待同步状态: %v", err)
	}
	if _, err := service.GetResource(ctx, id, "nodes", "", "worker", false); !errors.Is(err, ErrInventoryPending) {
		t.Fatalf("详情空快照未返回待同步状态: %v", err)
	}
	if _, err := service.Overview(ctx, id, "*", map[string]bool{"kubernetes.node.read": true}); !errors.Is(err, ErrInventoryPending) {
		t.Fatalf("总览空快照未返回待同步状态: %v", err)
	}
	subscription, err := service.SubscribeResourceChanges(ctx, id, []string{"*"}, map[string]bool{"kubernetes.node.read": true})
	if err != nil || resources.Load() != 0 {
		t.Fatalf("普通读取或订阅触发了远端请求: %v, %d", err, resources.Load())
	}

	// 后台扫描自行挑选首批资源，和请求是否已进入页面无关。
	finished := make(chan inventoryCollectionKey, inventoryConcurrent)
	active := map[inventoryCollectionKey]bool{}
	service.scheduleInventory(context.Background(), finished, active)
	if len(active) == 0 {
		t.Fatal("后台没有找到已登记的启用集群")
	}
	for range len(active) {
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("后台资源采集超时")
		}
	}
	select {
	case <-subscription:
	case <-time.After(time.Second):
		t.Fatal("后台回填没有发出缓存更新通知")
	}
	nodesDefinition, _ := ResourceType("nodes")
	nodesKey := inventoryKey(id, clusterQueryVersion(cluster), nodesDefinition, "")
	service.fetchInventory(context.Background(), inventoryTarget{key: nodesKey, cluster: cluster, def: nodesDefinition})
	select {
	case <-subscription:
		t.Fatal("相同的已同步资源不应触发页面重新读取")
	default:
	}
	secrets, _ := ResourceType("secrets")
	secretKey := inventoryKey(id, clusterQueryVersion(cluster), secrets, "")
	service.fetchInventory(context.Background(), inventoryTarget{key: secretKey, cluster: cluster, def: secrets})
	before := resources.Load()

	read := WithInventoryRead(context.Background())
	nodes, err := service.ListResources(read, id, "nodes", "", "")
	if err != nil || len(nodes.Items) != 1 {
		t.Fatalf("节点没有读取到已同步的值: %+v %v", nodes, err)
	}
	nodes.Items[0]["metadata"].(map[string]any)["name"] = "corrupted"
	node, err := service.GetResource(read, id, "nodes", "", "worker", false)
	if err != nil || node["metadata"].(map[string]any)["name"] != "worker" {
		t.Fatalf("快照被读取方污染: %+v %v", node, err)
	}
	namespaces, err := service.Namespaces(read, id)
	if err != nil || len(namespaces) != 1 || namespaces[0] != "default" {
		t.Fatalf("命名空间没有读取已同步结果: %v %v", namespaces, err)
	}
	if overview, err := service.Overview(read, id, "*", map[string]bool{"kubernetes.node.read": true, "kubernetes.workload.read": true}); err != nil || overview.Counts["nodes"].Count != 1 || overview.Counts["nodes"].State != "ok" {
		t.Fatalf("缺少其他快照不应影响已同步的节点统计: %+v %v", overview, err)
	}
	secret, err := service.GetResource(read, id, "secrets", "default", "private", false)
	encoded, _ := json.Marshal(secret)
	if err != nil || strings.Contains(string(encoded), "c2VjcmV0") || strings.Contains(string(encoded), "hidden") || !strings.Contains(string(encoded), "token") {
		t.Fatalf("Secret 快照脱敏不正确: %s %v", encoded, err)
	}
	if resources.Load() != before {
		t.Fatalf("缓存命中仍访问资源 API: %d → %d", before, resources.Load())
	}
	if at, stale := InventoryReadStatus(read); at.IsZero() || stale {
		t.Fatalf("缓存未报告实际采集时间: %s %v", at, stale)
	}
	service.inventory.store(nodesKey, nodesDefinition, nil, ErrClusterUnreachable)
	staleCtx := WithInventoryRead(context.Background())
	if _, err := service.GetResource(staleCtx, id, "nodes", "", "worker", false); err != nil {
		t.Fatalf("临时失败没有保留上次结果: %v", err)
	}
	if at, stale := InventoryReadStatus(staleCtx); at.IsZero() || !stale {
		t.Fatalf("过期状态没有随数据一起返回: %s %v", at, stale)
	}
	service.inventory.store(nodesKey, nodesDefinition, nil, ErrResourceForbidden)
	if _, err := service.GetResource(read, id, "nodes", "", "worker", false); !errors.Is(err, ErrResourceForbidden) {
		t.Fatalf("凭据明确失权后仍可读取旧资源: %v", err)
	}
}

func TestBackgroundInventoryWarmsKnownNamespaceWhenClusterListForbidden(t *testing.T) {
	var namespacedCalls atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/namespaces":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403}`)
		case "/api/v1/namespaces/default":
			namespacedCalls.Add(1)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"default","uid":"ns-1"}}`)
		case "/api/v1/nodes":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[]}`)
		default:
			t.Errorf("同步范围与配置不一致: %s", r.URL)
			http.NotFound(w, r)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	cluster, err := service.Find(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureInventoryMonitoring()
	def, _ := ResourceType("namespaces")
	key := inventoryKey(id, clusterQueryVersion(cluster), def, "")
	service.fetchInventory(context.Background(), inventoryTarget{key: key, cluster: cluster, def: def})
	finished := make(chan inventoryCollectionKey, inventoryConcurrent)
	active := map[inventoryCollectionKey]bool{}
	service.scheduleInventory(context.Background(), finished, active)
	for range len(active) {
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("后台命名空间采集超时")
		}
	}
	if namespacedCalls.Load() != 1 {
		t.Fatalf("后台没有独立采集已保存的命名空间: %d", namespacedCalls.Load())
	}
	view, err := service.GetResource(WithInventoryRead(context.Background()), id, "namespaces", "*", "default", false)
	if err != nil || view["metadata"].(map[string]any)["uid"] != "ns-1" || namespacedCalls.Load() != 1 {
		t.Fatalf("命名空间详情没有保留既有的已授权单项路径或触发了远程请求: %+v %v", view, err)
	}
}

func TestBackgroundInventoryEnforcesCurrentNativeAndLocalAuthorization(t *testing.T) {
	var nativeDenied atomic.Bool
	var resourceRequests atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			review := decodeIdentityReview(t, r)
			a := review.Spec.ResourceAttributes
			configAccess := a != nil && a.Resource == "configmaps" && (a.Verb == "list" || a.Verb == "get" && a.Name == "demo")
			observationAccess := a != nil && a.Group == "apps" && a.Resource == "deployments" && a.Verb == "get" && a.Name == "web"
			if (!configAccess && !observationAccess) || a.Namespace != "alpha" ||
				!strings.Contains(r.Header.Get("Impersonate-User"), ":alice:") {
				t.Errorf("快照读取的原生授权目标不正确: %+v", a)
			}
			writeIdentityReview(w, !nativeDenied.Load())
			return
		}
		resourceRequests.Add(1)
		if r.URL.Path != "/api/v1/configmaps" || r.Header.Get("Impersonate-User") != "" {
			t.Errorf("后台采集混入了当前浏览者的范围: %s", r.URL)
		}
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"ConfigMapList","items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"demo","namespace":"alpha"}},{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"private","namespace":"beta"}}]}`)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	cluster, err := service.Find(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureInventoryMonitoring()
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"alpha": {"kubernetes.config.read", "kubernetes.workload.read"}})
	service.authorizationResolver = policies.resolve
	def, _ := ResourceType("configmaps")
	target := inventoryTarget{cluster: cluster, def: def, key: inventoryKey(id, clusterQueryVersion(cluster), def, "")}
	service.fetchInventory(context.Background(), target)
	ctx := WithInventoryRead(identityTestContext(t, service, id, "alice"))
	list, err := service.ListResources(ctx, id, "configmaps", "*", "")
	if err != nil || len(list.Items) != 1 || list.Items[0]["metadata"].(map[string]any)["namespace"] != "alpha" {
		t.Fatalf("跨命名空间快照没有按真实授权过滤: %+v %v", list, err)
	}
	if _, err := service.GetResource(ctx, id, "configmaps", "alpha", "demo", false); err != nil {
		t.Fatal(err)
	}
	if scope, err := service.DeploymentObservationScope(ctx, id, "alpha", "web"); err != nil || scope == "" {
		t.Fatalf("已授权成员应可读取历史部署状态的范围: %v", err)
	}
	nativeDenied.Store(true)
	if _, err := service.GetResource(ctx, id, "configmaps", "alpha", "demo", false); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("缓存绕过了原生 get 权限: %v", err)
	}
	if _, err := service.DeploymentObservationScope(ctx, id, "alpha", "web"); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("历史部署状态绕过了具名工作负载读取权限: %v", err)
	}
	nativeDenied.Store(false)
	policies.mu.Lock()
	policies.revoked["alice"] = true
	policies.mu.Unlock()
	if _, err := service.ListResources(ctx, id, "configmaps", "*", ""); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("缓存绕过了 EDO 撤权: %v", err)
	}
	if _, err := service.DeploymentObservationScope(ctx, id, "alpha", "web"); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("历史部署状态绕过了 EDO 撤权: %v", err)
	}
	if resourceRequests.Load() != 1 {
		t.Fatalf("读取缓存命中了上游资源 API: %d", resourceRequests.Load())
	}
}

func TestBackgroundInventoryUsesSyncedActorWhenConnectorCanOnlyImpersonate(t *testing.T) {
	f := newRBACFixture(t)
	var nativeLists atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		subject := r.Header.Get("Impersonate-User")
		switch r.URL.Path {
		case "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
			if subject != "" {
				review := decodeIdentityReview(t, r)
				attributes := review.Spec.ResourceAttributes
				writeIdentityReview(w, attributes != nil && attributes.Verb == "list" && attributes.Resource == "deployments" && attributes.Namespace == "test")
				return
			}
		case "/apis/apps/v1/deployments", "/apis/apps/v1/namespaces/test/deployments":
			if r.URL.Path == "/apis/apps/v1/namespaces/test/deployments" && strings.Contains(subject, ":"+f.userID+":") {
				nativeLists.Add(1)
				_, _ = io.WriteString(w, `{"apiVersion":"apps/v1","kind":"DeploymentList","items":[{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"namespace":"test","name":"web","uid":"web-1"}}]}`)
				return
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403}`)
			return
		}
		f.peer.serve(t, w, r)
	})
	setConnectionStatusServer(t, f, server)
	state := f.enable(t)
	if state, err := f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision); err != nil || state.Status != "synced" {
		t.Fatalf("无法准备实际映射身份: %+v %v", state, err)
	}
	cluster, err := f.service.Find(f.adminCtx, f.cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.service.ConfigureInventoryMonitoring()
	def, _ := ResourceType("deployments")
	base := inventoryTarget{cluster: cluster, def: def, key: inventoryKey(cluster.ID, clusterQueryVersion(cluster), def, "")}
	f.service.fetchInventory(context.Background(), base)
	base.key.namespace = "test"
	f.service.fetchInventory(context.Background(), base)
	read := WithInventoryRead(f.userCtx)
	if _, err := f.service.ListResources(read, cluster.ID, "deployments", "test", ""); !errors.Is(err, ErrInventoryPending) || nativeLists.Load() != 0 {
		t.Fatalf("成员页面在快照就绪前意外发起集群读取: %v, %d", err, nativeLists.Load())
	}
	targets := f.service.inventorySubjectTargets(context.Background(), map[string][]inventoryTarget{cluster.ID: {base}}, nil, time.Now())
	if len(targets) != 1 || targets[0].subject.UserID != f.userID || targets[0].key.principal == "" {
		t.Fatalf("后台没有选择当前项目已同步授权身份: %+v", targets)
	}
	f.service.fetchInventory(context.Background(), targets[0])
	list, err := f.service.ListResources(read, cluster.ID, "deployments", "test", "")
	if err != nil || len(list.Items) != 1 || list.Items[0]["metadata"].(map[string]any)["uid"] != "web-1" || nativeLists.Load() != 1 {
		t.Fatalf("普通读取没有安全复用后台模拟得到的快照: %+v %v, %d", list, err, nativeLists.Load())
	}
	if _, err := f.service.ListResources(WithInventoryRead(f.adminCtx), cluster.ID, "deployments", "test", ""); !errors.Is(err, ErrResourceForbidden) {
		t.Fatalf("基础身份错误复用了成员模拟缓存: %v", err)
	}
}
