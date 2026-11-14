package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	"gorm.io/gorm"
	utilcache "k8s.io/apimachinery/pkg/util/cache"
)

type overviewTestClock struct{ now time.Time }

func (c *overviewTestClock) Now() time.Time { return c.now }

func writeOverviewCacheFixture(w http.ResponseWriter, r *http.Request, name string) {
	if strings.HasPrefix(r.URL.Path, "/apis/metrics.k8s.io/") {
		_, _ = fmt.Fprintf(w, `{"apiVersion":"metrics.k8s.io/v1","kind":"NodeMetricsList","items":[{"metadata":{"name":%q},"timestamp":%q,"usage":{"cpu":"100m","memory":"128Mi"}}]}`, name, time.Now().UTC().Format(time.RFC3339Nano))
		return
	}
	_, _ = fmt.Fprintf(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":%q,"uid":"worker-uid"},"status":{"allocatable":{"cpu":"2","memory":"4Gi","pods":"110"}}}]}`, name)
}

func overviewCacheFixture(t *testing.T, handler http.HandlerFunc) (*Service, string, *clusterQueries) {
	t.Helper()
	service := newKubeTestService(t, 3*time.Second)
	id := createKubeTestCluster(t, service, resourceTestServer(t, handler))
	query, err := service.clusterQueries(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	// 单测独立缓存生命周期，不启动与本用例无关的后台 informer。
	query.ephemeral = false
	service.queries.clusters[id] = query
	t.Cleanup(query.stop)
	return service, id, query
}

func TestOverviewCacheIsolatedCopyExpiryAndInvalidation(t *testing.T) {
	var reads atomic.Int32
	service, id, query := overviewCacheFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/nodes" {
			reads.Add(1)
		}
		writeOverviewCacheFixture(w, r, "worker")
	})
	clock := &overviewTestClock{now: time.Now()}
	query.overviews = utilcache.NewLRUExpireCacheWithClock(maxOverviewEntries, clock)
	allowed := map[string]bool{"kubernetes.node.read": true}
	read := func() *Overview {
		value, err := service.cachedOverview(context.Background(), query, "*", allowed)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	first := read()
	collected := first.CollectedAt
	first.Counts["nodes"] = ResourceCount{Count: 999}
	first.Phases["Injected"] = 99
	first.Usage["nodes"]["worker"] = ResourceUsage{CPU: 999}
	*first.Nodes[0].PodCapacity = 999
	second := read()
	if reads.Load() != 1 || second.Counts["nodes"].Count != 1 || second.Phases["Injected"] != 0 || second.Usage["nodes"]["worker"].CPU != .1 || *second.Nodes[0].PodCapacity != 110 || !second.CollectedAt.Equal(collected) {
		t.Fatal("缓存被调用方污染、重复采集或伪造了采集时间", reads.Load(), second)
	}
	clock.now = clock.now.Add(overviewCacheTTL + time.Millisecond)
	read()
	if reads.Load() != 2 {
		t.Fatal("到期后没有重新采集", reads.Load())
	}
	query.publish(ResourceChange{Resource: "nodes", Type: "MODIFIED"})
	read()
	if reads.Load() != 3 {
		t.Fatal("Watch 变更没有使统计失效", reads.Load())
	}
	service.invalidateResourceQueries(id, "nodes")
	read()
	if reads.Load() != 4 {
		t.Fatal("写操作没有使统计失效", reads.Load())
	}
	if value, err := service.cachedOverview(context.Background(), query, "*", map[string]bool{}); err != nil || len(value.Counts) != 0 {
		t.Fatal("不同权限集合复用了统计", value, err)
	}
}

func TestOverviewConcurrentWaitersShareSamplingAndCancelIndependently(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	service, _, query := overviewCacheFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/nodes" {
			if reads.Add(1) == 1 {
				close(started)
				<-release
			}
		}
		writeOverviewCacheFixture(w, r, "worker")
	})
	allowed := map[string]bool{"kubernetes.node.read": true}
	ctx, cancel := context.WithCancel(context.Background())
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := service.cachedOverview(ctx, query, "*", allowed); first <- err }()
	<-started
	go func() { _, err := service.cachedOverview(context.Background(), query, "*", allowed); second <- err }()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal("等待者取消未及时返回", err)
	}
	close(release)
	if err := <-second; err != nil || reads.Load() != 1 {
		t.Fatal("取消影响其他等待者或重复采集", err, reads.Load())
	}
}

