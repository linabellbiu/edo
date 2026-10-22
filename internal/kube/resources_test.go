package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func resourceTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/version" {
			_, _ = io.WriteString(w, `{"gitVersion":"v1.35.2"}`)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestResourceListsUseBoundedPaginationAndRedactSecrets(t *testing.T) {
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/default/secrets" || r.URL.Query().Get("limit") != "500" || r.URL.Query().Get("continue") != "next" {
			t.Errorf("资源查询范围不正确: %s", r.URL)
		}
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"SecretList","metadata":{"continue":"more"},"items":[{"apiVersion":"v1","kind":"Secret","metadata":{"name":"private","uid":"secret-1","resourceVersion":"12","namespace":"default","annotations":{"private":"sensitive-value","kubectl.kubernetes.io/last-applied-configuration":"sensitive-value"},"managedFields":[{}]},"data":{"token":"c2Vuc2l0aXZlLXZhbHVl"},"stringData":{"token":"sensitive-value"},"type":"Opaque"}]}`)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	list, err := service.ListResources(context.Background(), id, "secrets", "default", "next")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(list)
	if strings.Contains(string(body), "sensitive-value") || strings.Contains(string(body), "c2Vuc2l0aXZl") || strings.Contains(string(body), "managedFields") {
		t.Fatalf("Secret 列表泄露内容: %s", body)
	}
	if list.Continue != "more" || len(list.Items) != 1 || len(list.Items[0]["keys"].([]any)) != 1 {
		t.Fatal("Secret 分页或键名不正确", list)
	}
}

func TestResourceRevealIsExplicitAndDoesNotChangeOrdinaryView(t *testing.T) {
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"private","namespace":"default","uid":"1","resourceVersion":"2","annotations":{"private":"hidden"}},"data":{"token":"c2VjcmV0"}}`)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	for _, reveal := range []bool{false, true, false} {
		object, err := service.GetResource(context.Background(), id, "secrets", "default", "private", reveal)
		if err != nil {
			t.Fatal(err)
		}
		_, exists := object["data"]
		if exists != reveal {
			t.Fatalf("密钥查看状态错误: reveal=%v object=%v", reveal, object)
		}
		if strings.Contains(fmt.Sprint(object), "hidden") {
			t.Fatal("Secret 注解未脱敏")
		}
	}
}

func TestResourcePatchRequiresIdentityAndKeepsPatchSemantics(t *testing.T) {
	var calls atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "PATCH" || r.Header.Get("Content-Type") != "application/merge-patch+json" || r.URL.Path != "/apis/apps/v1/namespaces/team/deployments/web" {
			t.Errorf("更新方式不正确: %s %s", r.Method, r.URL)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		metadata := body["metadata"].(map[string]any)
		if metadata["uid"] != "uid-1" || metadata["resourceVersion"] != "123" || r.URL.Query().Get("fieldValidation") != "Strict" {
			t.Error("更新缺少并发保护或严格校验", body)
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Conflict","code":409,"message":"internal cluster details"}`)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	ctx := context.Background()
	_, err := service.SaveResource(ctx, id, "deployments", "team", "web", map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "team"}}, false)
	if !errors.Is(err, ErrInvalidResource) || calls.Load() != 0 {
		t.Fatal("无版本更新未拒绝", err, calls.Load())
	}
	_, err = service.ResourceAction(ctx, id, "deployments", "team", "web", ResourceAction{ResourceIdentity: ResourceIdentity{UID: "uid-1", ResourceVersion: "123"}, Action: "scale", Replicas: 2})
	if !errors.Is(err, ErrResourceConflict) || calls.Load() != 1 {
		t.Fatal("并发冲突未正确返回", err, calls.Load())
	}
}

