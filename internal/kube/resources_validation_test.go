package kube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestResourceValidationUsesStrictDryRunAndSameWriteGuards(t *testing.T) {
	var calls atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/namespaces/team/configmaps/config" || r.URL.Query().Get("dryRun") != "All" || r.URL.Query().Get("fieldValidation") != "Strict" || r.Header.Get("Content-Type") != "application/merge-patch+json" {
			t.Errorf("校验未使用严格 dry-run: %s %s", r.Method, r.URL)
		}
		var object map[string]any
		if err := json.NewDecoder(r.Body).Decode(&object); err != nil {
			t.Fatal(err)
		}
		metadata := object["metadata"].(map[string]any)
		if metadata["uid"] != "uid-1" || metadata["resourceVersion"] != "12" {
			t.Error("校验未保留资源身份")
		}
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"config","namespace":"team","uid":"uid-1","resourceVersion":"12"},"data":{"a":"new"}}`)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	object := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "config", "namespace": "team", "uid": "uid-1", "resourceVersion": "12"}, "data": map[string]any{"a": "new"}}
	if err := service.ValidateResource(context.Background(), id, "configmaps", "team", "config", object); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("校验请求数量错误", calls.Load())
	}
	for _, invalid := range []map[string]any{
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "config", "namespace": "team"}},
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": object["metadata"], "status": map[string]any{}},
	} {
		if err := service.ValidateResource(context.Background(), id, "configmaps", "team", "config", invalid); !errors.Is(err, ErrInvalidResource) {
			t.Fatal("无效校验请求未拒绝", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("无效请求访问了集群")
	}
}

func TestResourceSaveNeverInheritsDryRun(t *testing.T) {
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("dryRun") != "" || r.URL.Query().Get("fieldValidation") != "Strict" {
			t.Errorf("保存选项错误: %s", r.URL)
		}
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"config","namespace":"team","uid":"uid-1","resourceVersion":"13"}}`)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	_, err := service.SaveResource(context.Background(), id, "configmaps", "team", "config", map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "config", "namespace": "team", "uid": "uid-1", "resourceVersion": "12"}}, false)
	if err != nil {
		t.Fatal(err)
	}
}
