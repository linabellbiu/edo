package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/kube"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func TestNodeHistoryPermissionAndPayloadBoundaries(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	defer client.Close()
	var service *kube.Service
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, _ *gorm.DB) {
		service = deps.Kubernetes
		service.ConfigureNodeMonitoring(client, "test:kube:history", deps.Logger)
	})
	defer cleanup()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_, _ = io.WriteString(w, `{"gitVersion":"v1.35.5"}`)
		case "/api/v1/nodes":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"worker","uid":"n1"},"status":{"allocatable":{"cpu":"4","memory":"8Gi","pods":"110"}}}]}`)
		case "/api/v1/nodes/worker":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Node","metadata":{"name":"worker","uid":"n1"}}`)
		case "/api/v1/pods":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"namespace":"private","name":"private-pod"},"spec":{"nodeName":"worker","containers":[{"name":"app","resources":{"requests":{"cpu":"1"}}}]}}]}`)
		case "/apis/metrics.k8s.io/v1beta1/nodes":
			_, _ = fmt.Fprintf(w, `{"apiVersion":"metrics.k8s.io/v1beta1","kind":"NodeMetricsList","items":[{"metadata":{"name":"worker"},"timestamp":%q,"usage":{"cpu":"0","memory":"64Mi"}}]}`, time.Now().UTC().Format(time.RFC3339Nano))
		default:
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"NotFound","code":404}`)
		}
	}))
	defer server.Close()
	admin := loginForDepartmentTest(t, router, "admin")
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters", map[string]any{"name": "monitor-cluster", "mode": "kubeconfig", "default_namespace": "default", "kubeconfig": kubeconfigForHTTPTest(server)}, admin)
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body.String())
	}
	var cluster struct{ Cluster struct{ ID string } }
	_ = json.Unmarshal(created.Body.Bytes(), &cluster)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.RunNodeMonitoring(ctx) }()
	path := "/api/v1/kubernetes/clusters/" + cluster.Cluster.ID + "/nodes/worker/metrics?uid=n1"
	var history kube.NodeHistory
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response := performJSONRequest(t, router, http.MethodGet, path, nil, admin)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		_ = json.Unmarshal(response.Body.Bytes(), &history)
		if len(history.Points) > 0 {
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("节点监控被缓存")
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(history.Points) != 1 || history.Points[0].CPU == nil || *history.Points[0].CPU != 0 || history.Points[0].Allocation.CPURequests != 1 || *history.Points[0].Pods != 1 {
		t.Fatalf("当前真实指标错误: %+v", history)
	}
	role := createRoleForHTTPTest(t, router, admin, "node-only", []string{"kubernetes.node.read"})
	createUserForHTTPTest(t, router, admin, "node-reader", database.DefaultDepartmentID, []string{role})
	reader := loginForDepartmentTest(t, router, "node-reader")
	response := performJSONRequest(t, router, http.MethodGet, path, nil, reader)
	_ = json.Unmarshal(response.Body.Bytes(), &history)
	if response.Code != 200 || history.Points[0].Pods != nil || history.Points[0].Allocation != nil || history.Points[0].CPU == nil || history.Points[0].PodsState != "no_permission" {
		t.Fatal("节点权限越过 Pod 权限", response.Code, response.Body.String())
	}
	for _, query := range []string{"&window=0", "&window=172800", "&window=overflow"} {
		response = performJSONRequest(t, router, http.MethodGet, path+query, nil, admin)
		if response.Code != 400 {
			t.Fatal("错误窗口未被拒绝", response.Code)
		}
	}
	response = performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/clusters/"+cluster.Cluster.ID+"/nodes/worker/metrics?uid=old-node", nil, admin)
	if response.Code != 409 {
		t.Fatal("节点 UID 不一致未被拒绝", response.Code, response.Body.String())
	}
}
