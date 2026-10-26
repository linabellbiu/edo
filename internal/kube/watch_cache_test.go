package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
)

type resourceWatchEvent struct {
	Type   string         `json:"type"`
	Object map[string]any `json:"object"`
}

// The fake API deliberately lets a write or live state change precede its watch
// event, so these tests can distinguish direct reads from cached snapshots.
type resourceWatchFixture struct {
	server              *httptest.Server
	resource            string
	kind                string
	mu                  sync.Mutex
	objects             map[string]map[string]any
	watches             map[int]chan resourceWatchEvent
	nextWatch           int
	rv                  int
	denyWatch           bool
	metadataUnsupported bool
	expectedAuth        string
	listCalls           atomic.Int32
	barrierCalls        atomic.Int32
	watchCalls          atomic.Int32
	barrierGate         <-chan struct{}
	barrierEntered      chan struct{}
	barrierCancelled    chan struct{}
	barrierOnce         bool
	barrierSnapshot     bool
}

func newResourceWatchFixture(t *testing.T, resource, kind string, object map[string]any) *resourceWatchFixture {
	t.Helper()
	f := &resourceWatchFixture{resource: resource, kind: kind, objects: map[string]map[string]any{}, watches: map[int]chan resourceWatchEvent{}, rv: 1, expectedAuth: "Bearer test-token"}
	if object != nil {
		f.objects[object["metadata"].(map[string]any)["name"].(string)] = object
	}
	f.server = resourceTestServer(t, f.serveHTTP)
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.denyWatch = true
		for id, events := range f.watches {
			close(events)
			delete(f.watches, id)
		}
	})
	return f
}

func (f *resourceWatchFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	path := "/api/v1/namespaces/default/" + f.resource
	barrierRV := 0
	f.mu.Lock()
	if r.Header.Get("Authorization") != f.expectedAuth {
		f.mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","reason":"Unauthorized","code":401}`))
		return
	}
	if r.URL.Path == path && r.URL.Query().Get("watch") == "true" {
		if f.denyWatch {
			f.mu.Unlock()
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","reason":"Forbidden","code":403}`))
			return
		}
		f.nextWatch++
		id, events := f.nextWatch, make(chan resourceWatchEvent, 32)
		f.watches[id] = events
		initial := []resourceWatchEvent{}
		if r.URL.Query().Get("sendInitialEvents") == "true" {
			for _, object := range f.objects {
				initial = append(initial, resourceWatchEvent{Type: "ADDED", Object: (&unstructured.Unstructured{Object: object}).DeepCopy().Object})
			}
			initial = append(initial, resourceWatchEvent{Type: "BOOKMARK", Object: map[string]any{"apiVersion": "v1", "kind": f.kind, "metadata": map[string]any{"resourceVersion": strconv.Itoa(f.rv), "annotations": map[string]any{"k8s.io/initial-events-end": "true"}}}})
		}
		f.mu.Unlock()
		defer func() { f.mu.Lock(); delete(f.watches, id); f.mu.Unlock() }()
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()
		f.watchCalls.Add(1)
		encoder := json.NewEncoder(w)
		for _, event := range initial {
			if encoder.Encode(event) != nil {
				return
			}
			flusher.Flush()
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case event, ok := <-events:
				if !ok || encoder.Encode(event) != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
	if r.Method == http.MethodGet && r.URL.Path == path && strings.Contains(r.Header.Get("Accept"), "PartialObjectMetadataList") && f.barrierGate != nil {
		gate, entered, cancelled := f.barrierGate, f.barrierEntered, f.barrierCancelled
		if f.barrierSnapshot {
			barrierRV = f.rv
		}
		if f.barrierOnce {
			f.barrierGate = nil
		}
		f.mu.Unlock()
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
			select {
			case cancelled <- struct{}{}:
			default:
			}
			return
		case <-gate:
			f.mu.Lock()
		}
	}
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == path && strings.Contains(r.Header.Get("Accept"), "PartialObjectMetadataList"):
		f.barrierCalls.Add(1)
		if f.metadataUnsupported {
			w.WriteHeader(http.StatusNotAcceptable)
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","reason":"NotAcceptable","code":406}`))
			return
		}
		items := []map[string]any{}
		for _, object := range f.objects {
			items = append(items, map[string]any{"apiVersion": "meta.k8s.io/v1", "kind": "PartialObjectMetadata", "metadata": object["metadata"]})
			break
		}
		if barrierRV == 0 {
			barrierRV = f.rv
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "meta.k8s.io/v1", "kind": "PartialObjectMetadataList", "metadata": map[string]any{"resourceVersion": strconv.Itoa(barrierRV)}, "items": items})
	case r.Method == http.MethodGet && r.URL.Path == path:
		f.listCalls.Add(1)
		names := make([]string, 0, len(f.objects))
		for name := range f.objects {
			names = append(names, name)
		}
		sort.Strings(names)
		items := make([]map[string]any, 0, len(f.objects))
		for _, name := range names {
			items = append(items, f.objects[name])
		}
		offset := 0
		if token := r.URL.Query().Get("continue"); token != "" {
			offset, _ = strconv.Atoi(strings.TrimPrefix(token, "page-"))
		}
		if offset > len(items) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		end := len(items)
		if limit, _ := strconv.Atoi(r.URL.Query().Get("limit")); limit > 0 && offset+limit < end {
			end = offset + limit
		}
		continuation := ""
		if end < len(items) {
			continuation = fmt.Sprintf("page-%d", end)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": f.kind + "List", "metadata": map[string]any{"resourceVersion": strconv.Itoa(f.rv), "continue": continuation}, "items": items[offset:end]})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, path+"/"):
		object := f.objects[strings.TrimPrefix(r.URL.Path, path+"/")]
		if object == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(object)
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, path+"/"):
		var object map[string]any
		if json.NewDecoder(r.Body).Decode(&object) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.rv++
		object["metadata"].(map[string]any)["resourceVersion"] = strconv.Itoa(f.rv)
		f.objects[strings.TrimPrefix(r.URL.Path, path+"/")] = object
		_ = json.NewEncoder(w).Encode(object)
	default:
		http.NotFound(w, r)
	}
}

