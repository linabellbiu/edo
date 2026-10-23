package httpapi

import (
	"edo/internal/database"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResourceDiagnosticsRespectsCategoryPermissionAndIdentity(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			io.WriteString(w, `{"gitVersion":"v1.35.5"}`)
		case "/api/v1/namespaces/team/services/web":
			io.WriteString(w, `{"apiVersion":"v1","kind":"Service","metadata":{"name":"web","namespace":"team","uid":"s1"}}`)
		case "/apis/discovery.k8s.io/v1/namespaces/team/endpointslices":
			io.WriteString(w, `{"apiVersion":"discovery.k8s.io/v1","kind":"EndpointSliceList","items":[]}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"NotFound","code":404}`)
		}
	}))
	defer server.Close()
	admin := loginForDepartmentTest(t, router, "admin")
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters", map[string]any{"name": "diagnostic-cluster", "mode": "kubeconfig", "default_namespace": "team", "kubeconfig": kubeconfigForHTTPTest(server)}, admin)
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body.String())
	}
	var cluster struct{ Cluster struct{ ID string } }
	json.Unmarshal(created.Body.Bytes(), &cluster)
	root := "/api/v1/kubernetes/clusters/" + cluster.Cluster.ID + "/resources/"
	path := root + "services/web/diagnostics?namespace=team&uid=s1"
	network := createRoleForHTTPTest(t, router, admin, "diagnostic-network", []string{"kubernetes.network.read"})
	createUserForHTTPTest(t, router, admin, "diagnostic-reader", database.DefaultDepartmentID, []string{network})
	reader := loginForDepartmentTest(t, router, "diagnostic-reader")
	response := performJSONRequest(t, router, http.MethodGet, path, nil, reader)
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("无法读取已授权诊断", response.Code, response.Body.String())
	}
	for _, resource := range []string{"pods", "deployments"} {
		response = performJSONRequest(t, router, http.MethodGet, root+resource+"/web/diagnostics?namespace=team&uid=p1", nil, reader)
		if response.Code != 403 {
			t.Fatal("网络权限读取了工作负载", response.Code)
		}
	}
	response = performJSONRequest(t, router, http.MethodGet, strings.Replace(path, "uid=s1", "uid=old", 1), nil, reader)
	if response.Code != 409 {
		t.Fatal("同名资源身份校验失效", response.Code)
	}
	response = performJSONRequest(t, router, http.MethodGet, root+"secrets/web/diagnostics?namespace=team&uid=s1", nil, admin)
	if response.Code != 400 {
		t.Fatal("诊断接口开放了Secret", response.Code)
	}
}
