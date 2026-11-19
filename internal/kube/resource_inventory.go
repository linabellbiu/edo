package kube

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"edo/internal/database"
	"edo/internal/logredact"
	"edo/internal/model"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

var ErrInventoryPending = errors.New("Kubernetes 资源暂未同步，请等待后台同步")

const (
	inventoryScanInterval    = 2 * time.Second
	inventoryRefreshInterval = 20 * time.Second
	inventoryMaxAge          = 3 * time.Minute
	inventoryConcurrent      = 8
	inventoryPerCluster      = 2
	inventoryPageLimit       = 500
	inventoryItemLimit       = 10000
	inventoryCollectionLimit = 8 << 20
	inventoryTotalLimit      = 96 << 20
	inventoryEntryLimit      = 2048
)

var (
	inventoryReplicaSets    = ResourceDefinition{Resource: "replicasets", Kind: "ReplicaSet", APIVersion: "apps/v1", Namespaced: true, Permission: "kubernetes.workload"}
	inventoryEvents         = ResourceDefinition{Resource: "events", Kind: "Event", APIVersion: "v1", Namespaced: true, Permission: "kubernetes.workload"}
	inventoryEndpointSlices = ResourceDefinition{Resource: "endpointslices", Kind: "EndpointSlice", APIVersion: "discovery.k8s.io/v1", Namespaced: true, Permission: "kubernetes.network"}
)

func inventoryMetricDefinition(resource string) ResourceDefinition {
	def, _ := ResourceType(resource)
	def.APIVersion = "metrics.k8s.io/v1beta1"
	return def
}

type inventoryCollectionKey struct {
	cluster   string
	version   [32]byte
	resource  string
	namespace string
	principal string
}

func inventoryKey(cluster string, version [32]byte, def ResourceDefinition, namespace string) inventoryCollectionKey {
	return inventoryCollectionKey{cluster: cluster, version: version, resource: def.gvr().String(), namespace: namespace}
}

type inventoryCollection struct {
	items      []unstructured.Unstructured
	signatures map[string][32]byte
	at         time.Time
	bytes      int
}

type inventoryEntry struct {
	last    *inventoryCollection
	attempt time.Time
	err     error
}

type resourceInventory struct {
	mu        sync.Mutex
	entries   map[inventoryCollectionKey]inventoryEntry
	listeners map[string]map[chan ResourceChange]inventoryListener
	bytes     int
}

func (s *Service) ConfigureInventoryMonitoring() {
	s.inventory = &resourceInventory{
		entries:   make(map[inventoryCollectionKey]inventoryEntry),
		listeners: make(map[string]map[chan ResourceChange]inventoryListener),
	}
}

// RunInventoryMonitoring 只根据已保存的启用集群采集，不接受浏览器请求启动采集。
func (s *Service) RunInventoryMonitoring(ctx context.Context) error {
	if s.inventory == nil {
		return nil
	}
	ticker := time.NewTicker(inventoryScanInterval)
	defer ticker.Stop()
	finished := make(chan inventoryCollectionKey, inventoryConcurrent)
	active := make(map[inventoryCollectionKey]bool)
	for {
		s.scheduleInventory(ctx, finished, active)
		select {
		case <-ctx.Done():
			for range len(active) {
				<-finished
			}
			return nil
		case key := <-finished:
			delete(active, key)
			// 下次轮询才扫描数据库；先回收同批已结束的任务。
			for len(finished) != 0 {
				delete(active, <-finished)
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
			}
		case <-ticker.C:
		}
	}
}

type inventoryTarget struct {
	key      inventoryCollectionKey
	cluster  *model.KubernetesCluster
	def      ResourceDefinition
	previous time.Time
	subject  *model.KubernetesRBACSubject
}

