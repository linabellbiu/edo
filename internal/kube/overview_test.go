package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOverviewUsesPermissionsPaginationAndRealMetrics(t *testing.T) {
	var calls []string
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/nodes":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"worker"},"status":{"allocatable":{"cpu":"2500m","memory":"2Gi"},"conditions":[{"type":"Ready","status":"True"}]}}]}`)
		case "/api/v1/pods":
			if r.URL.Query().Get("limit") != "500" {
				t.Error("缺少分页上限")
			}
			if r.URL.Query().Get("continue") == "" {
				_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"PodList","metadata":{"continue":"second"},"items":[{"status":{"phase":"Running"}}]}`)
			} else {
				_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"PodList","items":[{"status":{"phase":"Pending"}}]}`)
			}
		case "/apis/metrics.k8s.io/v1/nodes", "/apis/metrics.k8s.io/v1/pods":
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","reason":"NotFound","code":404}`)
		case "/apis/metrics.k8s.io/v1beta1/nodes":
			_, _ = fmt.Fprintf(w, `{"apiVersion":"metrics.k8s.io/v1beta1","kind":"NodeMetricsList","items":[{"metadata":{"name":"worker"},"timestamp":%q,"usage":{"cpu":"125000000n","memory":"128Mi"}}]}`, time.Now().UTC().Format(time.RFC3339))
		case "/apis/metrics.k8s.io/v1beta1/pods":
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","reason":"Forbidden","code":403}`)
		default:
			// 没有配置权限时不得为统计读取 Secret 等资源。
			t.Errorf("越过资源权限查询: %s", r.URL.Path)
			w.WriteHeader(403)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	// 工作负载目录还包含 Deployment 等；测试只聚焦节点，随后使用全部工作负载模拟器。
	result, err := service.Overview(context.Background(), id, "*", map[string]bool{"kubernetes.node.read": true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts["nodes"].Count != 1 || result.Nodes[0].CPU != 2.5 || result.Nodes[0].Memory != 2147483648 || result.Usage["nodes"]["worker"].CPU != 0.125 {
		t.Fatalf("统计值不正确: %+v", result)
	}
	for _, path := range calls {
		if strings.Contains(path, "pods") || strings.Contains(path, "secrets") {
			t.Fatal("统计越权", path)
		}
	}
	if _, ok := result.Counts["pods"]; ok {
		t.Fatal("未授权资源不能显示为零")
	}
}

func TestOverviewCountsAllPagesAndKeepsUnavailableDistinct(t *testing.T) {
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "metrics.k8s.io") {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"NotFound","code":404}`)
			return
		}
		if r.URL.Path == "/apis/apps/v1/namespaces/team/deployments" {
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"Forbidden","code":403}`)
			return
		}
		continuation := ""
		items := []any{}
		if r.URL.Path == "/api/v1/namespaces/team/pods" {
			items = []any{map[string]any{"status": map[string]any{"phase": "Running"}}}
			if r.URL.Query().Get("continue") == "" {
				continuation = "next"
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "List", "metadata": map[string]string{"continue": continuation}, "items": items})
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	result, err := service.Overview(context.Background(), id, "team", map[string]bool{"kubernetes.workload.read": true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts["pods"].Count != 2 || result.Phases["Running"] != 2 || result.Counts["deployments"].State != "unavailable" || result.MetricsState["pods"] != "unavailable" || len(result.Usage["pods"]) != 0 {
		t.Fatalf("分页或不可用指标处理错误: %+v", result)
	}
}
