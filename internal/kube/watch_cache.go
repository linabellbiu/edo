package kube

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/resourceversion"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/tools/cache"
)

var ErrResourceWatchUnavailable = errors.New("Kubernetes 资源订阅暂不可用")

// ResourceChange contains identities only, never resource configuration or credentials.
type ResourceChange struct {
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Type      string `json:"type"`
}

type resourceSubscription struct {
	namespaces map[string]bool
	allowed    map[string]bool
	keys       []string
	state      string
}

type resourceObserver struct {
	owner       *clusterQueries
	key         string
	definition  ResourceDefinition
	namespace   string
	informer    cache.SharedIndexInformer
	ctx         context.Context
	cancel      context.CancelFunc
	active      atomic.Bool
	failed      atomic.Bool
	lastUsed    atomic.Int64
	objectCount atomic.Int64
}

// Standard paginated LIST + WATCH keeps the initial collection memory bound.
// WatchList's temporary initialization store would bypass our LIST page limit.
type boundedResourceListWatch struct{ *cache.ListWatch }

func (boundedResourceListWatch) IsWatchListSemanticsUnSupported() bool { return true }

func resourceWatchKey(def ResourceDefinition, namespace string) string {
	return def.gvr().String() + "\x00" + namespace
}

func (s *Service) watchingEnabled() bool {
	if s.queries == nil {
		return false
	}
	s.queries.mu.Lock()
	defer s.queries.mu.Unlock()
	return s.queries.ctx != nil && s.queries.ctx.Err() == nil
}

func (q *clusterQueries) observer(def ResourceDefinition, namespace string) *resourceObserver {
	key := resourceWatchKey(def, namespace)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ctx.Err() != nil {
		return nil
	}
	if entry := q.watchers[key]; entry != nil {
		if !entry.failed.Load() {
			entry.lastUsed.Store(time.Now().UnixNano())
			return entry
		}
		if q.backoff.IsInBackOffSinceUpdate(key, time.Now()) {
			return nil
		}
		entry.cancel()
		delete(q.watchers, key)
	}
	if len(q.watchers) >= maxResourceWatches {
		return nil
	}
	select {
	case q.watchSlots <- struct{}{}:
	default:
		return nil
	}
	ctx, cancel := context.WithCancel(q.ctx)
	entry := &resourceObserver{owner: q, key: key, definition: def, namespace: namespace, ctx: ctx, cancel: cancel}
	entry.lastUsed.Store(time.Now().UnixNano())
	var api dynamic.ResourceInterface = q.watchDynamic.Resource(def.gvr())
	if def.Namespaced {
		api = q.watchDynamic.Resource(def.gvr()).Namespace(namespace)
	}
	listed := 0
	lw := &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
			timeout := q.config.Timeout
			if timeout <= 0 || timeout > 30*time.Second {
				timeout = 30 * time.Second
			}
			requestCtx, done := context.WithTimeout(ctx, timeout)
			defer done()
			if options.Continue == "" {
				listed = 0
			}
			options.Limit = 500
			list, err := api.List(requestCtx, options)
			if err == nil {
				listed += len(list.Items)
				if listed > 10000 {
					err = ErrResourceWatchUnavailable
					list = nil
				}
			}
			if err != nil {
				entry.fail()
			}
			return list, err
		},
		WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			options.AllowWatchBookmarks = true
			stream, err := api.Watch(ctx, options)
			if err != nil {
				entry.fail()
				return nil, err
			}
			entry.active.Store(true)
			q.publish(ResourceChange{Type: "synced"})
			return entry.observeStream(stream), nil
		},
	}
	entry.informer = cache.NewSharedIndexInformer(boundedResourceListWatch{lw}, &unstructured.Unstructured{}, 0, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	_ = entry.informer.SetTransform(func(value any) (any, error) {
		object, ok := value.(*unstructured.Unstructured)
		if !ok {
			return value, nil
		}
		return &unstructured.Unstructured{Object: resourceView(def.Resource, object, false)}, nil
	})
	_ = entry.informer.SetWatchErrorHandlerWithContext(func(_ context.Context, _ *cache.Reflector, _ error) { entry.fail() })
	_, _ = entry.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(value any) { entry.change(value, "ADDED") },
		UpdateFunc: func(old, value any) {
			a, aok := old.(*unstructured.Unstructured)
			b, bok := value.(*unstructured.Unstructured)
			if aok && bok && a.GetResourceVersion() == b.GetResourceVersion() {
				return
			}
			entry.change(value, "MODIFIED")
		},
		DeleteFunc: func(value any) { entry.change(value, "DELETED") },
	})
	q.watchers[key] = entry
	go func() { defer func() { <-q.watchSlots }(); entry.informer.RunWithContext(ctx) }()
	go func() {
		if cache.WaitForCacheSync(ctx.Done(), entry.informer.HasSynced) {
			q.publish(ResourceChange{Type: "synced"})
		}
	}()
	return entry
}

