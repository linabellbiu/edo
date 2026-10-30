package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/model"
)

func TestKubernetesClusterUpdateSeparatesMetadataAndConnectionPermissions(t *testing.T) {
	old := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"gitVersion":"v1.37.1"}`) }))
	defer old.Close()
	var calls atomic.Int32
	candidate := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"gitVersion":"v1.37.1"}`)
	}))
	defer candidate.Close()
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters", map[string]any{"name": "update-boundary", "mode": "kubeconfig", "default_namespace": "existing-ns", "kubeconfig": kubeconfigForHTTPTest(old)}, admin)
	var body struct {
		Cluster struct{ ID string } `json:"cluster"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &body) != nil || body.Cluster.ID == "" {
		t.Fatalf("创建测试集群失败: status=%d", created.Code)
	}
	path := "/api/v1/kubernetes/clusters/" + body.Cluster.ID
	role := createRoleForHTTPTest(t, router, admin, "cluster-metadata-editor", []string{access.PermissionClusterUpdate})
	createUserForHTTPTest(t, router, admin, "cluster-metadata-editor", "", []string{role})
	editor := loginForDepartmentTest(t, router, "cluster-metadata-editor")
	old.Close()
	metadata := map[string]any{"name": "offline-name", "mode": "kubeconfig", "default_namespace": "existing-ns"}
	response := performJSONRequest(t, router, http.MethodPut, path, metadata, editor)
	if response.Code != http.StatusOK {
		t.Fatalf("仅更新权限应可离线修改元数据: status=%d body=%s", response.Code, response.Body.String())
	}
	var before model.KubernetesCluster
	if err := db.First(&before, "uuid = ?", body.Cluster.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, payload := range []map[string]any{
		{"name": "must-not-save", "mode": "kubeconfig", "default_namespace": "changed", "kubeconfig": kubeconfigForHTTPTest(candidate)},
		{"name": "must-not-save", "mode": "kubeconfig", "kubeconfig": ""},
		{"name": "must-not-save", "mode": "in_cluster"},
	} {
		response = performJSONRequest(t, router, http.MethodPut, path, payload, editor)
		if response.Code != http.StatusForbidden {
			t.Errorf("修改连接须单独执行权限: status=%d", response.Code)
		}
		var after model.KubernetesCluster
		if err := db.First(&after, "uuid = ?", body.Cluster.ID).Error; err != nil {
			t.Fatal(err)
		}
		if after.Name != before.Name || after.APIServer != before.APIServer || after.KubeconfigCiphertext != before.KubeconfigCiphertext || !after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Error("权限不足的连接修改写入了数据库")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("无执行权限的请求触发了外部连接")
	}
	metadata["kubeconfig"] = nil
	response = performJSONRequest(t, router, http.MethodPut, path, metadata, editor)
	if response.Code != http.StatusOK {
		t.Fatalf("null配置应保留原密文，不触发执行: %d", response.Code)
	}
	role = createRoleForHTTPTest(t, router, admin, "cluster-connection-editor", []string{access.PermissionClusterUpdate, access.PermissionClusterExecute})
	createUserForHTTPTest(t, router, admin, "cluster-connection-editor", "", []string{role})
	operator := loginForDepartmentTest(t, router, "cluster-connection-editor")
	response = performJSONRequest(t, router, http.MethodPut, path, map[string]any{"name": "verified-name", "mode": "kubeconfig", "default_namespace": "existing-ns", "kubeconfig": kubeconfigForHTTPTest(candidate)}, operator)
	if response.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("有权限替换必须真实测试后保存: status=%d calls=%d", response.Code, calls.Load())
	}
	if strings.Contains(response.Body.String(), "test-token") || strings.Contains(response.Body.String(), "certificate-authority-data") || strings.Contains(response.Body.String(), "kubeconfig_ciphertext") {
		t.Fatal("更新响应暴露了连接凭据")
	}
}

func TestKubernetesClusterUpdateFailedProbeReturnsSafeErrorWithoutWriting(t *testing.T) {
	old := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"gitVersion":"v1.37.1"}`) }))
	defer old.Close()
	var calls atomic.Int32
	candidate := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "synthetic-private-diagnostic", http.StatusForbidden)
	}))
	defer candidate.Close()
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters", map[string]any{"name": "failed-update", "mode": "kubeconfig", "kubeconfig": kubeconfigForHTTPTest(old)}, admin)
	var body struct {
		Cluster struct{ ID string } `json:"cluster"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &body) != nil {
		t.Fatal("创建隔离测试集群失败")
	}
	var before model.KubernetesCluster
	if err := db.First(&before, "uuid = ?", body.Cluster.ID).Error; err != nil {
		t.Fatal(err)
	}
	response := performJSONRequest(t, router, http.MethodPut, "/api/v1/kubernetes/clusters/"+body.Cluster.ID, map[string]any{"name": "must-not-save", "mode": "kubeconfig", "kubeconfig": kubeconfigForHTTPTest(candidate)}, admin)
	if response.Code != http.StatusBadGateway || calls.Load() != 1 || !strings.Contains(response.Body.String(), `"code":"kubernetes_unreachable"`) {
		t.Fatalf("失败连接替换未返回安全错误: status=%d calls=%d", response.Code, calls.Load())
	}
	if strings.Contains(response.Body.String(), candidate.URL) || strings.Contains(response.Body.String(), "synthetic-private-diagnostic") || strings.Contains(response.Body.String(), "test-token") {
		t.Fatal("错误响应泄漏内部诊断或凭据")
	}
	var after model.KubernetesCluster
	if err := db.First(&after, "uuid = ?", body.Cluster.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Name != before.Name || after.APIServer != before.APIServer || after.KubeconfigCiphertext != before.KubeconfigCiphertext || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("连接失败后写入了集群配置")
	}
}
