package kube

import (
	"context"
	"edo/internal/database"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceDiagnosticsUsesEndpointSlicesWithoutInventingReadiness(t *testing.T) {
	var slices atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("诊断执行了写操作: %s", r.Method)
		}
		switch r.URL.Path {
		case "/api/v1/namespaces/team/services/web":
			io.WriteString(w, `{"apiVersion":"v1","kind":"Service","metadata":{"name":"web","namespace":"team","uid":"s1"}}`)
		case "/apis/discovery.k8s.io/v1/namespaces/team/endpointslices":
			slices.Add(1)
			if r.URL.Query().Get("labelSelector") != "kubernetes.io/service-name=web" || r.URL.Query().Get("limit") != "500" {
				t.Error("端点范围未限制", r.URL)
			}
			if r.URL.Query().Get("continue") == "" {
				io.WriteString(w, `{"apiVersion":"discovery.k8s.io/v1","kind":"EndpointSliceList","metadata":{"continue":"page2"},"items":[{"metadata":{"name":"current","ownerReferences":[{"kind":"Service","uid":"s1"}]},"addressType":"IPv4","ports":[{"name":"http","port":8080,"protocol":"TCP"}],"endpoints":[{"addresses":["10.0.0.2"],"conditions":{"ready":false,"serving":true,"terminating":true},"targetRef":{"kind":"Pod","namespace":"team","name":"web-pod","uid":"p1"}}]},{"metadata":{"name":"stale","ownerReferences":[{"kind":"Service","uid":"old"}]},"endpoints":[{"addresses":["10.0.0.3"]}]}]}`)
			} else {
				io.WriteString(w, `{"apiVersion":"discovery.k8s.io/v1","kind":"EndpointSliceList","items":[{"metadata":{"name":"manual"},"addressType":"FQDN","endpoints":[{"addresses":["example.test"],"targetRef":{"kind":"Pod","namespace":"other","name":"hidden"}}]}]}`)
			}
		default:
			t.Errorf("意外查询: %s", r.URL)
			w.WriteHeader(404)
		}
	})
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, server)
	result, err := s.ResourceDiagnostics(context.Background(), id, "services", "team", "web", "s1")
	if err != nil || len(result.Endpoints) != 2 || slices.Load() != 2 {
		t.Fatalf("真实端点丢失: %+v %v", result, err)
	}
	first, second := result.Endpoints[0], result.Endpoints[1]
	if first.Ready == nil || *first.Ready || first.Serving == nil || !*first.Serving || first.Terminating == nil || !*first.Terminating || second.Ready != nil || second.Target != nil {
		t.Fatal("端点状态被伪造或越过命名空间", result)
	}
	if _, err = s.ResourceDiagnostics(context.Background(), id, "services", "team", "web", "recreated"); !errors.Is(err, ErrResourceConflict) {
		t.Fatal("接受旧对象UID", err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "unrelated", DepartmentID: database.DefaultDepartmentID})
	if _, err = s.ResourceDiagnostics(ctx, id, "services", "team", "web", "s1"); !errors.Is(err, ErrClusterNotFound) {
		t.Fatal("跨项目访问成功", err)
	}
}
func TestDeploymentDiagnosticsUsesControllerUIDAndTemplateInsteadOfHighestRevision(t *testing.T) {
	template := map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "web"}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": "new:2"}}}}
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/deployments/web"):
			json.NewEncoder(w).Encode(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "team", "uid": "d1"}, "spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "web"}}, "template": template}})
		case strings.HasSuffix(r.URL.Path, "/replicasets"):
			if r.URL.Query().Get("labelSelector") != "app=web" {
				t.Error("ReplicaSet 未按选择器过滤")
			}
			io.WriteString(w, `{"apiVersion":"apps/v1","kind":"ReplicaSetList","items":[{"metadata":{"name":"web-new","uid":"rs2","annotations":{"deployment.kubernetes.io/revision":"2"},"ownerReferences":[{"kind":"Deployment","uid":"d1","controller":true}]},"spec":{"replicas":2,"template":{"metadata":{"labels":{"app":"web","pod-template-hash":"hash"}},"spec":{"containers":[{"name":"app","image":"new:2"}]}}},"status":{"replicas":2,"readyReplicas":1}},{"metadata":{"name":"web-old","uid":"rs1","annotations":{"deployment.kubernetes.io/revision":"3"},"ownerReferences":[{"kind":"Deployment","uid":"d1","controller":true}]},"spec":{"replicas":0,"template":{"spec":{"containers":[{"name":"app","image":"old:1"}]}}}},{"metadata":{"name":"unrelated","uid":"rs3","ownerReferences":[{"kind":"Deployment","uid":"other","controller":true}]}}]}`)
		case strings.HasSuffix(r.URL.Path, "/pods"):
			io.WriteString(w, `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"name":"mine","uid":"p1","ownerReferences":[{"kind":"ReplicaSet","uid":"rs2","controller":true}]},"status":{"phase":"Running"}},{"metadata":{"name":"other","uid":"p2","ownerReferences":[{"kind":"ReplicaSet","uid":"rs3","controller":true}]}}]}`)
		default:
			t.Error("意外查询", r.URL)
			w.WriteHeader(404)
		}
	})
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, server)
	result, err := s.ResourceDiagnostics(context.Background(), id, "deployments", "team", "web", "d1")
	if err != nil || len(result.Revisions) != 2 {
		t.Fatalf("版本历史错误 %+v %v", result, err)
	}
	if result.Revisions[0].Revision != "3" || result.Revisions[0].Current || !result.Revisions[1].Current || len(result.Revisions[1].Pods) != 1 || result.Revisions[1].Pods[0].Name != "mine" {
		t.Fatal("按最大版本误判当前模板或错误关联Pod", result.Revisions)
	}
}
func TestPodDiagnosticsFreshnessMetricsVersionFallbackAndRealZero(t *testing.T) {
	var stamp atomic.Value
	stamp.Store(time.Now().UTC().Format(time.RFC3339Nano))
	var forbidden atomic.Bool
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/namespaces/team/pods/web":
			io.WriteString(w, `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web","namespace":"team","uid":"p1","creationTimestamp":"2026-01-01T00:00:00Z"}}`)
		case strings.Contains(r.URL.Path, "/metrics.k8s.io/v1/"):
			w.WriteHeader(404)
			io.WriteString(w, `{"kind":"Status","apiVersion":"v1","reason":"NotFound","code":404}`)
		default:
			if forbidden.Load() {
				w.WriteHeader(403)
				io.WriteString(w, `{"kind":"Status","apiVersion":"v1","reason":"Forbidden","code":403}`)
				return
			}
			fmt.Fprintf(w, `{"apiVersion":"metrics.k8s.io/v1beta1","kind":"PodMetrics","metadata":{"name":"web","namespace":"team"},"timestamp":%q,"containers":[{"name":"app","usage":{"cpu":"0","memory":"32Mi"}},{"name":"invalid","usage":{"cpu":"bad","memory":"32Mi"}}]}`, stamp.Load())
		}
	})
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, server)
	result, err := s.ResourceDiagnostics(context.Background(), id, "pods", "team", "web", "p1")
	if err != nil || result.State != "ok" || len(result.Containers) != 1 || result.Containers[0].CPU == nil || *result.Containers[0].CPU != 0 || *result.Containers[0].Memory != 32*1024*1024 {
		t.Fatalf("指标错误 %+v %v", result, err)
	}
	stamp.Store(time.Now().Add(-3 * time.Minute).Format(time.RFC3339))
	result, err = s.ResourceDiagnostics(context.Background(), id, "pods", "team", "web", "p1")
	if err != nil || result.State != "stale" || len(result.Containers) != 0 {
		t.Fatal("旧指标伪造当前用量", result, err)
	}
	forbidden.Store(true)
	result, err = s.ResourceDiagnostics(context.Background(), id, "pods", "team", "web", "p1")
	if err != nil || result.State != "forbidden" || len(result.Errors) != 1 {
		t.Fatal("指标权限错误未区分", result, err)
	}
	for _, resource := range []string{"secrets", "nodes", "replicasets"} {
		if _, err = s.ResourceDiagnostics(context.Background(), id, resource, "team", "web", "p1"); !errors.Is(err, ErrInvalidResource) {
			t.Fatal("开放通用资源诊断", err)
		}
	}
}
func TestDiagnosticsRejectsObjectRecreatedDuringQuery(t *testing.T) {
	var reads atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/services/web") {
			uid := "s1"
			if reads.Add(1) > 1 {
				uid = "s2"
			}
			fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Service","metadata":{"name":"web","namespace":"team","uid":%q}}`, uid)
		} else {
			io.WriteString(w, `{"apiVersion":"discovery.k8s.io/v1","kind":"EndpointSliceList","items":[]}`)
		}
	})
	s := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, s, server)
	if _, err := s.ResourceDiagnostics(context.Background(), id, "services", "team", "web", "s1"); !errors.Is(err, ErrResourceConflict) {
		t.Fatal("同名新对象展示了旧关联", err)
	}
}
