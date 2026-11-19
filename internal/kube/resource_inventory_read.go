package kube

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
)

type inventoryReadKey struct{}

type inventoryReadInfo struct {
	mu    sync.Mutex
	at    time.Time
	stale bool
}

// WithInventoryRead 将用户的展示请求限制为本地快照读取；普通读取不创建上游监听。
func WithInventoryRead(ctx context.Context) context.Context {
	if _, exists := ctx.Value(inventoryReadKey{}).(*inventoryReadInfo); exists {
		return ctx
	}
	return context.WithValue(ctx, inventoryReadKey{}, &inventoryReadInfo{})
}

func InventoryReadStatus(ctx context.Context) (time.Time, bool) {
	info, ok := ctx.Value(inventoryReadKey{}).(*inventoryReadInfo)
	if !ok {
		return time.Time{}, false
	}
	info.mu.Lock()
	defer info.mu.Unlock()
	return info.at, info.stale
}

func noteInventoryRead(ctx context.Context, at time.Time, stale bool) {
	info, ok := ctx.Value(inventoryReadKey{}).(*inventoryReadInfo)
	if !ok {
		return
	}
	info.mu.Lock()
	defer info.mu.Unlock()
	if info.at.IsZero() || at.Before(info.at) {
		info.at = at
	}
	info.stale = info.stale || stale
}

func (s *Service) inventoryRead(ctx context.Context) bool {
	return s != nil && s.inventory != nil && ctx.Value(inventoryReadKey{}) != nil
}

func (cache *resourceInventory) collection(ctx context.Context, q *clusterQueries, def ResourceDefinition, namespace string) (*inventoryCollection, error) {
	key := inventoryKey(q.clusterID, q.connectionVersion, def, "")
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry := cache.entries[key]
	if namespace != "" && entry.last == nil {
		scoped := key
		scoped.namespace = namespace
		if candidate, exists := cache.entries[scoped]; exists || errors.Is(entry.err, ErrResourceForbidden) {
			key, entry = scoped, candidate
		}
	}
	if q.authorization.enforced() && entry.last == nil && errors.Is(entry.err, ErrResourceForbidden) {
		key.principal = q.authorization.Principal
		entry = cache.entries[key]
	}
	if entry.last != nil && time.Since(entry.last.at) <= inventoryMaxAge {
		noteInventoryRead(ctx, entry.last.at, entry.err != nil || time.Since(entry.last.at) > 2*inventoryRefreshInterval)
		return entry.last, nil
	}
	if entry.err != nil {
		return nil, entry.err
	}
	return nil, ErrInventoryPending
}

func compareInventoryKey(aNamespace, aName, bNamespace, bName string) int {
	if order := cmp.Compare(aNamespace, bNamespace); order != 0 {
		return order
	}
	return cmp.Compare(aName, bName)
}

func inventoryAttributes(def ResourceDefinition, namespace, name, verb string) authorizationv1.ResourceAttributes {
	gvr := def.gvr()
	return authorizationv1.ResourceAttributes{
		Group: gvr.Group, Version: gvr.Version, Resource: def.Resource, Namespace: namespace, Name: name, Verb: verb,
	}
}

func (s *Service) inventoryGet(ctx context.Context, q *clusterQueries, def ResourceDefinition, namespace, name string) (*unstructured.Unstructured, error) {
	if !def.Namespaced {
		namespace = ""
	}
	scope := namespace
	if def.Resource == "namespaces" {
		scope = name
	}
	if _, _, err := authorizeResourceQuery(ctx, q, def, scope, "read", false); err != nil {
		return nil, err
	}
	if err := s.reviewCachedAuthorization(ctx, q, []authorizationv1.ResourceAttributes{inventoryAttributes(def, namespace, name, "get")}); err != nil {
		return nil, err
	}
	collection, err := s.inventory.collection(ctx, q, def, scope)
	if err != nil {
		return nil, err
	}
	index, found := slices.BinarySearchFunc(collection.items, name, func(item unstructured.Unstructured, _ string) int {
		return compareInventoryKey(item.GetNamespace(), item.GetName(), namespace, name)
	})
	if !found {
		return nil, ErrResourceNotFound
	}
	return collection.items[index].DeepCopy(), nil
}