func TestOverviewFreshReadCannotBeOverwrittenByOlderSampling(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	service, _, query := overviewCacheFixture(t, func(w http.ResponseWriter, r *http.Request) {
		name := "new"
		if r.URL.Path == "/api/v1/nodes" && reads.Add(1) == 1 {
			close(started)
			<-release
			name = "old"
		}
		writeOverviewCacheFixture(w, r, name)
	})
	allowed := map[string]bool{"kubernetes.node.read": true}
	oldDone := make(chan error, 1)
	go func() { _, err := service.cachedOverview(context.Background(), query, "*", allowed); oldDone <- err }()
	<-started
	fresh, err := service.cachedOverview(WithFreshResourceRead(context.Background()), query, "*", allowed)
	if err != nil || fresh.Nodes[0].Name != "new" {
		t.Fatal("手动刷新加入了旧采样", fresh, err)
	}
	close(release)
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	current, err := service.cachedOverview(context.Background(), query, "*", allowed)
	if err != nil || current.Nodes[0].Name != "new" || reads.Load() != 2 {
		t.Fatal("旧采样覆盖了手动刷新或未保存新缓存", current, err, reads.Load())
	}
}

func TestOverviewPartialFailuresAreNotCached(t *testing.T) {
	var reads atomic.Int32
	service, _, query := overviewCacheFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/nodes" && reads.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","code":503,"reason":"ServiceUnavailable"}`)
			return
		}
		writeOverviewCacheFixture(w, r, "worker")
	})
	allowed := map[string]bool{"kubernetes.node.read": true}
	first, err := service.cachedOverview(context.Background(), query, "*", allowed)
	if err != nil || first.Counts["nodes"].State != "unavailable" {
		t.Fatal("部分失败没有保持原语义", first, err)
	}
	second, err := service.cachedOverview(context.Background(), query, "*", allowed)
	if err != nil || second.Counts["nodes"].Count != 1 || reads.Load() != 2 {
		t.Fatal("失败结果被缓存", second, err, reads.Load())
	}
}

func TestOverviewCacheKeepsNamespaceResultsSeparate(t *testing.T) {
	var reads atomic.Int32
	service, _, query := overviewCacheFixture(t, func(w http.ResponseWriter, r *http.Request) {
		items := "[]"
		if strings.HasSuffix(r.URL.Path, "/services") {
			reads.Add(1)
			items = `[{"metadata":{"name":"service-a"}}]`
			if strings.Contains(r.URL.Path, "/team-b/") {
				items = `[{"metadata":{"name":"service-b"}},{"metadata":{"name":"service-b2"}}]`
			}
		}
		_, _ = fmt.Fprintf(w, `{"apiVersion":"v1","kind":"List","items":%s}`, items)
	})
	allowed := map[string]bool{"kubernetes.network.read": true}
	for _, ns := range []string{"team-a", "team-b", "team-a"} {
		value, err := service.cachedOverview(context.Background(), query, ns, allowed)
		expected := 1
		if ns == "team-b" {
			expected = 2
		}
		if err != nil || value.Counts["services"].Count != expected {
			t.Fatal("不同命名空间混用了缓存", ns, value, err)
		}
	}
	if reads.Load() != 2 {
		t.Fatal("没有按命名空间复用采样", reads.Load())
	}
}

func TestOverviewCacheRejectsRevokedAdministratorAndWrongProject(t *testing.T) {
	for _, change := range []string{"demoted", "disabled", "auth-version", "cluster-disabled", "project-disabled", "wrong-project"} {
		t.Run(change, func(t *testing.T) {
			var reads atomic.Int32
			f := administratorResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/nodes" {
					reads.Add(1)
				}
				writeOverviewCacheFixture(w, r, "private-worker")
			})
			identityStartResourceWatching(t, f.service)
			allowed := map[string]bool{"kubernetes.node.read": true}
			if _, err := f.service.Overview(WithFreshResourceRead(f.adminCtx), f.cluster.ID, "*", allowed); err != nil {
				t.Fatal(err)
			}
			query, err := f.service.clusterQueries(f.adminCtx, f.cluster.ID)
			if err != nil {
				t.Fatal(err)
			}
			before := reads.Load()
			ctx := f.adminCtx
			switch change {
			case "cluster-disabled":
				if err := f.service.db.Model(&model.KubernetesCluster{}).Where("uuid = ?", f.cluster.ID).Update("is_active", false).Error; err != nil {
					t.Fatal(err)
				}
			case "project-disabled":
				if err := f.service.db.Model(&model.Project{}).Where("uuid = ?", f.cluster.ProjectID).Update("is_active", false).Error; err != nil {
					t.Fatal(err)
				}
			case "wrong-project":
				ctx = identityTestContext(t, f.service, f.cluster.ID, f.adminID)
				scope, _ := database.DepartmentScopeFromContext(ctx)
				scope.ProjectID = "another-project"
				ctx = database.WithDepartmentScope(ctx, scope)
			default:
				column, value := "is_superuser", any(false)
				if change == "disabled" {
					column = "is_active"
				}
				if change == "auth-version" {
					column, value = "auth_version", 2
				}
				if err := f.service.db.Model(&model.User{}).Where("uuid = ?", f.adminID).Update(column, value).Error; err != nil {
					t.Fatal(err)
				}
			}
			var value *Overview
			if change == "auth-version" {
				// HTTP 层负责旧会话失效；这里验证旧身份绑定的缓存不能继续使用。
				value, err = f.service.cachedOverview(ctx, query, "*", allowed)
			} else {
				value, err = f.service.Overview(ctx, f.cluster.ID, "*", allowed)
			}
			if err == nil || value != nil || reads.Load() != before {
				t.Fatal("撤权或跨项目请求读取了旧缓存", value, err, reads.Load())
			}
		})
	}
}

func TestOverviewCollectReusesClusterLookupAcrossResourceKinds(t *testing.T) {
	var upstream, lookups atomic.Int32
	service := newKubeTestService(t, 3*time.Second)
	id := createKubeTestCluster(t, service, resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"List","items":[]}`)
	}))
	if err := service.db.Callback().Query().After("gorm:query").Register("test_overview_cluster_lookups", func(db *gorm.DB) {
		if db.Statement.Schema != nil && db.Statement.Schema.Table == "kubernetes_clusters" {
			lookups.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, def := range ResourceDefinitions() {
		allowed[def.Permission+".read"] = true
	}
	value, err := service.Overview(context.Background(), id, "*", allowed)
	if err != nil || len(value.Counts) != len(ResourceDefinitions()) {
		t.Fatal(value, err)
	}
	// 除入口查询外，只保留每次真实原生 HTTP 请求的即时安全核验。
	if lookups.Load() != upstream.Load()+1 {
		t.Fatal("资源循环重复读取了集群", lookups.Load(), upstream.Load())
	}
}

func TestOverviewCachePreservesActorAndNativeAuthorizationBoundaries(t *testing.T) {
	var revoked atomic.Bool
	var reads atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			writeIdentityReview(w, !revoked.Load())
			return
		}
		if r.URL.Path == "/api/v1/nodes" {
			reads.Add(1)
		}
		name := "alice-worker"
		if strings.Contains(r.Header.Get("Impersonate-User"), ":bob:") {
			name = "bob-worker"
		}
		writeOverviewCacheFixture(w, r, name)
	})
	service := newKubeTestService(t, 3*time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	for _, user := range []string{"alice", "bob"} {
		policies.grant(user, map[string][]string{"": {"kubernetes.node.read"}})
	}
	service.authorizationResolver = policies.resolve
	identityStartResourceWatching(t, service)
	allowed := map[string]bool{"kubernetes.node.read": true}
	for _, user := range []string{"alice", "bob"} {
		ctx := identityTestContext(t, service, id, user)
		value, err := service.Overview(WithFreshResourceRead(ctx), id, "*", allowed)
		if err != nil || value.Nodes[0].Name != user+"-worker" {
			t.Fatal("身份隔离失败", value, err)
		}
	}
	alice := identityTestContext(t, service, id, "alice")
	value, err := service.Overview(alice, id, "*", allowed)
	if err != nil || value.Nodes[0].Name != "alice-worker" || reads.Load() != 2 {
		t.Fatal("用户混用或未命中缓存", value, err, reads.Load())
	}
	revoked.Store(true)
	value, err = service.Overview(alice, id, "*", allowed)
	if value != nil || !errors.Is(err, ErrRBACForbidden) || reads.Load() != 2 {
		t.Fatal("原生撤权后仍返回缓存", value, err, reads.Load())
	}
}