func watchTestPod(phase string) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "web", "namespace": "default", "uid": "pod-web", "resourceVersion": "1"}, "status": map[string]any{"phase": phase}}
}

func (f *resourceWatchFixture) replace(object map[string]any, eventType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rv++
	copy := (&unstructured.Unstructured{Object: object}).DeepCopy().Object
	metadata := copy["metadata"].(map[string]any)
	metadata["resourceVersion"] = strconv.Itoa(f.rv)
	name := metadata["name"].(string)
	if eventType == "DELETED" {
		delete(f.objects, name)
	} else {
		f.objects[name] = copy
	}
	if eventType != "" {
		for _, events := range f.watches {
			events <- resourceWatchEvent{Type: eventType, Object: copy}
		}
	}
}

func startResourceWatchTest(t *testing.T, service *Service) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = service.RunResourceWatching(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("resource watch lifecycle did not stop after cancellation")
		}
	})
}

func waitResourceWatchState(t *testing.T, service *Service, id string, fixture *resourceWatchFixture, matches func(*ResourceList) bool) *ResourceList {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last *ResourceList
	var lastErr error
	for time.Now().Before(deadline) {
		before := fixture.listCalls.Load()
		last, lastErr = service.ListResources(context.Background(), id, fixture.resource, "default", "")
		if lastErr == nil && fixture.watchCalls.Load() > 0 && fixture.listCalls.Load() == before && matches(last) {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("watch snapshot was not served from cache: list=%v err=%v live_calls=%d watches=%d", last, lastErr, fixture.listCalls.Load(), fixture.watchCalls.Load())
	return nil
}

func podPhaseIs(phase string) func(*ResourceList) bool {
	return func(list *ResourceList) bool {
		return len(list.Items) == 1 && list.Items[0]["status"].(map[string]any)["phase"] == phase
	}
}

func TestResourceWatchCacheTracksModifiedDeletedAndFreshReads(t *testing.T) {
	f := newResourceWatchFixture(t, "pods", "Pod", watchTestPod("Pending"))
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, f.server)
	startResourceWatchTest(t, s)
	list := waitResourceWatchState(t, s, id, f, podPhaseIs("Pending"))
	// Consumers must receive their own copy, rather than mutate the shared store.
	list.Items[0]["status"].(map[string]any)["phase"] = "Corrupted"
	waitResourceWatchState(t, s, id, f, podPhaseIs("Pending"))
	f.replace(watchTestPod("Running"), "MODIFIED")
	waitResourceWatchState(t, s, id, f, podPhaseIs("Running"))
	f.replace(watchTestPod("Succeeded"), "")
	before := f.listCalls.Load()
	fresh, err := s.ListResources(WithFreshResourceRead(context.Background()), id, "pods", "default", "")
	if err != nil || !podPhaseIs("Succeeded")(fresh) || f.listCalls.Load() <= before {
		t.Fatalf("fresh read did not query current API state: list=%v err=%v", fresh, err)
	}
	object, err := s.GetResource(context.Background(), id, "pods", "default", "web", false)
	if err != nil || object["status"].(map[string]any)["phase"] != "Succeeded" {
		t.Fatalf("resource detail was served from a stale watch: object=%v err=%v", object, err)
	}
	f.replace(watchTestPod("Succeeded"), "DELETED")
	waitResourceWatchState(t, s, id, f, func(list *ResourceList) bool { return len(list.Items) == 0 })
}

func TestResourceWatchCacheDisconnectFallsBackToLiveAPI(t *testing.T) {
	f := newResourceWatchFixture(t, "pods", "Pod", watchTestPod("Pending"))
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, f.server)
	startResourceWatchTest(t, s)
	waitResourceWatchState(t, s, id, f, podPhaseIs("Pending"))
	f.replace(watchTestPod("Running"), "")
	f.mu.Lock()
	f.denyWatch = true
	for id, events := range f.watches {
		close(events)
		delete(f.watches, id)
	}
	f.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		before := f.listCalls.Load()
		list, err := s.ListResources(context.Background(), id, "pods", "default", "")
		if err == nil && podPhaseIs("Running")(list) && f.listCalls.Load() > before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("disconnected watcher kept serving the old snapshot instead of querying the API")
}

