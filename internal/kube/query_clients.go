package kube

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"edo/internal/model"
	"golang.org/x/sync/singleflight"
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
	mu         sync.Mutex
	ctx        context.Context
	clusters   map[string]*clusterQueries
	watchSlots chan struct{}
}

type clusterQueries struct {
	mu             sync.Mutex
	version        [32]byte
	config         *rest.Config
	typed          *kubernetes.Clientset
	dynamic        dynamic.Interface
	metadata       metadata.Interface
	watchDynamic   dynamic.Interface
	ctx            context.Context
	cancel         context.CancelFunc
	lastUsed       time.Time
	watchers       map[string]*resourceObserver
	subscribers    map[chan ResourceChange]resourceSubscription
	metrics        *utilcache.Expiring
	metricRequests singleflight.Group
	metricsEpoch   atomic.Uint64
	watchSlots     chan struct{}
	backoff        *flowcontrol.Backoff
}

func newResourceQueryCache() *resourceQueryCache {
	return &resourceQueryCache{clusters: make(map[string]*clusterQueries), watchSlots: make(chan struct{}, maxResourceWatches)}
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
				if time.Since(cluster.lastUsed) > queryIdleTimeout {
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
	if cluster := s.queries.clusters[id]; cluster != nil {
		cluster.stop()
		delete(s.queries.clusters, id)
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
	q := s.queries
	if q == nil {
		return s.newClusterQueries(ctx, cluster)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ctx == nil || q.ctx.Err() != nil {
		return s.newClusterQueries(ctx, cluster)
	}
	version := clusterQueryVersion(cluster)
	if current := q.clusters[id]; current != nil {
		if current.version == version && current.ctx.Err() == nil {
			current.lastUsed = time.Now()
			return current, nil
		}
		current.stop()
		delete(q.clusters, id)
	}
	if len(q.clusters) >= maxQueryClusters {
		var oldestID string
		var oldest time.Time
		for key, entry := range q.clusters {
			if oldestID == "" || entry.lastUsed.Before(oldest) {
				oldestID, oldest = key, entry.lastUsed
			}
		}
		q.clusters[oldestID].stop()
		delete(q.clusters, oldestID)
	}
	result, err := s.newClusterQueries(q.ctx, cluster)
	if err == nil {
		q.clusters[id] = result
	}
	return result, err
}

func (s *Service) newClusterQueries(ctx context.Context, cluster *model.KubernetesCluster) (*clusterQueries, error) {
	cfg, err := s.restConfigForCluster(cluster)
	if err != nil {
		return nil, err
	}
	// One rate limiter per connection, shared by typed, dynamic, metadata and watch clients.
	cfg.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(cfg.QPS, cfg.Burst)
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	meta, err := metadata.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	watchCfg := rest.CopyConfig(cfg)
	watchCfg.Timeout = 0
	watchClient, err := dynamic.NewForConfig(watchCfg)
	if err != nil {
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	var watchSlots chan struct{}
	if s.queries != nil {
		watchSlots = s.queries.watchSlots
	}
	return &clusterQueries{version: clusterQueryVersion(cluster), config: cfg, typed: typed, dynamic: dyn, metadata: meta, watchDynamic: watchClient, ctx: child, cancel: cancel, lastUsed: time.Now(), watchers: map[string]*resourceObserver{}, subscribers: map[chan ResourceChange]resourceSubscription{}, metrics: utilcache.NewExpiring(), watchSlots: watchSlots, backoff: flowcontrol.NewBackOff(time.Second, 30*time.Second)}, nil
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
	key := fmt.Sprintf("%d\x00%s\x00%s", q.metricsEpoch.Load(), kind, namespace)
	if !freshResourceRead(ctx) {
		if value, ok := q.metrics.Get(key); ok {
			return value.(*unstructured.UnstructuredList).DeepCopy(), nil
		}
	}
	result := q.metricRequests.DoChan(key, func() (any, error) {
		requestCtx, cancel := context.WithTimeout(q.ctx, s.config.RequestTimeout)
		defer cancel()
		var list *unstructured.UnstructuredList
		var readErr error
		for _, version := range []string{"v1", "v1beta1"} {
			var reader dynamic.ResourceInterface = q.dynamic.Resource(schema.GroupVersionResource{Group: "metrics.k8s.io", Version: version, Resource: kind})
			if kind == "pods" {
				reader = q.dynamic.Resource(schema.GroupVersionResource{Group: "metrics.k8s.io", Version: version, Resource: kind}).Namespace(namespace)
			}
			list, readErr = reader.List(requestCtx, metav1.ListOptions{})
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
		return value.Val.(*unstructured.UnstructuredList).DeepCopy(), nil
	}
}