func (s *Service) scheduleInventory(ctx context.Context, finished chan<- inventoryCollectionKey, active map[inventoryCollectionKey]bool) {
	if ctx.Err() != nil || len(active) >= inventoryConcurrent {
		return
	}
	queryCtx, cancel := context.WithTimeout(ctx, inventoryScanInterval)
	defer cancel()
	var clusters []model.KubernetesCluster
	if err := s.db.WithContext(queryCtx).Where("kubernetes_clusters.is_active = ?", true).
		Where("EXISTS (SELECT 1 FROM projects WHERE projects.uuid = kubernetes_clusters.project_id AND projects.department_id = kubernetes_clusters.department_id AND projects.is_active = ?)", true).
		Order("id ASC").Find(&clusters).Error; err != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "读取待同步的集群失败", "operation", "kubernetes_inventory_clusters", "err", err)
		}
		return
	}
	var grants []struct {
		ClusterID string
		Namespace string
	}
	if err := s.db.WithContext(queryCtx).Model(&model.KubernetesRoleGrant{}).Select("DISTINCT cluster_id, namespace").
		Where("namespace <> '' AND cluster_scope = ?", false).Find(&grants).Error; err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "读取集群同步范围失败", "operation", "kubernetes_inventory_namespaces", "err", err)
	}
	namespaces := make(map[string][]string)
	for _, grant := range grants {
		namespaces[grant.ClusterID] = append(namespaces[grant.ClusterID], grant.Namespace)
	}
	definitions := append(ResourceDefinitions(), inventoryReplicaSets, inventoryEvents, inventoryEndpointSlices, inventoryMetricDefinition("nodes"), inventoryMetricDefinition("pods"))
	inventory := s.inventory
	inventory.mu.Lock()
	versions := make(map[string][32]byte, len(clusters))
	targets := make([]inventoryTarget, 0, len(clusters)*len(definitions))
	denied := make(map[string][]inventoryTarget)
	now := time.Now()
	consider := func(key inventoryCollectionKey, cluster *model.KubernetesCluster, def ResourceDefinition) inventoryEntry {
		entry := inventory.entries[key]
		target := inventoryTarget{key: key, cluster: cluster, def: def, previous: entry.attempt}
		if !active[key] && now.Sub(entry.attempt) >= inventoryRefreshInterval {
			targets = append(targets, target)
		}
		if errors.Is(entry.err, ErrResourceForbidden) && (!def.Namespaced || key.namespace != "") {
			denied[cluster.ID] = append(denied[cluster.ID], target)
		}
		return entry
	}
	for i := range clusters {
		cluster := &clusters[i]
		version := clusterQueryVersion(cluster)
		versions[cluster.ID] = version
		for _, def := range definitions {
			key := inventoryKey(cluster.ID, version, def, "")
			entry := consider(key, cluster, def)
			if (!def.Namespaced && def.Resource != "namespaces") || !errors.Is(entry.err, ErrResourceForbidden) {
				continue
			}
			visible := []string{cluster.DefaultNamespace}
			visible = append(visible, namespaces[cluster.ID]...)
			namespaceDef, _ := ResourceType("namespaces")
			if entry := inventory.entries[inventoryKey(cluster.ID, version, namespaceDef, "")]; entry.last != nil {
				for j := range entry.last.items {
					visible = append(visible, entry.last.items[j].GetName())
				}
			}
			slices.Sort(visible)
			for _, namespace := range slices.Compact(visible) {
				key.namespace = namespace
				if namespace != "" {
					consider(key, cluster, def)
				}
			}
		}
	}
	for key, entry := range inventory.entries {
		if version, exists := versions[key.cluster]; !exists || version != key.version {
			inventory.deleteLocked(key, entry)
		}
	}
	inventory.mu.Unlock()
	targets = append(targets, s.inventorySubjectTargets(queryCtx, denied, active, now)...)
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].previous.Before(targets[j].previous) })
	clustersActive := make(map[string]int)
	for key := range active {
		clustersActive[key.cluster]++
	}
	for _, target := range targets {
		if len(active) >= inventoryConcurrent {
			return
		}
		if clustersActive[target.key.cluster] >= inventoryPerCluster {
			continue
		}
		active[target.key] = true
		clustersActive[target.key.cluster]++
		go func() {
			defer func() { finished <- target.key }()
			s.fetchInventory(ctx, target)
		}()
	}
}