func TestResourceWatchCacheVersionBarrierRejectsLaggingSnapshot(t *testing.T) {
	f := newResourceWatchFixture(t, "pods", "Pod", watchTestPod("Pending"))
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, f.server)
	startResourceWatchTest(t, s)
	waitResourceWatchState(t, s, id, f, podPhaseIs("Pending"))
	// Keep the watch connected but withhold its MODIFIED event. Connection
	// health alone must not let an old snapshot pass a current version barrier.
	f.replace(watchTestPod("Running"), "")
	before := f.listCalls.Load()
	list, err := s.ListResources(context.Background(), id, "pods", "default", "")
	if err != nil || !podPhaseIs("Running")(list) || f.listCalls.Load() <= before {
		t.Fatalf("lagging watch bypassed the API version barrier: list=%v err=%v live_calls=%d barriers=%d", list, err, f.listCalls.Load(), f.barrierCalls.Load())
	}
}

func TestResourceWatchCacheUnsupportedMetadataFallsBackToLiveList(t *testing.T) {
	f := newResourceWatchFixture(t, "pods", "Pod", watchTestPod("Pending"))
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, f.server)
	startResourceWatchTest(t, s)
	waitResourceWatchState(t, s, id, f, podPhaseIs("Pending"))
	f.replace(watchTestPod("Running"), "")
	f.mu.Lock()
	f.metadataUnsupported = true
	f.mu.Unlock()
	before := f.listCalls.Load()
	list, err := s.ListResources(context.Background(), id, "pods", "default", "")
	if err != nil || !podPhaseIs("Running")(list) || f.listCalls.Load() <= before {
		t.Fatalf("unsupported metadata used an unverifiable cached list: list=%v err=%v", list, err)
	}
}

func TestResourceWatchCacheDoesNotMaskRevokedAPICredentials(t *testing.T) {
	f := newResourceWatchFixture(t, "pods", "Pod", watchTestPod("Pending"))
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, f.server)
	startResourceWatchTest(t, s)
	waitResourceWatchState(t, s, id, f, podPhaseIs("Pending"))
	f.mu.Lock()
	f.expectedAuth = "Bearer revoked-token"
	f.mu.Unlock()
	if _, err := s.ListResources(context.Background(), id, "pods", "default", ""); !errors.Is(err, ErrResourceForbidden) {
		t.Fatalf("warm cache masked an API authorization failure: %v", err)
	}
}