func TestOverviewApplicationAuthorizationCacheRejectsDirectRoleRevocation(t *testing.T) {
	var reads atomic.Int32
	f := administratorResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Impersonate-User") != "" {
			t.Error("未启用原生 RBAC 时改变了实际集群身份")
		}
		if r.URL.Path == "/api/v1/nodes" {
			reads.Add(1)
		}
		writeOverviewCacheFixture(w, r, "member-worker")
	})
	identityStartResourceWatching(t, f.service)
	allowed := map[string]bool{"kubernetes.node.read": true}
	if _, err := f.service.Overview(WithFreshResourceRead(f.userCtx), f.cluster.ID, "*", allowed); err != nil {
		t.Fatal(err)
	}
	query, err := f.service.clusterQueries(f.userCtx, f.cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := reads.Load()
	if err := f.service.db.Where("role_id = ? AND permission = ?", f.roleID, "kubernetes.node.read").Delete(&model.RolePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	value, err := f.service.cachedOverview(f.userCtx, query, "*", allowed)
	if value != nil || !errors.Is(err, ErrRBACForbidden) {
		t.Fatal("应用层撤权未阻断旧缓存", value, err)
	}
	value, err = f.service.Overview(f.userCtx, f.cluster.ID, "*", allowed)
	if err != nil || value == nil || len(value.Counts) != 0 || reads.Load() != before {
		t.Fatal("旧候选权限读取了已撤销的资源", value, err, reads.Load())
	}
	if f.peer.writeCount() != 0 {
		t.Fatal("展示缓存不应写入原生 RBAC")
	}
}
