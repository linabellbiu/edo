package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

func TestKubernetesCreateValidationHTTPPermissionsScopeAndNoWrites(t *testing.T) {
	var resourceCalls, dryRuns, writes atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/version" {
			_, _ = io.WriteString(w, `{"gitVersion":"v1.35.5"}`)
			return
		}
		resourceCalls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/namespaces/default/pods" {
			t.Errorf("校验未使用 Kubernetes 创建集合路径: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("dryRun") == "All" {
			dryRuns.Add(1)
		} else {
			writes.Add(1)
		}
		if r.URL.Query().Get("fieldValidation") != "Strict" || r.URL.Query().Get("fieldManager") != "edo" {
			t.Error("创建校验丢失严格字段检查")
		}
		var object map[string]any
		if err := json.NewDecoder(r.Body).Decode(&object); err != nil {
			t.Error(err)
			return
		}
		object["metadata"].(map[string]any)["uid"] = "cluster-generated-uid"
		_ = json.NewEncoder(w).Encode(object)
	}))
	t.Cleanup(server.Close)
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters", map[string]any{"name": "create-validation", "mode": "kubeconfig", "default_namespace": "default", "kubeconfig": kubeconfigForHTTPTest(server)}, admin)
	var cluster struct{ Cluster struct{ ID string } }
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &cluster) != nil || cluster.Cluster.ID == "" {
		t.Fatalf("创建测试集群失败: %d %s", created.Code, created.Body.String())
	}
	path := "/api/v1/kubernetes/clusters/" + cluster.Cluster.ID + "/resources/pods/validate/validate-create?namespace=default"
	body := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "validate", "namespace": "default"}, "spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": "alpine:3.22"}}}}
	for _, permission := range []string{"kubernetes.workload.read", "kubernetes.workload.update"} {
		role := createRoleForHTTPTest(t, router, admin, "create-validation-"+strings.TrimPrefix(permission, "kubernetes.workload."), []string{permission})
		username := "validation-" + strings.TrimPrefix(permission, "kubernetes.workload.")
		createUserForHTTPTest(t, router, admin, username, database.DefaultDepartmentID, []string{role})
		user := loginForDepartmentTest(t, router, username)
		response := performJSONRequest(t, router, http.MethodPost, path, body, user)
		if response.Code != http.StatusForbidden {
			t.Fatalf("非创建权限可以校验新对象: %s %d %s", permission, response.Code, response.Body.String())
		}
	}
	if resourceCalls.Load() != 0 {
		t.Fatal("权限拒绝后仍访问了 Kubernetes 资源")
	}
	role := createRoleForHTTPTest(t, router, admin, "create-validation-creator", []string{"kubernetes.workload.create"})
	createUserForHTTPTest(t, router, admin, "validation-creator", database.DefaultDepartmentID, []string{role})
	creator := loginForDepartmentTest(t, router, "validation-creator")
	valid := performJSONRequest(t, router, http.MethodPost, path+"&dryRun=false&fieldValidation=Ignore", body, creator)
	if valid.Code != http.StatusOK || strings.TrimSpace(valid.Body.String()) != `{"valid":true}` || valid.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("创建校验响应不正确或回显了资源: %d %s", valid.Code, valid.Body.String())
	}
	if dryRuns.Load() != 1 || writes.Load() != 0 {
		t.Fatalf("校验产生了真实写入: dry-runs=%d writes=%d", dryRuns.Load(), writes.Load())
	}
	editValidation := performJSONRequest(t, router, http.MethodPost, strings.Replace(path, "/validate-create", "/validate", 1), body, creator)
	if editValidation.Code != http.StatusForbidden {
		t.Fatalf("仅创建权限可以调用已有编辑校验: %d %s", editValidation.Code, editValidation.Body.String())
	}
	before := resourceCalls.Load()
	for _, request := range []struct {
		path string
		body any
	}{
		{strings.Replace(path, "/pods/", "/replicasets/", 1), body},
		{strings.Replace(path, "/pods/", "/nodes/", 1), body},
		{strings.Replace(path, "namespace=default", "namespace=other", 1), body},
		{strings.Replace(path, "namespace=default", "namespace=*", 1), body},
		{path, map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "validate", "namespace": "default", "uid": "old-uid"}}},
		{path, map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": body["metadata"], "status": map[string]any{}}},
	} {
		response := performJSONRequest(t, router, http.MethodPost, request.path, request.body, admin)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("非法资源或创建身份未拒绝: %s %d %s", request.path, response.Code, response.Body.String())
		}
	}
	otherProject := model.Project{ID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, Name: "创建校验其他项目", IsActive: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := db.Create(&otherProject).Error; err != nil {
		t.Fatal(err)
	}
	testProjectScopes.Store(admin.Value, otherProject.ID)
	foreign := performJSONRequest(t, router, http.MethodPost, path, body, admin)
	testProjectScopes.Store(admin.Value, database.DefaultProjectID(database.DefaultDepartmentID))
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("跨项目集群没有隐藏: %d %s", foreign.Code, foreign.Body.String())
	}
	missing := performJSONRequest(t, router, http.MethodPost, strings.Replace(path, cluster.Cluster.ID, uuid.NewString(), 1), body, admin)
	var missingFailure, foreignFailure errorResponse
	if err := json.Unmarshal(foreign.Body.Bytes(), &foreignFailure); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(missing.Body.Bytes(), &missingFailure); err != nil {
		t.Fatal(err)
	}
	if missing.Code != http.StatusNotFound || missingFailure.Code != "kubernetes_resource_not_found" || foreignFailure.Code != missingFailure.Code || foreignFailure.Message != missingFailure.Message {
		t.Fatalf("不存在集群未返回安全404: %d %s", missing.Code, missing.Body.String())
	}
	if resourceCalls.Load() != before {
		t.Fatal("无效、跨项目或不存在集群的校验仍访问了 Kubernetes 资源")
	}
	// 名字 validate 是合法资源名称，不能被新校验路由抢占原创建入口。
	actual := performJSONRequest(t, router, http.MethodPost, strings.Replace(path, "/validate-create", "", 1), body, creator)
	if actual.Code != http.StatusCreated || writes.Load() != 1 || dryRuns.Load() != 1 {
		t.Fatalf("新路由破坏了原创建或遗留dry-run: %d %s writes=%d dry-runs=%d", actual.Code, actual.Body.String(), writes.Load(), dryRuns.Load())
	}
}

