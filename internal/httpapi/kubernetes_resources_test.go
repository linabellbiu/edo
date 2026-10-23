package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"edo/internal/database"
)

func TestKubernetesResourcePermissionsAndSecretAudit(t *testing.T) {
	var resourceCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/version" {
			_, _ = io.WriteString(w, `{"gitVersion":"v1.35.5"}`)
			return
		}
		resourceCalls.Add(1)
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"private","namespace":"default","uid":"uid-1","resourceVersion":"1"},"type":"Opaque","data":{"token":"cHJpdmF0ZS1wYXNzd29yZA=="}}`)
	}))
	defer server.Close()
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters", map[string]any{"name": "test-cluster", "mode": "kubeconfig", "default_namespace": "default", "kubeconfig": kubeconfigForHTTPTest(server)}, admin)
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body.String())
	}
	var result struct{ Cluster struct{ ID string } }
	_ = json.Unmarshal(created.Body.Bytes(), &result)
	role := createRoleForHTTPTest(t, router, admin, "kube-config-reader", []string{"kubernetes.config.read"})
	createUserForHTTPTest(t, router, admin, "kube-reader", database.DefaultDepartmentID, []string{role})
	reader := loginForDepartmentTest(t, router, "kube-reader")
	base := "/api/v1/kubernetes/clusters/" + result.Cluster.ID + "/resources/secrets/private?namespace=default"
	view := performJSONRequest(t, router, http.MethodGet, base, nil, reader)
	if view.Code != 200 || strings.Contains(view.Body.String(), "cHJpdmF0ZS") || !strings.Contains(view.Body.String(), "token") {
		t.Fatal("普通详情未脱敏", view.Code, view.Body.String())
	}
	before := resourceCalls.Load()
	for _, target := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/kubernetes/clusters/" + result.Cluster.ID + "/resources/secrets/private/secret?namespace=default", nil},
		{http.MethodPatch, base, map[string]any{}},
		{http.MethodDelete, base, map[string]any{"uid": "uid-1", "resource_version": "1"}},
		{http.MethodPost, base, map[string]any{}},
		{http.MethodPost, "/api/v1/kubernetes/clusters/" + result.Cluster.ID + "/resources/secrets/private/validate?namespace=default", map[string]any{}},
		{http.MethodPost, "/api/v1/kubernetes/clusters/" + result.Cluster.ID + "/resources/deployments/web/actions?namespace=default", map[string]any{"action": "restart", "uid": "uid-1", "resource_version": "1"}},
		{http.MethodGet, "/api/v1/kubernetes/clusters/" + result.Cluster.ID + "/resources/nodes", nil},
		{http.MethodPost, "/api/v1/kubernetes/clusters/" + result.Cluster.ID + "/node-joins", map[string]any{"provider": "k3s", "minutes": 15}},
		{http.MethodDelete, "/api/v1/kubernetes/clusters/" + result.Cluster.ID + "/nodes/worker", map[string]any{"uid": "uid-1", "resource_version": "1", "agent_stopped": true, "name": "worker"}},
	} {
		response := performJSONRequest(t, router, target.method, target.path, target.body, reader)
		if response.Code != 403 {
			t.Fatal("只读角色越权", target.path, response.Code, response.Body.String())
		}
	}
	if resourceCalls.Load() != before {
		t.Fatal("拒绝的请求仍然访问了集群")
	}
	validation := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters/"+result.Cluster.ID+"/resources/secrets/private/validate?namespace=default", map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "private", "namespace": "default", "uid": "uid-1", "resourceVersion": "1"}}, admin)
	if validation.Code != 200 || strings.TrimSpace(validation.Body.String()) != `{"valid":true}` {
		t.Fatal("校验返回了资源或未成功", validation.Code, validation.Body.String())
	}
	reveal := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/clusters/"+result.Cluster.ID+"/resources/secrets/private/secret?namespace=default", nil, admin)
	if reveal.Code != 200 || !strings.Contains(reveal.Body.String(), "cHJpdmF0ZS") {
		t.Fatal("已授权查看密钥失败", reveal.Code, reveal.Body.String())
	}
	audits := performJSONRequest(t, router, http.MethodGet, "/api/v1/audit-logs", nil, admin)
	if !strings.Contains(audits.Body.String(), "kubernetes.resource.validate") {
		t.Fatal("资源校验缺少独立审计")
	}
	if !strings.Contains(audits.Body.String(), "kubernetes.secret.reveal") || !strings.Contains(audits.Body.String(), "secrets/default/private") || strings.Contains(audits.Body.String(), "cHJpdmF0ZS") || strings.Contains(audits.Body.String(), "private-password") {
		t.Fatal("密钥审计缺失或包含密钥", audits.Body.String())
	}
}
