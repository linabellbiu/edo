package kube

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	"golang.org/x/sync/singleflight"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilcache "k8s.io/apimachinery/pkg/util/cache"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
)

const queryIdleTimeout = 5 * time.Minute
const maxQueryClusters = 64
const maxResourceWatches = 256

type freshResourceReadKey struct{}

// WithFreshResourceRead bypasses display caches. Mutation preconditions and
// resource details always use live GETs, independently of this context value.
func WithFreshResourceRead(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshResourceReadKey{}, true)
}

func freshResourceRead(ctx context.Context) bool {
	value, _ := ctx.Value(freshResourceReadKey{}).(bool)
	return value
}

type resourceQueryCache struct {
	mu                 sync.Mutex
	ctx                context.Context
	clusters           map[string]*clusterQueries
	watchSlots         chan struct{}
	authorizationSlots chan struct{}
	authorizationRate  flowcontrol.RateLimiter
}

type clusterQueries struct {
	mu                 sync.Mutex
	version            [32]byte
	config             *rest.Config
	typed              *kubernetes.Clientset
	dynamic            dynamic.Interface
	metadata           metadata.Interface
	watchDynamic       dynamic.Interface
	ctx                context.Context
	cancel             context.CancelFunc
	lastUsed           atomic.Int64
	watchers           map[string]*resourceObserver
	subscribers        map[chan ResourceChange]resourceSubscription
	metrics            *utilcache.Expiring
	metricRequests     singleflight.Group
	metricsEpoch       atomic.Uint64
	watchSlots         chan struct{}
	backoff            *flowcontrol.Backoff
	clusterID          string
	connectionVersion  [32]byte
	authorization      *clusterAuthorization
	scope              database.DepartmentScope
	internal           bool
	reviewClient       *kubernetes.Clientset
	authorizationSlots chan struct{}
	activeRequests     int
	activeLeases       int
	activeAccess       map[string]*activeClusterAccess
	ephemeral          bool
}

func newResourceQueryCache() *resourceQueryCache {
	return &resourceQueryCache{clusters: make(map[string]*clusterQueries), watchSlots: make(chan struct{}, maxResourceWatches), authorizationSlots: make(chan struct{}, maxNativeAuthorizationChecks), authorizationRate: flowcontrol.NewTokenBucketRateLimiter(maxNativeAuthorizationChecks, maxNativeAuthorizationChecks)}
}

func clusterQueryVersion(cluster *model.KubernetesCluster) [32]byte {
	return sha256.Sum256([]byte(cluster.ID + "\x00" + cluster.ProjectID + "\x00" + cluster.DepartmentID + "\x00" + string(cluster.Mode) + "\x00" + cluster.APIServer + "\x00" + cluster.KubeconfigCiphertext))
}

// RunResourceWatching binds all informers to the server lifetime. Unit tests and
// command-line operations that do not run it retain ordinary direct API reads.
func (s *Service) RunResourceWatching(ctx context.Context) error {
	q := s.queries
	q.mu.Lock()
	if q.ctx != nil {
		q.mu.Unlock()
		return fmt.Errorf("Kubernetes resource watcher already running")
	}
	q.ctx = ctx
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		for id, cluster := range q.clusters {
			cluster.stop()
			delete(q.clusters, id)
		}
		q.ctx = nil
	}()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			q.mu.Lock()
			for id, cluster := range q.clusters {
				cluster.mu.Lock()
				for key, observer := range cluster.watchers {
					if time.Since(time.Unix(0, observer.lastUsed.Load())) > queryIdleTimeout {
						observer.failed.Store(true)
						observer.cancel()
						delete(cluster.watchers, key)
					}
				}
				cluster.mu.Unlock()
				if time.Since(time.Unix(0, cluster.lastUsed.Load())) > queryIdleTimeout {
					cluster.stop()
					delete(q.clusters, id)
				}
			}
			q.mu.Unlock()
		}
	}
}

func (q *clusterQueries) stop() {
	q.cancel()
	q.mu.Lock()
	defer q.mu.Unlock()
	for key := range q.activeAccess {
		delete(q.activeAccess, key)
		<-q.authorizationSlots
	}
	for channel := range q.subscribers {
		close(channel)
		delete(q.subscribers, channel)
	}
}

func (s *Service) invalidateClusterQueries(id string) {
	if s.queries == nil {
		return
	}
	s.queries.mu.Lock()
	defer s.queries.mu.Unlock()
	for key, cluster := range s.queries.clusters {
		if cluster.clusterID == id {
			cluster.stop()
			delete(s.queries.clusters, key)
		}
	}
}

