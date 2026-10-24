package kube

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestResourceCreateValidationUsesStrictDryRunAndCreateIdentity(t *testing.T) {
	var dryRuns, writes atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/namespaces/team/pods" {
			t.Errorf("创建校验使用错误路径: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("dryRun") != "All" {
			writes.Add(1)
			http.Error(w, "必须使用 dry-run", http.StatusInternalServerError)
			return
		}
		dryRuns.Add(1)
		if r.URL.Query().Get("fieldValidation") != "Strict" || r.URL.Query().Get("fieldManager") != "edo" {
			t.Errorf("创建校验未复用严格参数: %s", r.URL)
		}
		var object map[string]any
		if err := json.NewDecoder(r.Body).Decode(&object); err != nil {
			t.Error(err)
			return
		}
		metadata := object["metadata"].(map[string]any)
		if metadata["name"] != "validate" || metadata["namespace"] != "team" || metadata["uid"] != nil || metadata["resourceVersion"] != nil {
			t.Error("创建校验篡改或混入了已有对象身份")
		}
		_ = json.NewEncoder(w).Encode(object)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	object := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "validate", "namespace": "team"}, "spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": "app:1"}}}}
	if err := service.ValidateResourceCreate(context.Background(), id, "pods", "team", "validate", object); err != nil {
		t.Fatal(err)
	}
	if dryRuns.Load() != 1 || writes.Load() != 0 {
		t.Fatalf("校验未保持只读: dry-run=%d writes=%d", dryRuns.Load(), writes.Load())
	}
}

func TestResourceCreateValidationRejectsUnsupportedResourcesAndWriteFields(t *testing.T) {
	var calls atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "不应访问集群资源", http.StatusInternalServerError)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	for _, target := range []struct {
		name, resource, namespace string
		change                    func(map[string]any)
	}{
		{"未知资源", "replicasets", "team", nil},
		{"禁止新建节点", "nodes", "", nil},
		{"禁止新建存储类", "storageclasses", "", nil},
		{"全部命名空间", "pods", "*", nil},
		{"空命名空间", "pods", "", nil},
		{"名称不匹配", "pods", "team", func(object map[string]any) { object["metadata"].(map[string]any)["name"] = "other" }},
		{"命名空间不匹配", "pods", "team", func(object map[string]any) { object["metadata"].(map[string]any)["namespace"] = "other" }},
		{"不能带UID", "pods", "team", func(object map[string]any) { object["metadata"].(map[string]any)["uid"] = "existing" }},
		{"不能带版本", "pods", "team", func(object map[string]any) { object["metadata"].(map[string]any)["resourceVersion"] = "12" }},
		{"不能带状态", "pods", "team", func(object map[string]any) { object["status"] = map[string]any{} }},
		{"不能改种类", "pods", "team", func(object map[string]any) { object["kind"] = "Secret" }},
		{"不能改API版本", "pods", "team", func(object map[string]any) { object["apiVersion"] = "apps/v1" }},
	} {
		t.Run(target.name, func(t *testing.T) {
			object := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "web", "namespace": "team"}, "spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": "app:1"}}}}
			if target.change != nil {
				target.change(object)
			}
			if err := service.ValidateResourceCreate(context.Background(), id, target.resource, target.namespace, "web", object); !errors.Is(err, ErrInvalidResource) {
				t.Fatalf("非法创建校验没有被拒绝: %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("非法校验请求仍访问了集群: %d", calls.Load())
	}
}