func TestResourceWatchCacheRechecksProjectAndActiveState(t *testing.T) {
	f := newResourceWatchFixture(t, "pods", "Pod", watchTestPod("Pending"))
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, f.server)
	if err := s.db.Model(&model.KubernetesCluster{}).Where("uuid = ?", id).Update("project_id", "project-a").Error; err != nil {
		t.Fatal(err)
	}
	startResourceWatchTest(t, s)
	waitResourceWatchState(t, s, id, f, podPhaseIs("Pending"))
	before := f.listCalls.Load()
	other := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "project-b", DepartmentID: database.DefaultDepartmentID, AllDepartments: true})
	if _, err := s.ListResources(other, id, "pods", "default", ""); !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("warm watch cache bypassed project scope: %v", err)
	}
	if f.listCalls.Load() != before {
		t.Fatal("cross-project request reached the cluster API")
	}
	if err := s.SetActive(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListResources(context.Background(), id, "pods", "default", ""); !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("warm watch cache bypassed disabled-cluster check: %v", err)
	}
}

func TestResourceWatchCacheConnectionChangeDiscardsOldSnapshot(t *testing.T) {
	f := newResourceWatchFixture(t, "pods", "Pod", watchTestPod("Pending"))
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, f.server)
	startResourceWatchTest(t, s)
	waitResourceWatchState(t, s, id, f, podPhaseIs("Pending"))
	f.replace(watchTestPod("Running"), "")
	f.mu.Lock()
	f.expectedAuth = "Bearer rotated-token"
	f.mu.Unlock()
	kubeconfig := strings.Replace(kubeconfigForServer(f.server), "token: test-token", "token: rotated-token", 1)
	if _, err := s.Update(context.Background(), id, Input{Name: "test-cluster", Mode: model.KubernetesKubeconfig, DefaultNamespace: "default", Kubeconfig: &kubeconfig}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListResources(context.Background(), id, "pods", "default", "")
	if err != nil || !podPhaseIs("Running")(list) {
		t.Fatalf("credential rotation reused the previous client or snapshot: list=%v err=%v", list, err)
	}
}

func TestResourceWatchCacheWriteInvalidatesBeforeFollowupRead(t *testing.T) {
	object := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "settings", "namespace": "default", "uid": "config-settings", "resourceVersion": "1"}, "data": map[string]any{"value": "old"}}
	f := newResourceWatchFixture(t, "configmaps", "ConfigMap", object)
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, f.server)
	startResourceWatchTest(t, s)
	waitResourceWatchState(t, s, id, f, func(list *ResourceList) bool {
		return len(list.Items) == 1 && list.Items[0]["data"].(map[string]any)["value"] == "old"
	})
	update := (&unstructured.Unstructured{Object: object}).DeepCopy().Object
	update["data"] = map[string]any{"value": "new"}
	if _, err := s.SaveResource(context.Background(), id, "configmaps", "default", "settings", update, false); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListResources(context.Background(), id, "configmaps", "default", "")
	if err != nil || len(list.Items) != 1 || list.Items[0]["data"].(map[string]any)["value"] != "new" {
		t.Fatalf("read after successful write returned the pre-write snapshot: list=%v err=%v", list, err)
	}
}

func TestResourceWatchCacheKeepsSecretProjectionRedacted(t *testing.T) {
	object := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "private", "namespace": "default", "uid": "secret-private", "resourceVersion": "1", "annotations": map[string]any{"private": "private-value"}}, "type": "Opaque", "data": map[string]any{"token": "cHJpdmF0ZS12YWx1ZQ=="}, "stringData": map[string]any{"token": "private-value"}}
	f := newResourceWatchFixture(t, "secrets", "Secret", object)
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, f.server)
	startResourceWatchTest(t, s)
	list := waitResourceWatchState(t, s, id, f, func(list *ResourceList) bool { return len(list.Items) == 1 })
	body, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "private-value") || strings.Contains(string(body), "cHJpdmF0ZS12YWx1ZQ==") || strings.Contains(string(body), "annotations") {
		t.Fatalf("cached Secret list leaked a value: %s", body)
	}
	keys, ok := list.Items[0]["keys"].([]any)
	if !ok || fmt.Sprint(keys) != "[token]" {
		t.Fatalf("Secret key names were lost while projecting the cached object: %v", list)
	}
}

