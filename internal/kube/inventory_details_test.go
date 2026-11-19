package kube

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestInventoryDetailsNeverFetchResourcesOnOpen(t *testing.T) {
	var requests atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		t.Errorf("打开资源详情触发了上游资源请求: %s", r.URL)
		http.Error(w, "unexpected resource read", http.StatusInternalServerError)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	cluster, err := service.Find(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureInventoryMonitoring()
	read := func() context.Context { return WithInventoryRead(context.Background()) }
	checks := []struct {
		name string
		run  func() error
	}{
		{"diagnostics", func() error {
			_, err := service.ResourceDiagnostics(read(), id, "deployments", "demo", "web", "deployment-1")
			return err
		}},
		{"events", func() error {
			_, err := service.ResourceEvents(read(), id, "deployments", "demo", "web", "deployment-1")
			return err
		}},
		{"replica set YAML", func() error {
			_, err := service.DeploymentReplicaSetManifest(read(), id, "demo", "web", "deployment-1", "web-old", "revision-1")
			return err
		}},
		{"topology", func() error {
			_, err := service.NodeTopology(read(), id, map[string]bool{"kubernetes.node.read": true}, true)
			return err
		}},
	}
	for _, check := range checks {
		if err := check.run(); !errors.Is(err, ErrInventoryPending) {
			t.Fatalf("%s 缺少快照时没有等待后台: %v", check.name, err)
		}
	}
	seed := func(def ResourceDefinition, objects ...runtime.Object) {
		t.Helper()
		collection := &inventoryCollection{at: time.Now().UTC(), signatures: make(map[string][32]byte)}
		for _, object := range objects {
			value, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
			if err != nil {
				t.Fatal(err)
			}
			if err := collection.add(def, &unstructured.Unstructured{Object: value}); err != nil {
				t.Fatal(err)
			}
		}
		key := inventoryKey(id, clusterQueryVersion(cluster), def, "")
		service.inventory.store(key, def, collection, nil)
	}
	replicas := int32(1)
	template := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "example/web:v1"}}},
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "demo", UID: types.UID("deployment-1")},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}, Template: template},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1, AvailableReplicas: 1},
	}
	controller := true
	revision := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "web-old", Namespace: "demo", UID: types.UID("revision-1"), Labels: map[string]string{"app": "web"}, OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: deployment.UID, Controller: &controller}}},
		Spec:       appsv1.ReplicaSetSpec{Replicas: &replicas, Template: template},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "demo", UID: types.UID("pod-1"), Labels: map[string]string{"app": "web"}, OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", UID: revision.UID, Controller: &controller}}},
		Spec:       corev1.PodSpec{NodeName: "worker", Containers: template.Spec.Containers},
	}
	nodes, _ := ResourceType("nodes")
	pods, _ := ResourceType("pods")
	deployments, _ := ResourceType("deployments")
	services, _ := ResourceType("services")
	seed(nodes, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: types.UID("node-1")}})
	seed(pods, pod)
	seed(deployments, deployment)
	seed(inventoryReplicaSets, revision)
	seed(inventoryEvents, &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "web-event", Namespace: "demo"}, InvolvedObject: corev1.ObjectReference{UID: deployment.UID}, Reason: "Scaled"})
	seed(services, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web-service", Namespace: "demo", UID: types.UID("service-1")}})
	seed(inventoryEndpointSlices, &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "web-slice", Namespace: "demo", Labels: map[string]string{discoveryv1.LabelServiceName: "web-service"}},
		Endpoints:  []discoveryv1.Endpoint{{Addresses: []string{"192.0.2.1"}}},
	})
	for _, kind := range []string{"nodes", "pods"} {
		seed(inventoryMetricDefinition(kind))
	}
	diagnostics, err := service.ResourceDiagnostics(read(), id, "deployments", "demo", "web", "deployment-1")
	if err != nil || len(diagnostics.Revisions) != 1 || len(diagnostics.Revisions[0].Pods) != 1 {
		t.Fatalf("历史版本没有复用已同步资源: %+v %v", diagnostics, err)
	}
	endpoints, err := service.ResourceDiagnostics(read(), id, "services", "demo", "web-service", "service-1")
	if err != nil || len(endpoints.Endpoints) != 1 || endpoints.Endpoints[0].Ready != nil {
		t.Fatalf("Service 端点未读取快照或编造了就绪状态: %+v %v", endpoints, err)
	}
	events, err := service.ResourceEvents(read(), id, "deployments", "demo", "web", "deployment-1")
	if err != nil || len(events) != 1 || events[0].Reason != "Scaled" {
		t.Fatalf("事件没有从快照读取: %+v %v", events, err)
	}
	manifest, err := service.DeploymentReplicaSetManifest(read(), id, "demo", "web", "deployment-1", "web-old", "revision-1")
	if err != nil || manifest.UID != revision.UID {
		t.Fatalf("副本集 YAML 没有从准确版本读取: %+v %v", manifest, err)
	}
	topology, err := service.NodeTopology(read(), id, map[string]bool{"kubernetes.node.read": true, "kubernetes.workload.read": true}, true)
	if err != nil || len(topology.Nodes) != 1 || topology.Nodes[0].Pods.Count == nil || *topology.Nodes[0].Pods.Count != 1 {
		t.Fatalf("拓扑没有从节点和 Pod 快照组合: %+v %v", topology, err)
	}
	state, snapshot, err := service.CachedDeploymentState(read(), id, "demo", "web", "deployment-1")
	if err != nil || state == nil || !state.Running || snapshot.CollectedAt == nil {
		t.Fatalf("应用部署实例没有从同一快照读取: %+v %+v %v", state, snapshot, err)
	}
	if _, err := service.NodeHistory(read(), id, "worker", "node-1", 15*time.Minute, true); !errors.Is(err, ErrNodeHistoryUnavailable) {
		t.Fatalf("未配置指标存储应准确报不可用: %v", err)
	}
	if _, _, err := service.CachedDeploymentState(read(), id, "demo", "web", "replacement-uid"); !errors.Is(err, ErrResourceConflict) {
		t.Fatalf("部署实例缓存绕过了工作负载身份: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("展示请求访问了远程资源 %d 次", requests.Load())
	}
}