// 只有基础凭据明确被拒绝时，才按已同步成员身份采集该资源范围。
func (s *Service) inventorySubjectTargets(ctx context.Context, denied map[string][]inventoryTarget, active map[inventoryCollectionKey]bool, now time.Time) []inventoryTarget {
	if len(denied) == 0 {
		return nil
	}
	ids := slices.Sorted(maps.Keys(denied))
	var subjects []model.KubernetesRBACSubject
	if err := s.db.WithContext(ctx).Where("cluster_id IN ? AND status = ? AND policy_hash <> ''", ids, "synced").
		Where("EXISTS (SELECT 1 FROM kubernetes_rbac_policies AS policy WHERE policy.cluster_id = kubernetes_rbac_subjects.cluster_id AND policy.revision = kubernetes_rbac_subjects.revision AND policy.enabled = ? AND policy.desired_enabled = ? AND policy.status = ?)", true, true, "synced").
		Where("EXISTS (SELECT 1 FROM project_members AS inventory_member JOIN users AS inventory_actor ON inventory_actor.uuid = inventory_member.user_id AND inventory_actor.is_active = ? WHERE inventory_member.project_id = kubernetes_rbac_subjects.project_id AND inventory_member.user_id = kubernetes_rbac_subjects.user_id)", true).
		Order("id ASC").Find(&subjects).Error; err != nil {
		slog.WarnContext(ctx, "读取后台同步的集群授权成员失败", "operation", "kubernetes_inventory_members", "err", err)
		return nil
	}
	perCluster := make(map[string][]inventoryTarget, len(ids))
	s.inventory.mu.Lock()
	for i := range subjects {
		subject := &subjects[i]
		for _, target := range denied[subject.ClusterID] {
			if target.cluster.ProjectID != subject.ProjectID || target.cluster.DepartmentID != subject.DepartmentID {
				continue
			}
			target.key.principal = rbacPrincipal(subject.ProjectID, subject.UserID, subject.PolicyHash)
			target.subject = subject
			target.previous = s.inventory.entries[target.key].attempt
			if active[target.key] || now.Sub(target.previous) < inventoryRefreshInterval {
				continue
			}
			ready := perCluster[subject.ClusterID]
			if len(ready) < inventoryPerCluster {
				ready = append(ready, target)
			} else if target.previous.Before(ready[len(ready)-1].previous) {
				ready[len(ready)-1] = target
			}
			sort.SliceStable(ready, func(i, j int) bool { return ready[i].previous.Before(ready[j].previous) })
			perCluster[subject.ClusterID] = ready
		}
	}
	s.inventory.mu.Unlock()
	var targets []inventoryTarget
	for _, id := range ids {
		targets = append(targets, perCluster[id]...)
	}
	return targets
}

func (s *Service) fetchInventory(ctx context.Context, target inventoryTarget) {
	request, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	cluster := target.cluster
	scope := database.DepartmentScope{ProjectID: cluster.ProjectID, DepartmentID: cluster.DepartmentID}
	if target.subject == nil {
		request = WithInternalClusterAccess(request, "resource-inventory")
	} else {
		scope.UserID = target.subject.UserID
		request = WithInventoryRead(request)
	}
	request = database.WithDepartmentScope(request, scope)
	query, err := s.clusterQueries(request, cluster.ID)
	var collection *inventoryCollection
	if err == nil {
		if query.connectionVersion != target.key.version {
			return
		}
		if target.subject != nil && (!query.authorization.enforced() || query.authorization.Principal != target.key.principal ||
			!query.authorization.permitsResourceOperation(target.def, target.key.namespace, "read", false)) {
			err = ErrRBACForbidden
		} else {
			collection, err = collectInventory(request, query, target.def, target.key.namespace)
		}
	}
	if ctx.Err() != nil || query != nil && !query.ephemeral && query.ctx.Err() != nil || errors.Is(err, ErrClusterNotFound) {
		return
	}
	if err != nil {
		diagnostic := logredact.Text(err.Error())
		if target.def.Resource == "secrets" {
			diagnostic = string(apierrors.ReasonForError(err))
		} else if query != nil {
			diagnostic = logredact.Text(err.Error(), query.config.BearerToken, query.config.Password)
		}
		slog.DebugContext(ctx, "后台同步 Kubernetes 资源失败", "operation", "kubernetes_inventory_refresh",
			"cluster_id", cluster.ID, "resource", target.def.Resource, "namespace", target.key.namespace, "err", diagnostic)
		classified := resourceError(target.def.Resource, err)
		err = ErrClusterUnreachable
		for _, reason := range []error{ErrInvalidCluster, ErrInvalidResource, ErrResourceNotFound, ErrResourceConflict, ErrResourceForbidden, ErrRBACForbidden, ErrRBACSyncPending} {
			if errors.Is(classified, reason) {
				err = reason
				break
			}
		}
	}
	s.inventory.store(target.key, target.def, collection, err)
}