func TestResourceWatchCacheSecretScopeErrorsRemainNotFound(t *testing.T) {
	object := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "private", "namespace": "default", "uid": "secret-private", "resourceVersion": "1"}, "type": "Opaque", "data": map[string]any{"token": "dGVzdA=="}}
	f := newResourceWatchFixture(t, "secrets", "Secret", object)
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, f.server)
	if err := s.db.Model(&model.KubernetesCluster{}).Where("uuid = ?", id).Update("project_id", "project-a").Error; err != nil {
		t.Fatal(err)
	}
	startResourceWatchTest(t, s)
	waitResourceWatchState(t, s, id, f, func(list *ResourceList) bool { return len(list.Items) == 1 })
	other := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "project-b", DepartmentID: database.DefaultDepartmentID, AllDepartments: true})
	if _, err := s.ListResources(other, id, "secrets", "default", ""); !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("Secret error redaction changed a project scope rejection into an API failure: %v", err)
	}
}

// These state-only informers isolate subscriber behavior from network timing.
// Full LIST/WATCH synchronization is exercised by the TLS tests above.
type resourceWatchSyncedState struct {
	cache.SharedIndexInformer
	synced bool
}

func (s *resourceWatchSyncedState) HasSynced() bool { return s.synced }

func watchSubscriptionStateFixture() (*clusterQueries, chan ResourceChange, *resourceObserver, *resourceObserver) {
	pods, _ := ResourceType("pods")
	nodes, _ := ResourceType("nodes")
	podKey, nodeKey := resourceWatchKey(pods, "default"), resourceWatchKey(nodes, "")
	newObserver := func(def ResourceDefinition, key string) *resourceObserver {
		entry := &resourceObserver{key: key, definition: def, informer: &resourceWatchSyncedState{synced: true}}
		entry.active.Store(true)
		return entry
	}
	pod, node := newObserver(pods, podKey), newObserver(nodes, nodeKey)
	changes := make(chan ResourceChange, 64)
	q := &clusterQueries{
		watchers: map[string]*resourceObserver{podKey: pod, nodeKey: node},
		subscribers: map[chan ResourceChange]resourceSubscription{
			changes: {namespaces: map[string]bool{"default": true}, allowed: map[string]bool{"kubernetes.workload.read": true, "kubernetes.node.read": true}, keys: []string{podKey, nodeKey}},
		},
	}
	return q, changes, pod, node
}

func drainResourceWatchChanges(changes <-chan ResourceChange) []ResourceChange {
	result := []ResourceChange{}
	for {
		select {
		case event, open := <-changes:
			if !open {
				return result
			}
			result = append(result, event)
		default:
			return result
		}
	}
}

func assertResourceWatchControlClean(t *testing.T, changes []ResourceChange) {
	t.Helper()
	for _, event := range changes {
		if event.Type == "degraded" || event.Type == "synced" || event.Type == "reset" {
			if event.Resource != "" || event.Namespace != "" || event.Name != "" {
				t.Fatalf("control event leaked a resource identity: %+v", event)
			}
		}
	}
}

func TestResourceWatchCacheSubscriptionHealthRequiresEveryResource(t *testing.T) {
	q, changes, _, node := watchSubscriptionStateFixture()
	q.publish(ResourceChange{Type: "synced"})
	initial := drainResourceWatchChanges(changes)
	if len(initial) != 1 || initial[0].Type != "synced" {
		t.Fatalf("fully synchronized subscription was not ready: %+v", initial)
	}
	node.failed.Store(true)
	q.publish(ResourceChange{Resource: "nodes", Name: "private-node", Type: "degraded"})
	q.publish(ResourceChange{Resource: "pods", Namespace: "default", Name: "web", Type: "ADDED"})
	q.publish(ResourceChange{Resource: "pods", Namespace: "default", Name: "web", Type: "synced"})
	failed := drainResourceWatchChanges(changes)
	if len(failed) < 1 || failed[0].Type != "degraded" {
		t.Fatalf("failed resource did not degrade the subscription: %+v", failed)
	}
	for _, event := range failed {
		if event.Type == "synced" {
			t.Fatalf("healthy Pod observer hid a failed Node observer: %+v", failed)
		}
	}
	assertResourceWatchControlClean(t, failed)
	node.failed.Store(false)
	node.informer.(*resourceWatchSyncedState).synced = false
	q.publish(ResourceChange{Type: "synced"})
	if events := drainResourceWatchChanges(changes); len(events) != 0 {
		t.Fatalf("initializing resource falsely marked the subscription healthy: %+v", events)
	}
	node.informer.(*resourceWatchSyncedState).synced = true
	q.publish(ResourceChange{Type: "synced"})
	recovered := drainResourceWatchChanges(changes)
	if len(recovered) != 1 || recovered[0].Type != "synced" {
		t.Fatalf("subscription did not recover when every resource synchronized: %+v", recovered)
	}
	assertResourceWatchControlClean(t, recovered)
}

