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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
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
	if err := service.db.Model(&model.KubernetesCluster{}).Where("uuid = ?", id).Update("project_id", "project-a").Error; err != nil {
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

func TestResourceEventsNormalizesEmptyLists(t *testing.T) {
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes/worker":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Node","metadata":{"name":"worker","uid":"uid-1"}}`)
		case "/api/v1/events":
			if r.URL.Query().Get("fieldSelector") != "involvedObject.uid=uid-1" {
				t.Error("事件查询缺少 UID 范围")
			}
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"EventList","items":null}`)
		default:
			t.Errorf("意外路径 %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	events, err := service.ResourceEvents(context.Background(), id, "nodes", "", "worker", "uid-1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(events)
	if string(body) != "[]" {
		t.Fatalf("无事件不应返回 null: %s", body)
	}
}

func TestDeploymentReplicaSetEventsRequireExactOwner(t *testing.T) {
	var eventReads atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apis/apps/v1/namespaces/team/deployments/web":
			_, _ = io.WriteString(w, `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","uid":"deployment-a"}}`)
		case "/apis/apps/v1/namespaces/team/replicasets/owned":
			_, _ = io.WriteString(w, `{"apiVersion":"apps/v1","kind":"ReplicaSet","metadata":{"uid":"rs-a","ownerReferences":[{"kind":"Deployment","uid":"deployment-a","controller":true}]}}`)
		case "/apis/apps/v1/namespaces/team/replicasets/other":
			_, _ = io.WriteString(w, `{"apiVersion":"apps/v1","kind":"ReplicaSet","metadata":{"uid":"rs-b","ownerReferences":[{"kind":"Deployment","uid":"deployment-b","controller":true}]}}`)
		case "/api/v1/namespaces/team/events":
			eventReads.Add(1)
			if r.URL.Query().Get("fieldSelector") != "involvedObject.uid=rs-a" {
				t.Error("副本集事件未按 UID 查询", r.URL)
			}
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"EventList","items":[{"reason":"SuccessfulCreate","involvedObject":{"uid":"rs-a"}}]}`)
		default:
			t.Error("意外的集群请求", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	ctx := context.Background()
	for _, target := range [][3]string{{"deployment-a", "other", "rs-b"}, {"deployment-a", "owned", "replaced-rs"}, {"replaced-deployment", "owned", "rs-a"}} {
		if _, err := service.DeploymentReplicaSetEvents(ctx, id, "team", "web", target[0], target[1], target[2]); !errors.Is(err, ErrResourceConflict) {
			t.Fatalf("应拒绝失效或非所属副本集 %v: %v", target, err)
		}
		if _, err := service.DeploymentReplicaSetManifest(ctx, id, "team", "web", target[0], target[1], target[2]); !errors.Is(err, ErrResourceConflict) {
			t.Fatalf("不应读取失效或非所属副本集的 YAML %v: %v", target, err)
		}
	}
	if eventReads.Load() != 0 {
		t.Fatal("失效目标不应读取事件")
	}
	events, err := service.DeploymentReplicaSetEvents(ctx, id, "team", "web", "deployment-a", "owned", "rs-a")
	if err != nil || len(events) != 1 || events[0].Reason != "SuccessfulCreate" || eventReads.Load() != 1 {
		t.Fatalf("未读取目标副本集事件: %v, %v", events, err)
	}
	manifest, err := service.DeploymentReplicaSetManifest(ctx, id, "team", "web", "deployment-a", "owned", "rs-a")
	if err != nil || manifest.UID != "rs-a" || manifest.Kind != "ReplicaSet" || manifest.APIVersion != "apps/v1" || eventReads.Load() != 1 {
		t.Fatalf("未读取准确的副本集 YAML: %+v, %v", manifest, err)
	}
}

func TestResourceDebugUsesEphemeralSubresourceWithDryRunAndIdentity(t *testing.T) {
	const podPath = "/api/v1/namespaces/team/pods/web"
	const pod = `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web","namespace":"team","uid":"pod-a","resourceVersion":"7"},"spec":{"containers":[{"name":"app","image":"app:stable"}],"ephemeralContainers":[{"name":"earlier","image":"busybox:stable"}]},"status":{"phase":"Running","containerStatuses":[{"name":"app","state":{"running":{}}}]}}`
	var writes atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET " + podPath:
			_, _ = io.WriteString(w, pod)
		case "PUT " + podPath + "/ephemeralcontainers":
			dryRun := r.URL.Query().Get("dryRun")
			if call := writes.Add(1); call == 1 && dryRun != "All" || call == 2 && dryRun != "" {
				t.Errorf("非预期的干运行顺序: %v %q", call, dryRun)
			}
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			object, _, err := scheme.Codecs.UniversalDeserializer().Decode(data, nil, nil)
			if err != nil {
				t.Error(err)
				return
			}
			updated := object.(*corev1.Pod)
			containers := updated.Spec.EphemeralContainers
			if string(updated.UID) != "pod-a" || updated.ResourceVersion != "7" || len(containers) != 2 || containers[0].Name != "earlier" {
				t.Error("提交必须保留 UID、版本与已有调试容器")
				return
			}
			debug := containers[1]
			if debug.Name != "inspect" || debug.Image != "busybox:stable" || debug.TargetContainerName != "app" || debug.SecurityContext == nil ||
				debug.SecurityContext.AllowPrivilegeEscalation == nil || *debug.SecurityContext.AllowPrivilegeEscalation ||
				len(debug.VolumeMounts) != 0 || len(debug.Env) != 0 {
				t.Error("调试容器目标或配置错误")
			}
			_, _ = io.WriteString(w, pod)
		default:
			t.Error("意外的集群请求", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	ctx := context.Background()
	input := ResourceAction{ResourceIdentity: ResourceIdentity{UID: "replaced-pod", ResourceVersion: "7"}, Action: "debug", Debug: &PodDebugContainer{Name: "inspect", Image: "busybox:stable", Container: "app"}}
	if _, err := service.ResourceAction(ctx, id, "pods", "team", "web", input); !errors.Is(err, ErrResourceConflict) || writes.Load() != 0 {
		t.Fatalf("旧 Pod 不能添加调试容器: %v", err)
	}
	input.UID = "pod-a"
	if _, err := service.ResourceAction(ctx, id, "pods", "team", "web", input); err != nil || writes.Load() != 2 {
		t.Fatalf("临时容器未通过子资源校验和提交: %v, %v", err, writes.Load())
	}
}

func TestResourceDebugRequiresUpdateWithMappingDisabled(t *testing.T) {
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("无更新权限不应访问 Pod", r.Method, r.URL)
		w.WriteHeader(http.StatusForbidden)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	service.authorizationResolver = func(context.Context, *model.KubernetesCluster, bool) (*clusterAuthorization, error) {
		return &clusterAuthorization{
			CacheKey: "operator", Version: "read-and-execute",
			Permissions: map[string][]string{"": {"kubernetes.workload.read", "kubernetes.workload.execute"}},
		}, nil
	}
	input := ResourceAction{
		ResourceIdentity: ResourceIdentity{UID: "pod-a", ResourceVersion: "7"},
		Action:           "debug", Debug: &PodDebugContainer{Name: "inspect", Image: "busybox:stable", Container: "app"},
	}
	if _, err := service.ResourceAction(context.Background(), id, "pods", "team", "web", input); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("关闭原生映射时仍应校验项目更新权限: %v", err)
	}
}