func TestKubernetesCreateValidationHTTPSecretFailureIsSafe(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/version" {
			_, _ = io.WriteString(w, `{"gitVersion":"v1.35.5"}`)
			return
		}
		calls.Add(1)
		if r.URL.Query().Get("dryRun") != "All" {
			t.Error("Secret 校验没有使用 dry-run")
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","code":422,"reason":"Invalid","message":"synthetic-validation-secret-value"}`)
	}))
	t.Cleanup(server.Close)
	var logs bytes.Buffer
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, _ *gorm.DB) { deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil)) })
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters", map[string]any{"name": "secret-validation", "mode": "kubeconfig", "default_namespace": "default", "kubeconfig": kubeconfigForHTTPTest(server)}, admin)
	var cluster struct{ Cluster struct{ ID string } }
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &cluster) != nil {
		t.Fatalf("创建测试集群失败: %d", created.Code)
	}
	path := "/api/v1/kubernetes/clusters/" + cluster.Cluster.ID + "/resources/secrets/private/validate-create?namespace=default"
	body := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "private", "namespace": "default"}, "stringData": map[string]any{"token": "synthetic-validation-secret-value"}}
	logs.Reset()
	response := performJSONRequest(t, router, http.MethodPost, path, body, admin)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_kubernetes_resource"`) || !strings.Contains(logs.String(), "kubernetes_resource_create_validate") {
		t.Fatalf("校验失败缺少安全错误或日志: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "synthetic-validation-secret-value") || strings.Contains(logs.String(), "synthetic-validation-secret-value") {
		t.Fatal("Secret 校验向响应或日志泄露了提交内容")
	}
	if calls.Load() != 1 {
		t.Fatalf("校验失败被重试或未发送: %d", calls.Load())
	}
	audits := performJSONRequest(t, router, http.MethodGet, "/api/v1/audit-logs?limit=200", nil, admin)
	if !strings.Contains(audits.Body.String(), "kubernetes.resource.create.validate") || strings.Contains(audits.Body.String(), "synthetic-validation-secret-value") {
		t.Fatal("创建校验审计缺失或包含Secret内容")
	}
}