const inventoryContinuationPrefix = "edo-cache-v1:"

type inventoryCursor struct {
	Scope     string
	Namespace string
	Name      string
}

func (s *Service) inventoryList(ctx context.Context, q *clusterQueries, def ResourceDefinition, namespace string, options metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	if !def.Namespaced {
		namespace = ""
	}
	result := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{}}
	if def.Resource == "namespaces" && q.authorization.enforced() && !q.authorization.has("", "kubernetes.config.read") && len(q.authorization.Namespaces) == 0 {
		return result, nil
	}
	if _, _, err := authorizeResourceQuery(ctx, q, def, namespace, "read", true); err != nil {
		return nil, err
	}
	sourceNamespaces := []string{namespace}
	checks := []authorizationv1.ResourceAttributes{inventoryAttributes(def, namespace, "", "list")}
	filterNamespaceNames := q.authorization.enforced() && def.Resource == "namespaces" && !q.authorization.has("", "kubernetes.config.read")
	if filterNamespaceNames {
		sourceNamespaces = slices.Clone(q.authorization.Namespaces)
		slices.Sort(sourceNamespaces)
		sourceNamespaces = slices.Compact(sourceNamespaces)
		checks = nil
		for _, name := range sourceNamespaces {
			checks = append(checks, inventoryAttributes(def, "", name, "get"))
		}
	} else if q.authorization.enforced() && def.Namespaced && namespace == "" {
		sourceNamespaces = q.authorization.readNamespaces(def)
		checks = nil
		for _, name := range sourceNamespaces {
			checks = append(checks, inventoryAttributes(def, name, "", "list"))
		}
	}
	matchesLabels, labelErr := labels.Parse(options.LabelSelector)
	matchesFields, fieldErr := fields.ParseSelector(options.FieldSelector)
	if labelErr != nil || fieldErr != nil || options.ResourceVersion != "" || options.ResourceVersionMatch != "" {
		return nil, ErrInvalidResource
	}
	scope := rbacHash([]any{q.version, def.gvr(), namespace, options.LabelSelector, options.FieldSelector})
	cursor := inventoryCursor{Scope: scope}
	if options.Continue != "" {
		encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(options.Continue, inventoryContinuationPrefix))
		if !strings.HasPrefix(options.Continue, inventoryContinuationPrefix) || err != nil || json.Unmarshal(encoded, &cursor) != nil ||
			cursor.Scope != scope || cursor.Name == "" {
			return nil, ErrInvalidResource
		}
	}
	if err := s.reviewCachedAuthorization(ctx, q, checks); err != nil {
		return nil, err
	}

	limit := options.Limit
	if limit <= 0 || limit > inventoryItemLimit {
		limit = inventoryItemLimit
	}
	for _, selected := range sourceNamespaces {
		collection, err := s.inventory.collection(ctx, q, def, selected)
		if err != nil {
			return nil, err
		}
		start := 0
		if def.Namespaced && selected != "" {
			start, _ = slices.BinarySearchFunc(collection.items, selected, func(item unstructured.Unstructured, scope string) int {
				return cmp.Compare(item.GetNamespace(), scope)
			})
		}
		for i := start; i < len(collection.items); i++ {
			item := &collection.items[i]
			if selected != "" && def.Namespaced && item.GetNamespace() != selected {
				break
			}
			if filterNamespaceNames && item.GetName() != selected ||
				compareInventoryKey(item.GetNamespace(), item.GetName(), cursor.Namespace, cursor.Name) <= 0 ||
				options.LabelSelector != "" && !matchesLabels.Matches(labels.Set(item.GetLabels())) ||
				options.FieldSelector != "" && !matchesFields.Matches(inventoryObjectFields(item)) {
				continue
			}
			if int64(len(result.Items)) == limit {
				last := &result.Items[len(result.Items)-1]
				data, _ := json.Marshal(inventoryCursor{Scope: scope, Namespace: last.GetNamespace(), Name: last.GetName()})
				result.SetContinue(inventoryContinuationPrefix + base64.RawURLEncoding.EncodeToString(data))
				return result, nil
			}
			result.Items = append(result.Items, *item.DeepCopy())
		}
	}
	return result, nil
}

