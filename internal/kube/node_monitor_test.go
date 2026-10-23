package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"testing"
	"time"

	"edo/internal/database"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	resourcehelper "k8s.io/component-helpers/resource"
)

func testNodeMonitor(t *testing.T, s *Service) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	s.ConfigureNodeMonitoring(client, "test:kube:node", slog.New(slog.NewTextHandler(io.Discard, nil)))
	return server
}
func TestNodeSampleUsesOfficialSchedulingResourcesAndRealMetrics(t *testing.T) {
	now := time.Now().UTC()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: types.UID("n1"), CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}, Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi"), corev1.ResourcePods: resource.MustParse("110")}}}
	resources := func(cpu, mem string) corev1.ResourceRequirements {
		return corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}}
	}
	always := corev1.ContainerRestartPolicyAlways
	pod := corev1.Pod{Spec: corev1.PodSpec{NodeName: "worker", Containers: []corev1.Container{{Name: "app", Resources: resources("500m", "128Mi")}}, InitContainers: []corev1.Container{{Name: "sidecar", RestartPolicy: &always, Resources: resources("100m", "64Mi")}, {Name: "init", Resources: resources("2", "256Mi")}}, Overhead: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("16Mi")}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	completed := pod.DeepCopy()
	completed.Status.Phase = corev1.PodSucceeded
	other := pod.DeepCopy()
	other.Spec.NodeName = "other"
	allocations := map[string]nodePodAllocation{}
	for _, item := range []*corev1.Pod{&pod, completed, other} {
		accumulateNodePod(allocations, item, resourcehelper.PodResourcesOptions{UseStatusResources: true})
	}
	point := buildNodeSample(node, allocations[node.Name], ResourceUsage{CPU: 0, Memory: 1024, Timestamp: now.Format(time.RFC3339Nano)}, now)
	if point.CPU == nil || *point.CPU != 0 || point.MetricsState != "ok" || *point.Pods != 2 {
		t.Fatalf("真实零值或全命名空间 Pod 记录不正确: %+v", point)
	}
	if math.Abs(point.Allocation.CPURequests-2.15) > 1e-8 || point.Allocation.MemoryRequests != 336*1024*1024 {
		t.Fatalf("Init/Sidecar/Overhead 汇总错误: %+v", point.Allocation)
	}
	for _, at := range []string{now.Add(-3 * time.Minute).Format(time.RFC3339), now.Add(2 * time.Minute).Format(time.RFC3339), "bad"} {
		point = buildNodeSample(node, nodePodAllocation{}, ResourceUsage{Timestamp: at}, now)
		if point.CPU != nil || point.Memory != nil || point.MetricsState == "ok" {
			t.Fatal("过期或无效指标伪装成零")
		}
	}
}
func TestNodeMonitoringHistoryKeepsUIDPermissionsRetentionAndRestart(t *testing.T) {
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Node","metadata":{"name":"worker","uid":"n1"}}`)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	redisServer := testNodeMonitor(t, service)
	ctx := context.Background()
	now := time.Now().UTC()
	pods := int64(0)
	for _, at := range []time.Time{now.Add(-73 * time.Hour), now.Add(-2 * time.Hour), now.Add(-30 * time.Second), now} {
		if err := service.nodeMonitor.append(ctx, id, "n1", NodeSample{CollectedAt: at, Pods: &pods, Allocation: &NodeAllocation{CPURequests: 1}, PodsState: "ok", MetricsState: "missing"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.nodeMonitor.append(ctx, id, "other-uid", NodeSample{CollectedAt: now}); err != nil {
		t.Fatal(err)
	}
	restarted := newKubeTestService(t, time.Second)
	restarted.db = service.db
	restarted.secrets = service.secrets
	restarted.nodeMonitor = service.nodeMonitor
	history, err := restarted.NodeHistory(ctx, id, "worker", "n1", NodeHistoryRetention, false)
	if err != nil || len(history.Points) < 2 || len(history.Points) > 3 {
		t.Fatalf("历史保留不正确 %+v %v", history, err)
	}
	for _, p := range history.Points {
		if p.Pods != nil || p.Allocation != nil || p.PodsState != "no_permission" {
			t.Fatal("泄露工作负载统计")
		}
	}
	full, err := service.NodeHistory(ctx, id, "worker", "n1", time.Hour, true)
	if err != nil || len(full.Points) == 0 || full.Points[0].Pods == nil {
		t.Fatal("授权历史缺失", err)
	}
	if _, err = service.NodeHistory(ctx, id, "worker", "other-uid", time.Hour, true); !errors.Is(err, ErrResourceConflict) {
		t.Fatal("同名新节点拼接旧历史", err)
	}
	scoped := database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: "other-project", DepartmentID: database.DefaultDepartmentID})
	if _, err = service.NodeHistory(scoped, id, "worker", "n1", time.Hour, true); !errors.Is(err, ErrClusterNotFound) {
		t.Fatal("跨项目读取历史", err)
	}
	for _, duration := range []time.Duration{-time.Hour, time.Second, 48 * time.Hour} {
		if _, err = service.NodeHistory(ctx, id, "worker", "n1", duration, true); !errors.Is(err, ErrNodeHistoryRange) {
			t.Fatal("接受无界窗口", err)
		}
	}
	redisServer.FastForward(73 * time.Hour)
	history, err = service.NodeHistory(ctx, id, "worker", "n1", time.Hour, true)
	if err != nil || len(history.Points) != 0 {
		t.Fatal("停止采集后未过期", err)
	}
}
func TestNodeCollectorRunsWithoutBrowserAndMarksUnavailable(t *testing.T) {
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"worker","uid":"n1"},"status":{"allocatable":{"cpu":"4","memory":"8Gi","pods":"110"}}}]}`)
		case "/api/v1/nodes/worker":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Node","metadata":{"name":"worker","uid":"n1"}}`)
		default:
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"Forbidden","code":403}`)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	testNodeMonitor(t, service)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.RunNodeMonitoring(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	key := service.nodeMonitor.key(id, "n1")
	for service.nodeMonitor.client.ZCard(ctx, key).Val() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	result, err := service.NodeHistory(ctx, id, "worker", "n1", time.Hour, true)
	if err != nil || len(result.Points) != 1 {
		t.Fatalf("后台没有独立采集: %+v %v", result, err)
	}
	p := result.Points[0]
	if p.CPU != nil || p.Pods != nil || p.Allocation != nil || p.MetricsState != "forbidden" || p.PodsState != "forbidden" {
		t.Fatalf("错误采样被补成零: %+v", p)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("采集取消没有退出")
	}
}
func TestNodeCollectorRejectsPartialPodPages(t *testing.T) {
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/pods" {
			_, _ = fmt.Fprint(w, `{"apiVersion":"v1","kind":"PodList","metadata":{"continue":"next"},"items":[]}`)
			return
		}
		w.WriteHeader(404)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	client, err := service.Clientset(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readNodePods(context.Background(), client); err == nil {
		t.Fatal("部分分页伪装成完整统计")
	}
}

func TestNodeViewDoesNotExposeJoinCredentialsInAnnotations(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Node", "metadata": map[string]any{"name": "worker", "annotations": map[string]any{"k3s.io/node-env": `{"K3S_TOKEN":"private-token"}`, "rke2.io/node-env": `{"RKE2_TOKEN":"private-token"}`, "description": "worker"}}}}
	view := resourceView("nodes", object, false)
	annotations := (&unstructured.Unstructured{Object: view}).GetAnnotations()
	if len(annotations) != 1 || annotations["description"] != "worker" {
		t.Fatal("节点详情和 YAML 暴露加入凭据")
	}
	if object.GetAnnotations()["k3s.io/node-env"] == "" {
		t.Fatal("脱敏修改了原对象")
	}
}