func (entry *resourceObserver) fail() {
	if entry.failed.Swap(true) {
		return
	}
	entry.active.Store(false)
	entry.owner.backoff.Next(entry.key, time.Now())
	entry.cancel()
	entry.owner.publish(ResourceChange{Resource: entry.definition.Resource, Namespace: entry.namespace, Type: "degraded"})
}

// client-go owns decoding, resourceVersion resume, pagination and store updates.
// This wrapper only invalidates the store when its upstream connection ends.
func (entry *resourceObserver) observeStream(upstream watch.Interface) watch.Interface {
	output := make(chan watch.Event)
	proxy := watch.NewProxyWatcher(output)
	go func() {
		defer close(output)
		defer upstream.Stop()
		defer entry.fail()
		for {
			select {
			case <-entry.ctx.Done():
				return
			case <-proxy.StopChan():
				return
			case event, ok := <-upstream.ResultChan():
				if !ok || event.Type == watch.Error {
					return
				}
				select {
				case output <- event:
				case <-entry.ctx.Done():
					return
				case <-proxy.StopChan():
					return
				}
			}
		}
	}()
	return proxy
}

func (entry *resourceObserver) change(value any, eventType string) {
	if tombstone, ok := value.(cache.DeletedFinalStateUnknown); ok {
		value = tombstone.Obj
	}
	object, ok := value.(*unstructured.Unstructured)
	if !ok {
		return
	}
	if eventType == "ADDED" {
		entry.objectCount.Add(1)
	} else if eventType == "DELETED" {
		entry.objectCount.Add(-1)
	}
	if entry.objectCount.Load() > 10000 {
		entry.fail()
		return
	}
	entry.owner.publish(ResourceChange{Resource: entry.definition.Resource, Namespace: object.GetNamespace(), Name: object.GetName(), Type: eventType})
}

func (q *clusterQueries) publish(event ResourceChange) {
	q.mu.Lock()
	defer q.mu.Unlock()
	def, known := ResourceType(event.Resource)
	for channel, subscription := range q.subscribers {
		// Health is per subscription. One healthy resource must not hide another
		// resource's failed or still-initializing observer.
		state := "synced"
		for _, key := range subscription.keys {
			entry := q.watchers[key]
			if entry == nil || entry.failed.Load() || !entry.active.Load() || !entry.informer.HasSynced() {
				state = "degraded"
				break
			}
		}
		if subscription.state != state {
			subscription.state = state
			q.subscribers[channel] = subscription
			enqueueResourceChange(channel, ResourceChange{Type: state}, state)
		}
		if event.Type == "degraded" || event.Type == "synced" {
			continue
		}
		if known && (!subscription.allowed[def.Permission+".read"] || def.Namespaced && event.Namespace != "" && !subscription.namespaces["*"] && !subscription.namespaces[event.Namespace]) {
			continue
		}
		safeEvent := event
		if event.Type == "reset" {
			safeEvent = ResourceChange{Type: "reset"}
		}
		enqueueResourceChange(channel, safeEvent, state)
	}
}

// All producers hold q.mu, so only the reader may drain concurrently. A select
// avoids the len(channel)/receive race that could deadlock a saturated stream.
func enqueueResourceChange(channel chan ResourceChange, event ResourceChange, state string) {
	select {
	case channel <- event:
		return
	default:
	}
drain:
	for {
		select {
		case <-channel:
		default:
			break drain
		}
	}
	channel <- ResourceChange{Type: "reset"}
	channel <- ResourceChange{Type: state}
}

// snapshot uses a consistent metadata LIST as a read fence. A connected Watch
// and HasSynced alone cannot prove that queued changes reached the local store.
// If the store lags, the API lacks metadata support, or RV cannot be compared,
// callers perform their normal live LIST instead of returning an older result.
func (entry *resourceObserver) snapshot(ctx context.Context, limit int64) (*unstructured.UnstructuredList, bool) {
	if entry.failed.Load() || !entry.active.Load() || !entry.informer.HasSynced() {
		return nil, false
	}
	// Every read needs its own fence: an older in-flight LIST may have captured
	// its version before a write that completed before this request began.
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stop := context.AfterFunc(entry.ctx, cancel)
	defer stop()
	var reader metadata.ResourceInterface = entry.owner.metadata.Resource(entry.definition.gvr())
	if entry.definition.Namespaced {
		reader = entry.owner.metadata.Resource(entry.definition.gvr()).Namespace(entry.namespace)
	}
	fence, err := reader.List(requestCtx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return nil, false
	}
	required := fence.GetResourceVersion()
	store := entry.informer.GetStore()
	// Read the version before the snapshot, so later store changes only make
	// the returned data newer than this successful fence.
	observed := store.LastStoreSyncResourceVersion()
	order, err := resourceversion.CompareResourceVersion(observed, required)
	if err != nil || order < 0 || entry.failed.Load() {
		return nil, false
	}
	items := store.List()
	if int64(len(items)) > limit || len(items) > 10000 {
		return nil, false
	}
	list := &unstructured.UnstructuredList{Items: make([]unstructured.Unstructured, 0, len(items))}
	list.SetResourceVersion(observed)
	for _, value := range items {
		object, ok := value.(*unstructured.Unstructured)
		if !ok {
			return nil, false
		}
		list.Items = append(list.Items, *object.DeepCopy())
	}
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].GetNamespace()+"/"+list.Items[i].GetName() < list.Items[j].GetNamespace()+"/"+list.Items[j].GetName()
	})
	return list, !entry.failed.Load() && entry.ctx.Err() == nil
}