func TestResourceWatchCacheSlowSubscriberResetsWithoutLosingDegradedState(t *testing.T) {
	q, changes, _, node := watchSubscriptionStateFixture()
	q.publish(ResourceChange{Type: "synced"})
	_ = drainResourceWatchChanges(changes)
	for i := 0; i < cap(changes); i++ {
		q.publish(ResourceChange{Resource: "pods", Namespace: "default", Name: fmt.Sprintf("web-%d", i), Type: "ADDED"})
	}
	node.failed.Store(true)
	q.publish(ResourceChange{Resource: "nodes", Name: "private-node", Type: "degraded"})
	q.publish(ResourceChange{Resource: "pods", Namespace: "default", Name: "web-after-failure", Type: "MODIFIED"})
	events := drainResourceWatchChanges(changes)
	reset, degraded := false, false
	for _, event := range events {
		reset = reset || event.Type == "reset"
		degraded = degraded || event.Type == "degraded"
		if event.Type == "synced" {
			t.Fatalf("overflow marked a partly failed subscription healthy: %+v", events)
		}
	}
	if !reset || !degraded {
		t.Fatalf("slow subscriber lost resynchronization or health state: %+v", events)
	}
	assertResourceWatchControlClean(t, events)
}

func TestResourceWatchCacheResetControlDoesNotExposeResourceIdentity(t *testing.T) {
	q, changes, _, _ := watchSubscriptionStateFixture()
	q.publish(ResourceChange{Type: "synced"})
	_ = drainResourceWatchChanges(changes)
	q.publish(ResourceChange{Resource: "pods", Namespace: "default", Name: "private-resource", Type: "reset"})
	events := drainResourceWatchChanges(changes)
	if len(events) != 1 || events[0].Type != "reset" {
		t.Fatalf("expected one reset control event: %+v", events)
	}
	assertResourceWatchControlClean(t, events)
}

