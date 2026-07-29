package httpapi

import (
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKubernetesConnectionTestDoesNotPersistAndCreateRetests(t *testing.T) {
	clusterAPI := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/version" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"gitVersion":"v1.32.4"}`)
	}))
	defer clusterAPI.Close()

	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("登录失败: status=%d body=%s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	payload := map[string]any{
		"name": "production", "mode": "kubeconfig", "default_namespace": "default",
		"kubeconfig": kubeconfigForHTTPTest(clusterAPI),
	}

	tested := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters/test", payload, cookie)
	if tested.Code != http.StatusOK || !strings.Contains(tested.Body.String(), `"version":"v1.32.4"`) ||
		!strings.Contains(tested.Body.String(), `"api_server":"`+clusterAPI.URL+`"`) {
		t.Fatalf("测试 Kubernetes 连接失败: status=%d body=%s", tested.Code, tested.Body.String())
	}
	audits := performJSONRequest(t, router, http.MethodGet, "/api/v1/audit-logs", nil, cookie)
	if audits.Code != http.StatusOK || !strings.Contains(audits.Body.String(), `"action":"kubernetes.cluster.test_input"`) {
		t.Fatalf("Kubernetes 连接测试未写入审计: status=%d body=%s", audits.Code, audits.Body.String())
	}
	listed := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/clusters", nil, cookie)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), `"name":"production"`) {
		t.Fatalf("连接测试不应保存 Kubernetes 集群: status=%d body=%s", listed.Code, listed.Body.String())
	}

	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters", payload, cookie)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"name":"production"`) {
		t.Fatalf("创建 Kubernetes 集群失败: status=%d body=%s", created.Code, created.Body.String())
	}
}

func TestKubernetesConnectionTestRequiresManagePermission(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters/test", map[string]any{
		"name": "production", "mode": "kubeconfig", "default_namespace": "default", "kubeconfig": "invalid",
	}, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("未登录用户可以测试 Kubernetes 连接: status=%d body=%s", response.Code, response.Body.String())
	}
}

func kubeconfigForHTTPTest(server *httptest.Server) string {
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
    certificate-authority-data: %s
users:
- name: zrt
  user:
    token: test-token
contexts:
- name: test
  context:
    cluster: test
    user: zrt
current-context: test
`, server.URL, base64.StdEncoding.EncodeToString(certificate))
}