func (s *Service) listObservedResources(ctx context.Context, clusterID string, def ResourceDefinition, namespace string, options metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	limit := options.Limit
	if limit == 0 {
		limit = 10000
	}
	return s.listResourcePage(ctx, clusterID, def, namespace, options, limit)
}

// Aggregate readers consume a complete bounded informer snapshot; public list
// endpoints retain Kubernetes' native pagination and continuation tokens.
func (s *Service) listResourceSnapshotPage(ctx context.Context, clusterID string, def ResourceDefinition, namespace string, options metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return s.listResourcePage(ctx, clusterID, def, namespace, options, 10000)
}

func (s *Service) listResourcePage(ctx context.Context, clusterID string, def ResourceDefinition, namespace string, options metav1.ListOptions, cacheLimit int64) (*unstructured.UnstructuredList, error) {
	q, err := s.clusterQueries(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	if s.watchingEnabled() && options.Continue == "" && options.LabelSelector == "" && options.FieldSelector == "" && options.ResourceVersion == "" && options.ResourceVersionMatch == "" && !freshResourceRead(ctx) {
		if entry := q.observer(def, namespace); entry != nil {
			if list, ok := entry.snapshot(ctx, cacheLimit); ok {
				return list, nil
			}
		}
	}
	var reader dynamic.ResourceInterface = q.dynamic.Resource(def.gvr())
	if def.Namespaced {
		reader = q.dynamic.Resource(def.gvr()).Namespace(namespace)
	}
	return reader.List(ctx, options)
}

func (s *Service) invalidateResourceQueries(clusterID, resource string) {
	if s.queries == nil {
		return
	}
	s.queries.mu.Lock()
	q := s.queries.clusters[clusterID]
	s.queries.mu.Unlock()
	if q == nil {
		return
	}
	q.mu.Lock()
	for key, entry := range q.watchers {
		if resource == "namespaces" || entry.definition.Resource == resource {
			entry.failed.Store(true)
			entry.cancel()
			delete(q.watchers, key)
		}
	}
	q.metricsEpoch.Add(1)
	q.mu.Unlock()
	q.publish(ResourceChange{Resource: resource, Type: "reset"})
}

func (s *Service) SubscribeResourceChanges(ctx context.Context, clusterID string, namespaces []string, allowed map[string]bool) (<-chan ResourceChange, error) {
	if !s.watchingEnabled() {
		return nil, ErrResourceWatchUnavailable
	}
	if len(namespaces) > 32 {
		return nil, ErrInvalidResource
	}
	selection := map[string]bool{}
	if len(namespaces) == 0 {
		namespaces = []string{"*"}
	}
	for _, ns := range namespaces {
		if ns != "*" && len(validation.IsDNS1123Label(ns)) != 0 {
			return nil, ErrInvalidResource
		}
		selection[ns] = true
	}
	if selection["*"] && len(selection) > 1 {
		return nil, ErrInvalidResource
	}
	permissions := map[string]bool{}
	for key, value := range allowed {
		permissions[key] = value
	}
	q, err := s.clusterQueries(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	channel := make(chan ResourceChange, 64)
	keys := []string{}
	for _, def := range resourceDefinitions {
		if !permissions[def.Permission+".read"] {
			continue
		}
		if !def.Namespaced {
			keys = append(keys, resourceWatchKey(def, ""))
		} else {
			for ns := range selection {
				keys = append(keys, resourceWatchKey(def, strings.ReplaceAll(ns, "*", "")))
			}
		}
	}
	if len(keys) == 0 {
		return nil, ErrResourceForbidden
	}
	q.mu.Lock()
	q.subscribers[channel] = resourceSubscription{namespaces: selection, allowed: permissions, keys: keys}
	q.mu.Unlock()
	for _, def := range resourceDefinitions {
		if !permissions[def.Permission+".read"] {
			continue
		}
		if !def.Namespaced {
			q.observer(def, "")
			continue
		}
		for ns := range selection {
			q.observer(def, strings.ReplaceAll(ns, "*", ""))
		}
	}
	q.publish(ResourceChange{Type: "reset"})
	go func() {
		select {
		case <-ctx.Done():
		case <-q.ctx.Done():
		}
		q.mu.Lock()
		defer q.mu.Unlock()
		if _, ok := q.subscribers[channel]; ok {
			delete(q.subscribers, channel)
			close(channel)
		}
	}()
	return channel, nil
}