func TestResourceWatchCacheEachReadUsesIndependentMetadataFence(t *testing.T) {
	f := newResourceWatchFixture(t, "pods", "Pod", watchTestPod("Pending"))
	s := newKubeTestService(t, 5*time.Second)
	id := createKubeTestCluster(t, s, f.server)
	startResourceWatchTest(t, s)
	waitResourceWatchState(t, s, id, f, podPhaseIs("Pending"))
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	var release sync.Once
	releaseFirst := func() { release.Do(func() { close(gate) }) }
	defer releaseFirst()
	f.mu.Lock()
	f.barrierGate, f.barrierEntered = gate, entered
	f.barrierCancelled = make(chan struct{}, 1)
	f.barrierOnce, f.barrierSnapshot = true, true
	f.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstDone := make(chan error, 1)
	go func() { _, err := s.ListResources(ctx, id, "pods", "default", ""); firstDone <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first read did not capture its metadata version")
	}
	// The first metadata response has captured the old version but is delayed.
	// Complete an external write without delivering its Watch event, then begin
	// a second read. It must fence after this write rather than join the old one.
	f.replace(watchTestPod("Running"), "")
	before := f.listCalls.Load()
	type outcome struct {
		list *ResourceList
		err  error
	}
	secondDone := make(chan outcome, 1)
	go func() {
		list, err := s.ListResources(ctx, id, "pods", "default", "")
		secondDone <- outcome{list: list, err: err}
	}()
	select {
	case result := <-secondDone:
		if result.err != nil || result.list == nil || !podPhaseIs("Running")(result.list) || f.listCalls.Load() <= before {
			t.Fatalf("later read reused a version fence captured before its preceding write: list=%v err=%v", result.list, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("later read joined an older in-flight metadata fence instead of independently checking the current version")
	}
	releaseFirst()
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first read did not finish after its response was released: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first read remained blocked after releasing its metadata response")
	}
}

func TestResourceWatchCacheMetadataCancellationAndLifecycleStopReleaseRequests(t *testing.T) {
	f := newResourceWatchFixture(t, "pods", "Pod", watchTestPod("Pending"))
	s := newKubeTestService(t, 5*time.Second)
	id := createKubeTestCluster(t, s, f.server)
	lifetime, stop := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { defer close(stopped); _ = s.RunResourceWatching(lifetime) }()
	t.Cleanup(stop)
	waitResourceWatchState(t, s, id, f, podPhaseIs("Pending"))
	gate, entered, barrierCancelled := make(chan struct{}), make(chan struct{}, 1), make(chan struct{}, 1)
	t.Cleanup(func() { close(gate) })
	f.mu.Lock()
	f.barrierGate, f.barrierEntered, f.barrierCancelled = gate, entered, barrierCancelled
	f.mu.Unlock()
	request, cancel := context.WithCancel(context.Background())
	defer cancel()
	response := make(chan error, 1)
	go func() { _, err := s.ListResources(request, id, "pods", "default", ""); response <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cached read did not enter the metadata version fence")
	}
	cancel()
	select {
	case err := <-response:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled caller received an old cached success: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("caller cancellation waited for the shared metadata HTTP request")
	}
	stop()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("resource watch lifecycle remained blocked on a metadata fence")
	}
	select {
	case <-barrierCancelled:
	case <-time.After(time.Second):
		t.Fatal("lifecycle cancellation left a metadata HTTP request running")
	}
	deadline := time.Now().Add(time.Second)
	for len(s.queries.watchSlots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.queries.watchSlots) != 0 {
		t.Fatal("stopped informer retained its shared watch slot")
	}
	s.queries.mu.Lock()
	remaining := len(s.queries.clusters)
	s.queries.mu.Unlock()
	if remaining != 0 {
		t.Fatal("stopped lifecycle retained cluster observers")
	}
}

func TestResourceWatchCacheLargeSnapshotPreservesNativePublicPagination(t *testing.T) {
	f := newResourceWatchFixture(t, "pods", "Pod", nil)
	f.mu.Lock()
	for i := 0; i < 501; i++ {
		pod := watchTestPod("Running")
		metadata := pod["metadata"].(map[string]any)
		name := fmt.Sprintf("web-%03d", i)
		metadata["name"], metadata["uid"] = name, name
		f.objects[name] = pod
	}
	f.mu.Unlock()
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, f.server)
	startResourceWatchTest(t, s)
	def, _ := ResourceType("pods")
	deadline := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		before := f.listCalls.Load()
		snapshot, err := s.listResourceSnapshotPage(context.Background(), id, def, "default", metav1.ListOptions{Limit: 500})
		if err == nil && len(snapshot.Items) == 501 && snapshot.GetContinue() == "" && f.listCalls.Load() == before {
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("aggregate reader did not use a complete watch snapshot larger than one native page")
	}
	before := f.listCalls.Load()
	first, err := s.ListResources(context.Background(), id, "pods", "default", "")
	if err != nil || len(first.Items) != 500 || first.Continue != "page-500" || f.listCalls.Load() <= before {
		t.Fatalf("public first page replaced the API's native limit/token with a whole snapshot: list=%v err=%v", first, err)
	}
	second, err := s.ListResources(context.Background(), id, "pods", "default", first.Continue)
	if err != nil || len(second.Items) != 1 || second.Continue != "" || second.Items[0]["metadata"].(map[string]any)["name"] != "web-500" {
		t.Fatalf("native continuation did not return the remaining object: list=%v err=%v", second, err)
	}
	before = f.listCalls.Load()
	snapshot, err := s.listResourceSnapshotPage(context.Background(), id, def, "default", metav1.ListOptions{Limit: 500})
	if err != nil || len(snapshot.Items) != 501 || snapshot.GetContinue() != "" || f.listCalls.Load() != before {
		t.Fatalf("aggregate reader lost its complete cache snapshot after public pagination: items=%v err=%v", snapshot, err)
	}
}
