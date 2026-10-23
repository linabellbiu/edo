package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	resourcehelper "k8s.io/component-helpers/resource"
)

func nodePodListClient(t testing.TB, handler http.HandlerFunc) kubernetes.Interface {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func nodeResourcePod(name, node, cpuRequest, cpuLimit, memoryRequest, memoryLimit string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team"},
		Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpuRequest), corev1.ResourceMemory: resource.MustParse(memoryRequest)},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpuLimit), corev1.ResourceMemory: resource.MustParse(memoryLimit)},
		}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func TestNodePodPagesAggregateOnlyAssignedNodesAndKeepResourcePrecision(t *testing.T) {
	first := nodeResourcePod("first", "worker", "500m", "1", "128Mi", "256Mi")
	second := nodeResourcePod("second", "worker", "250m", "2", "64Mi", "512Mi")
	second.Namespace = "other-team"
	completed := nodeResourcePod("completed", "worker", "4", "8", "4Gi", "8Gi")
	completed.Status.Phase = corev1.PodSucceeded
	failed := completed.DeepCopy()
	failed.Name, failed.Status.Phase = "failed", corev1.PodFailed
	unassigned := nodeResourcePod("pending", "", "10", "10", "10Gi", "10Gi")
	other := nodeResourcePod("other", "other", "1m", "2m", "1", "2")
	calls := 0
	client := nodePodListClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/pods" || r.URL.Query().Get("limit") != "500" {
			t.Errorf("节点 Pod 采样范围不正确: %s", r.URL)
		}
		page := corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}}
		switch r.URL.Query().Get("continue") {
		case "":
			page.Continue, page.Items = "next", []corev1.Pod{first, completed, unassigned}
		case "next":
			page.Items = []corev1.Pod{second, *failed, other}
		default:
			t.Errorf("分页游标不正确: %s", r.URL)
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	totals, err := readNodePods(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	worker := totals["worker"]
	if calls != 2 || len(totals) != 2 || worker.count != 4 || totals["other"].count != 1 {
		t.Fatalf("分页、已结束记录或未调度 Pod 统计不正确: calls=%d totals=%+v", calls, totals)
	}
	if worker.allocation != (NodeAllocation{CPURequests: .75, CPULimits: 3, MemoryRequests: 192 * 1024 * 1024, MemoryLimits: 768 * 1024 * 1024}) {
		t.Fatalf("请求与限制未分别按原始精度汇总: %+v", worker.allocation)
	}
	if other := totals["other"].allocation; math.Abs(other.CPURequests-.001) > 1e-12 || math.Abs(other.CPULimits-.002) > 1e-12 || other.MemoryRequests != 1 || other.MemoryLimits != 2 {
		t.Fatalf("微小非零资源被舍入或受复用资源表影响: %+v", other)
	}
	point := buildNodeSample(&corev1.Node{}, nodePodAllocation{}, ResourceUsage{}, time.Now())
	if point.Pods == nil || *point.Pods != 0 || point.Allocation == nil || *point.Allocation != (NodeAllocation{}) {
		t.Fatalf("完整采样中的空节点未保留真实零值: %+v", point)
	}
}

func TestNodePodFailedPageDiscardsPreviouslyAggregatedTotals(t *testing.T) {
	client := nodePodListClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("continue") == "" {
			_ = json.NewEncoder(w).Encode(corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, ListMeta: metav1.ListMeta{Continue: "next"}, Items: []corev1.Pod{nodeResourcePod("first", "worker", "1", "2", "1Gi", "2Gi")}})
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"Forbidden","code":403}`)
	})
	totals, err := readNodePods(context.Background(), client)
	if err == nil || totals != nil {
		t.Fatalf("失败页返回了部分节点资源统计: %+v %v", totals, err)
	}
}

func TestNodePodPagesRejectMoreThanTotalObjectLimit(t *testing.T) {
	pods := make([]corev1.Pod, 10001)
	client := nodePodListClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: pods})
	})
	if totals, err := readNodePods(context.Background(), client); err == nil || totals != nil {
		t.Fatal("接受了超过总数量上限的 Pod 结果")
	}
}

func TestNodePodFailedPageKeepsHistoryUnavailable(t *testing.T) {
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"worker","uid":"n1"},"status":{"allocatable":{"cpu":"4","memory":"8Gi","pods":"110"}}}]}`)
		case "/api/v1/nodes/worker":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Node","metadata":{"name":"worker","uid":"n1"}}`)
		case "/api/v1/pods":
			if r.URL.Query().Get("continue") == "" {
				_ = json.NewEncoder(w).Encode(corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, ListMeta: metav1.ListMeta{Continue: "next"}, Items: []corev1.Pod{nodeResourcePod("first", "worker", "1", "2", "1Gi", "2Gi")}})
				return
			}
			fallthrough
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"Forbidden","code":403}`)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	testNodeMonitor(t, service)
	if err := service.collectClusterNodes(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	history, err := service.NodeHistory(context.Background(), id, "worker", "n1", time.Hour, true)
	if err != nil || len(history.Points) != 1 {
		t.Fatalf("未记录采样状态: %+v %v", history, err)
	}
	point := history.Points[0]
	if point.Pods != nil || point.Allocation != nil || point.PodsState != "forbidden" {
		t.Fatalf("部分采样伪装成有效历史: %+v", point)
	}
}

var nodePodBenchmarkResult map[string]nodePodAllocation

func BenchmarkNodePodAggregation(b *testing.B) {
	// 20 页覆盖 10,000 Pod 上限；不含网络解析，仅比较保留完整 Pod 结构的额外分配。
	page := make([]corev1.Pod, 500)
	for i := range page {
		page[i] = nodeResourcePod(fmt.Sprintf("pod-%d", i), "node-"+strconv.Itoa(i%20), "100m", "1", "32Mi", "128Mi")
		page[i].Annotations = map[string]string{"large": strings.Repeat("x", 8192)}
	}
	for _, retain := range []bool{false, true} {
		name := "流式聚合"
		if retain {
			name = "保留完整对象后聚合"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				allocations := map[string]nodePodAllocation{}
				options := resourcehelper.PodResourcesOptions{UseStatusResources: true, Reuse: corev1.ResourceList{}}
				var pods []corev1.Pod
				for range 20 {
					if retain {
						pods = append(pods, page...)
						continue
					}
					for i := range page {
						accumulateNodePod(allocations, &page[i], options)
					}
				}
				if retain {
					byNode := map[string][]corev1.Pod{}
					for _, pod := range pods {
						byNode[pod.Spec.NodeName] = append(byNode[pod.Spec.NodeName], pod)
					}
					for _, items := range byNode {
						for i := range items {
							accumulateNodePod(allocations, &items[i], options)
						}
					}
				}
				nodePodBenchmarkResult = allocations
			}
		})
	}
}