func TestResourceWritesRejectUnsafeScopeAndFields(t *testing.T) {
	var calls atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Error("无效请求不应访问集群", r.URL)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	ctx := context.Background()
	base := func(kind, name, ns string) map[string]any {
		return map[string]any{"kind": kind, "apiVersion": "v1", "metadata": map[string]any{"name": name, "namespace": ns, "uid": "uid-1", "resourceVersion": "1"}}
	}
	for _, tc := range []struct {
		resource, ns, name string
		object             map[string]any
	}{
		{"secrets", "*", "secret", base("Secret", "secret", "*")},
		{"secrets", "default", "other", base("Secret", "secret", "default")},
		{"nodes", "default", "node", base("Node", "node", "default")},
		{"clusterroles", "", "root", base("ClusterRole", "root", "")},
	} {
		if _, err := service.SaveResource(ctx, id, tc.resource, tc.ns, tc.name, tc.object, false); !errors.Is(err, ErrInvalidResource) {
			t.Fatal("越界请求未被拒绝", err)
		}
	}
	node := base("Node", "node", "")
	node["spec"] = map[string]any{"providerID": "changed"}
	if _, err := service.SaveResource(ctx, id, "nodes", "", "node", node, false); !errors.Is(err, ErrInvalidResource) {
		t.Fatal("节点身份允许被修改", err)
	}
	node = base("Node", "node", "")
	node["metadata"].(map[string]any)["ownerReferences"] = []any{}
	if _, err := service.SaveResource(ctx, id, "nodes", "", "node", node, false); !errors.Is(err, ErrInvalidResource) {
		t.Fatal("资源身份字段允许被修改", err)
	}
	for _, name := range []string{"default", "kube-system", "kube-public"} {
		if err := service.DeleteResource(ctx, id, "namespaces", "", name, ResourceIdentity{UID: "uid-1", ResourceVersion: "1"}); !errors.Is(err, ErrResourceProtected) {
			t.Fatal("系统命名空间未保护", name, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("无效参数访问了集群")
	}
}

func TestResourceDeletePassesUIDAndVersionPreconditions(t *testing.T) {
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		pre := body["preconditions"].(map[string]any)
		if r.Method != "DELETE" || pre["uid"] != "original" || pre["resourceVersion"] != "5" || body["gracePeriodSeconds"] != nil || body["propagationPolicy"] != "Background" {
			t.Error("删除缺少安全约束", body)
		}
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Success"}`)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	if err := service.DeleteResource(context.Background(), id, "pods", "default", "web", ResourceIdentity{UID: "original", ResourceVersion: "5"}); err != nil {
		t.Fatal(err)
	}
}

func TestResourcesHonorProjectScopeBeforeConnecting(t *testing.T) {
	var calls atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); t.Error("跨项目访问了集群") })
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	if err := service.db.Model(&model.KubernetesCluster{}).Where("id = ?", id).Update("project_id", "project-a").Error; err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "project-b", DepartmentID: database.DefaultDepartmentID, AllDepartments: true})
	_, err := service.ListResources(ctx, id, "nodes", "", "")
	if !errors.Is(err, ErrClusterNotFound) || calls.Load() != 0 {
		t.Fatal("跨项目集群请求未拒绝", err)
	}
}

func TestSecretErrorsNeverEchoInvalidValues(t *testing.T) {
	err := resourceError("secrets", apierrors.NewBadRequest("invalid token private-password"))
	if !errors.Is(err, ErrInvalidResource) || strings.Contains(err.Error(), "private-password") {
		t.Fatal("密钥诊断信息泄露", err)
	}
	err = resourceError("secrets", apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "private", errors.New("private-password")))
	if !errors.Is(err, ErrResourceForbidden) || strings.Contains(err.Error(), "private-password") {
		t.Fatal("密钥权限信息泄露", err)
	}
}

func TestClusterDisplayNameAcceptsChinese(t *testing.T) {
	if !clusterNamePattern.MatchString("EDO 隔离 K3s 测试") || clusterNamePattern.MatchString("bad/name") {
		t.Fatal("集群名称应支持中文，但不能包含路径分隔符")
	}
}

func TestPodLogsDoNotUseOrdinaryRequestTimeout(t *testing.T) {
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/team/pods/web/log" || r.URL.Query().Get("container") != "web" {
			t.Error("日志请求目标不正确", r.URL)
		}
		_, _ = io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		select {
		case <-time.After(100 * time.Millisecond):
			_, _ = io.WriteString(w, "second\n")
		case <-r.Context().Done():
		}
	})
	service := newKubeTestService(t, 20*time.Millisecond)
	id := createKubeTestCluster(t, service, server)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := service.PodLogs(ctx, id, "team", "web", "web", 20, false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	body, err := io.ReadAll(stream)
	if err != nil || string(body) != "first\nsecond\n" {
		t.Fatal("日志流被普通请求超时截断", string(body), err)
	}
}