func inventoryObjectFields(item *unstructured.Unstructured) fields.Set {
	set := fields.Set{"metadata.name": item.GetName(), "metadata.namespace": item.GetNamespace()}
	for _, key := range []string{"spec.nodeName", "status.phase", "involvedObject.uid"} {
		set[key], _, _ = unstructured.NestedString(item.Object, strings.Split(key, ".")...)
	}
	return set
}

func (s *Service) inventoryMetrics(ctx context.Context, query *clusterQueries, kind, namespace string) (*unstructured.UnstructuredList, error) {
	if kind != "nodes" && kind != "pods" {
		return nil, ErrInvalidResource
	}
	if kind == "nodes" {
		namespace = ""
	}
	return s.inventoryList(ctx, query, inventoryMetricDefinition(kind), namespace, metav1.ListOptions{})
}

type inventoryListener struct {
	namespaces    map[string]bool
	permissions   map[string]bool
	authorization *clusterAuthorization
	version       [32]byte
}

func (listener inventoryListener) wants(def ResourceDefinition, source string, changed map[string]bool, statusChanged bool) bool {
	if !listener.permissions[def.Permission+".read"] {
		return false
	}
	if !def.Namespaced {
		return listener.authorization.permitsResourceOperation(def, source, "read", false)
	}
	visible := func(namespace string) bool {
		return (listener.namespaces[namespace] || listener.namespaces["*"]) &&
			listener.authorization.permitsResourceOperation(def, namespace, "read", false)
	}
	if statusChanged && (source != "" && visible(source) || source == "" && (len(listener.namespaces) > 0)) {
		return true
	}
	for namespace, updated := range changed {
		if updated && visible(namespace) {
			return true
		}
	}
	return false
}

func (s *Service) subscribeInventory(ctx context.Context, q *clusterQueries, namespaces, permissions map[string]bool) (<-chan ResourceChange, error) {
	allowed := false
	for _, def := range resourceDefinitions {
		if !permissions[def.Permission+".read"] {
			continue
		}
		if (!def.Namespaced && q.authorization.has("", def.Permission+".read")) ||
			(def.Namespaced && len(watchedNamespaces(q.authorization, def, namespaces)) != 0) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, ErrResourceForbidden
	}
	cache := s.inventory
	cache.mu.Lock()
	if len(cache.listeners[q.clusterID]) >= 64 {
		cache.mu.Unlock()
		return nil, ErrResourceWatchUnavailable
	}
	q.mu.Lock()
	if q.ctx.Err() != nil {
		q.mu.Unlock()
		cache.mu.Unlock()
		return nil, ErrRBACForbidden
	}
	q.activeLeases++
	q.mu.Unlock()
	channel := make(chan ResourceChange, 1)
	if cache.listeners[q.clusterID] == nil {
		cache.listeners[q.clusterID] = map[chan ResourceChange]inventoryListener{}
	}
	cache.listeners[q.clusterID][channel] = inventoryListener{namespaces: namespaces, permissions: permissions, authorization: q.authorization, version: q.connectionVersion}
	cache.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
		case <-q.ctx.Done():
		}
		cache.mu.Lock()
		delete(cache.listeners[q.clusterID], channel)
		if len(cache.listeners[q.clusterID]) == 0 {
			delete(cache.listeners, q.clusterID)
		}
		close(channel)
		cache.mu.Unlock()
		q.mu.Lock()
		q.activeLeases--
		q.mu.Unlock()
	}()
	return channel, nil
}