func collectInventory(ctx context.Context, query *clusterQueries, def ResourceDefinition, namespace string) (*inventoryCollection, error) {
	gvr := def.gvr()
	list := func() dynamic.ResourceInterface {
		resource := query.dynamic.Resource(gvr)
		if def.Namespaced {
			return resource.Namespace(namespace)
		}
		return resource
	}
	result := &inventoryCollection{signatures: make(map[string][32]byte)}
	if def.Resource == "namespaces" && namespace != "" {
		item, err := list().Get(ctx, namespace, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if err := result.add(def, item); err != nil {
			return nil, err
		}
		result.at = time.Now().UTC()
		return result, nil
	}
	options := metav1.ListOptions{Limit: inventoryPageLimit}
	for page := 0; page < 20; page++ {
		items, err := list().List(ctx, options)
		if page == 0 && gvr.Group == "metrics.k8s.io" && apierrors.IsNotFound(err) {
			gvr.Version = "v1"
			items, err = list().List(ctx, options)
		}
		if err != nil {
			return nil, err
		}
		if len(result.items)+len(items.Items) > inventoryItemLimit {
			return nil, ErrClusterUnreachable
		}
		for i := range items.Items {
			if err := result.add(def, &items.Items[i]); err != nil {
				return nil, err
			}
		}
		continuation := items.GetContinue()
		if continuation == "" {
			result.at = time.Now().UTC()
			slices.SortFunc(result.items, func(a, b unstructured.Unstructured) int {
				return compareInventoryKey(a.GetNamespace(), a.GetName(), b.GetNamespace(), b.GetName())
			})
			return result, nil
		}
		if continuation == options.Continue {
			break
		}
		options.Continue = continuation
	}
	return nil, ErrClusterUnreachable
}

func (c *inventoryCollection) add(def ResourceDefinition, item *unstructured.Unstructured) error {
	view := unstructured.Unstructured{Object: resourceView(def.Resource, item, false)}
	data, err := json.Marshal(view.Object)
	if err != nil {
		return err
	}
	c.bytes += len(data)
	if c.bytes > inventoryCollectionLimit {
		return ErrClusterUnreachable
	}
	c.items = append(c.items, view)
	scope := view.GetNamespace()
	previous := c.signatures[scope]
	hash := sha256.New()
	_, _ = hash.Write(previous[:])
	_, _ = hash.Write(data)
	c.signatures[scope] = [32]byte(hash.Sum(nil))
	return nil
}

func (cache *resourceInventory) deleteLocked(key inventoryCollectionKey, entry inventoryEntry) {
	if entry.last != nil {
		cache.bytes -= entry.last.bytes
	}
	delete(cache.entries, key)
}

func (cache *resourceInventory) store(key inventoryCollectionKey, def ResourceDefinition, collection *inventoryCollection, err error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	previous := cache.entries[key]
	entry := inventoryEntry{last: previous.last, attempt: time.Now(), err: err}
	if collection != nil || errors.Is(err, ErrResourceForbidden) || errors.Is(err, ErrRBACForbidden) || errors.Is(err, ErrResourceNotFound) ||
		entry.last != nil && time.Since(entry.last.at) > inventoryMaxAge {
		entry.last = collection
	}
	cache.deleteLocked(key, previous)
	for len(cache.entries) >= inventoryEntryLimit || entry.last != nil && cache.bytes+entry.last.bytes > inventoryTotalLimit {
		var oldestKey inventoryCollectionKey
		var oldestAt time.Time
		for k, item := range cache.entries {
			if oldestAt.IsZero() || item.attempt.Before(oldestAt) {
				oldestKey, oldestAt = k, item.attempt
			}
		}
		cache.deleteLocked(oldestKey, cache.entries[oldestKey])
	}
	cache.entries[key] = entry
	if entry.last != nil {
		cache.bytes += entry.last.bytes
	}
	// 只有已同步的变动才通知页面读缓存，消息不含资源或授权身份。
	before, after := inventorySignatures(previous.last), inventorySignatures(entry.last)
	if (previous.last == nil) == (entry.last == nil) && (previous.err == nil) == (entry.err == nil) && maps.Equal(before, after) {
		return
	}
	changed := make(map[string]bool, len(before)+len(after))
	for namespace, signature := range before {
		changed[namespace] = signature != after[namespace]
	}
	for namespace, signature := range after {
		changed[namespace] = signature != before[namespace]
	}
	statusChanged := (previous.last == nil) != (entry.last == nil) || (previous.err == nil) != (entry.err == nil)
	for channel, listener := range cache.listeners[key.cluster] {
		if listener.version != key.version || key.principal != "" && (listener.authorization == nil || listener.authorization.Principal != key.principal) ||
			!listener.wants(def, key.namespace, changed, statusChanged) {
			continue
		}
		select {
		case channel <- ResourceChange{Type: "reset"}:
		default:
		}
	}
}

func inventorySignatures(collection *inventoryCollection) map[string][32]byte {
	if collection == nil {
		return nil
	}
	return collection.signatures
}