func (s *Service) clusterQueries(ctx context.Context, id string) (*clusterQueries, error) {
	// Always retain the database's project/department scope and active check,
	// including on cache hits. Credentials themselves are never a public key.
	cluster, err := s.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	if !cluster.IsActive {
		s.invalidateClusterQueries(id)
		return nil, ErrClusterNotFound
	}
	authorization, err := s.resolveClusterAuthorization(ctx, cluster, !s.queryTransaction)
	if err != nil {
		s.invalidateClusterActorQueries(ctx, cluster)
		return nil, err
	}
	if authorization.enforced() && (authorization.CacheKey == "" || authorization.Version == "" || authorization.Principal == "") {
		return nil, ErrRBACForbidden
	}
	key := cluster.ID
	if internalClusterAccess(ctx) {
		key += "\x00internal"
	} else if authorization.enforced() {
		key += "\x00" + authorization.CacheKey
	}
	version := authorizedQueryVersion(cluster, authorization, internalClusterAccess(ctx))
	q := s.queries
	if q == nil {
		return s.newClusterQueries(ctx, ctx, cluster, authorization)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ctx == nil || q.ctx.Err() != nil {
		return s.newClusterQueries(ctx, ctx, cluster, authorization)
	}
	if current := q.clusters[key]; current != nil {
		if current.version == version && current.ctx.Err() == nil {
			current.lastUsed.Store(time.Now().UnixNano())
			return current, nil
		}
		current.stop()
		delete(q.clusters, key)
	}
	if len(q.clusters) >= maxQueryClusters {
		var oldestID string
		var oldest int64
		for key, entry := range q.clusters {
			if oldestID == "" || entry.lastUsed.Load() < oldest {
				oldestID, oldest = key, entry.lastUsed.Load()
			}
		}
		q.clusters[oldestID].stop()
		delete(q.clusters, oldestID)
	}
	result, err := s.newClusterQueries(q.ctx, ctx, cluster, authorization)
	if err == nil {
		q.clusters[key] = result
	}
	return result, err
}

func (s *Service) newClusterQueries(ctx, lookupCtx context.Context, cluster *model.KubernetesCluster, authorization *clusterAuthorization) (*clusterQueries, error) {
	cfg, err := s.restConfigForCluster(cluster)
	if err != nil {
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	result := &clusterQueries{clusterID: cluster.ID, connectionVersion: clusterQueryVersion(cluster), authorization: cloneClusterAuthorization(authorization), scope: identityScope(lookupCtx, cluster), internal: internalClusterAccess(lookupCtx), version: authorizedQueryVersion(cluster, authorization, internalClusterAccess(lookupCtx)), config: cfg, ctx: child, cancel: cancel, watchers: map[string]*resourceObserver{}, subscribers: map[chan ResourceChange]resourceSubscription{}, metrics: utilcache.NewExpiring(), backoff: flowcontrol.NewBackOff(time.Second, 30*time.Second), activeAccess: map[string]*activeClusterAccess{}}
	result.lastUsed.Store(time.Now().UnixNano())
	result.ephemeral = s.queries == nil || s.queries.ctx == nil
	if result.authorization.enforced() {
		cfg.Impersonate = rest.ImpersonationConfig{UserName: result.authorization.Principal}
		reviewConfig := rest.CopyConfig(cfg)
		if s.queries != nil {
			result.authorizationSlots = s.queries.authorizationSlots
			reviewConfig.RateLimiter = s.queries.authorizationRate
		} else {
			result.authorizationSlots = make(chan struct{}, maxNativeAuthorizationChecks)
			reviewConfig.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(maxNativeAuthorizationChecks, maxNativeAuthorizationChecks)
		}
		result.reviewClient, err = kubernetes.NewForConfig(reviewConfig)
		if err != nil {
			cancel()
			return nil, err
		}
	}
	previousWrapper := cfg.WrapTransport
	cfg.WrapTransport = func(next http.RoundTripper) http.RoundTripper {
		if previousWrapper != nil {
			next = previousWrapper(next)
		}
		return &authorizationRoundTripper{service: s, query: result, next: next}
	}
	// One rate limiter per connection, shared by typed, dynamic, metadata and watch clients.
	cfg.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(cfg.QPS, cfg.Burst)
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		cancel()
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		cancel()
		return nil, err
	}
	meta, err := metadata.NewForConfig(cfg)
	if err != nil {
		cancel()
		return nil, err
	}
	watchCfg := rest.CopyConfig(cfg)
	watchCfg.Timeout = 0
	watchClient, err := dynamic.NewForConfig(watchCfg)
	if err != nil {
		cancel()
		return nil, err
	}
	var watchSlots chan struct{}
	if s.queries != nil {
		watchSlots = s.queries.watchSlots
	}
	result.typed, result.dynamic, result.metadata, result.watchDynamic, result.watchSlots = typed, dyn, meta, watchClient, watchSlots
	go s.watchQueryAuthorization(result)
	return result, nil
}

func (s *Service) dynamicClient(ctx context.Context, id string) (dynamic.Interface, error) {
	q, err := s.clusterQueries(ctx, id)
	if err != nil {
		return nil, err
	}
	return q.dynamic, nil
}

// Metrics APIs do not generally implement Watch. A two-second sampled cache
// coalesces simultaneous namespace overviews; original sample timestamps and
// missing-data states are retained, and errors are never cached.
func (s *Service) listMetrics(ctx context.Context, id, kind, namespace string) (*unstructured.UnstructuredList, error) {
	q, err := s.clusterQueries(ctx, id)
	if err != nil {
		return nil, err
	}
	definition, ok := ResourceType(kind)
	if !ok {
		return nil, ErrInvalidResource
	}
	ctx, _, err = authorizeResourceQuery(ctx, q, definition, namespace, "read", true)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%d\x00%s\x00%s", q.metricsEpoch.Load(), kind, namespace)
	if !freshResourceRead(ctx) {
		if value, ok := q.metrics.Get(key); ok {
			if err := s.reviewCachedAuthorization(ctx, q, metricAccessAttributes(q.authorization, kind, namespace)); err != nil {
				return nil, err
			}
			return value.(*unstructured.UnstructuredList).DeepCopy(), nil
		}
	}
	result := q.metricRequests.DoChan(key, func() (any, error) {
		requestCtx, cancel := context.WithTimeout(q.ctx, s.config.RequestTimeout)
		defer cancel()
		// Coalesced sampling keeps the exact operation scope while its lifetime
		// is independent of any single waiting caller.
		requestCtx, _, readErr := authorizeResourceQuery(requestCtx, q, definition, namespace, "read", true)
		if readErr != nil {
			return nil, readErr
		}
		var list *unstructured.UnstructuredList
		for _, version := range []string{"v1", "v1beta1"} {
			list, readErr = listIdentityMetrics(requestCtx, q, schema.GroupVersionResource{Group: "metrics.k8s.io", Version: version, Resource: kind}, namespace)
			if !apierrors.IsNotFound(readErr) {
				break
			}
		}
		if readErr == nil {
			q.metrics.Set(key, list.DeepCopy(), 2*time.Second)
		}
		return list, readErr
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case value := <-result:
		if value.Err != nil {
			return nil, value.Err
		}
		if value.Shared {
			// Data sampling may coalesce, authorization may not: a later caller
			// needs a new native fence rather than an older in-flight approval.
			if err := s.reviewCachedAuthorization(ctx, q, metricAccessAttributes(q.authorization, kind, namespace)); err != nil {
				return nil, err
			}
		}
		return value.Val.(*unstructured.UnstructuredList).DeepCopy(), nil
	}
}

func metricAccessAttributes(authorization *clusterAuthorization, kind, namespace string) []authorizationv1.ResourceAttributes {
	namespaces := []string{namespace}
	if kind == "pods" && namespace == "" && authorization.enforced() {
		definition, _ := ResourceType("pods")
		namespaces = authorization.readNamespaces(definition)
	}
	result := make([]authorizationv1.ResourceAttributes, 0, len(namespaces))
	for _, selected := range namespaces {
		if kind != "pods" {
			selected = ""
		}
		result = append(result, authorizationv1.ResourceAttributes{Group: "metrics.k8s.io", Resource: kind, Namespace: selected, Verb: "list"})
	}
	return result
}

func listIdentityMetrics(ctx context.Context, q *clusterQueries, resource schema.GroupVersionResource, namespace string) (*unstructured.UnstructuredList, error) {
	if resource.Resource != "pods" {
		return q.dynamic.Resource(resource).List(ctx, metav1.ListOptions{})
	}
	namespaces := []string{namespace}
	if q.authorization.enforced() {
		definition, _ := ResourceType("pods")
		allowed := q.authorization.readNamespaces(definition)
		if namespace == "" {
			namespaces = allowed
		} else if !slices.Contains(allowed, namespace) {
			return nil, ErrRBACForbidden
		}
		if len(namespaces) == 0 {
			return nil, ErrRBACForbidden
		}
	}
	result := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{}}
	for _, selected := range namespaces {
		continuation, seen := "", map[string]bool{}
		for {
			page, err := q.dynamic.Resource(resource).Namespace(selected).List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
			if err != nil {
				return nil, err
			}
			result.Items = append(result.Items, page.Items...)
			if len(result.Items) > 10000 {
				return nil, ErrClusterUnreachable
			}
			continuation = page.GetContinue()
			if continuation == "" {
				break
			}
			if seen[continuation] || len(seen) >= 20 {
				return nil, ErrClusterUnreachable
			}
			seen[continuation] = true
		}
	}
	return result, nil
}
