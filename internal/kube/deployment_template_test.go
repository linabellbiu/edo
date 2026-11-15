package kube

import (
	"context"
	"edo/internal/model"
	"encoding/json"
	"errors"
	"fmt"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	"io"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	"k8s.io/client-go/kubernetes/scheme"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type templateAPIFixture struct {
	mu      sync.Mutex
	objects map[string]map[string]any
	writes  int
	deny    string
}

func newTemplateAPIFixture(t *testing.T) (*Service, string, *templateAPIFixture) {
	t.Helper()
	f := &templateAPIFixture{objects: map[string]map[string]any{}}
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := r.URL.Path
		if r.Method == http.MethodGet {
			if value, ok := f.objects[path]; ok {
				_ = json.NewEncoder(w).Encode(value)
			} else {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"apiVersion":"v1","kind":"Status","reason":"NotFound","code":404}`)
			}
			return
		}
		if strings.Contains(path, f.deny) && f.deny != "" {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"Status","reason":"Forbidden","code":403,"message":"private infrastructure detail"}`)
			return
		}
		var object map[string]any
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.Header.Get("Content-Type"), "protobuf") {
			typed, _, err := protobuf.NewSerializer(scheme.Scheme, scheme.Scheme).Decode(body, nil, nil)
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			object = templateObject(typed)
		} else if err := json.Unmarshal(body, &object); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		meta := object["metadata"].(map[string]any)
		if r.Method == http.MethodPost {
			path += "/" + meta["name"].(string)
			if _, exists := f.objects[path]; exists {
				w.WriteHeader(409)
				fmt.Fprint(w, `{"apiVersion":"v1","kind":"Status","reason":"AlreadyExists","code":409}`)
				return
			}
		}
		old := f.objects[path]
		if old != nil {
			oldMeta := old["metadata"].(map[string]any)
			if meta["uid"] != oldMeta["uid"] || meta["resourceVersion"] != oldMeta["resourceVersion"] {
				t.Error("更新未传递准确对象身份", path, meta)
				w.WriteHeader(409)
				return
			}
		}
		if old != nil && r.Method == http.MethodPatch {
			original, _ := json.Marshal(old)
			merged, mergeErr := jsonpatch.MergePatch(original, body)
			if mergeErr != nil {
				t.Fatal(mergeErr)
			}
			if err := json.Unmarshal(merged, &object); err != nil {
				t.Fatal(err)
			}
			meta = object["metadata"].(map[string]any)
		}
		meta["uid"] = "uid-" + meta["name"].(string)
		meta["resourceVersion"] = fmt.Sprint(f.writes + 1)
		meta["generation"] = int64(f.writes + 1)
		if object["kind"] == "Deployment" {
			replicas := object["spec"].(map[string]any)["replicas"]
			object["status"] = map[string]any{"observedGeneration": meta["generation"], "replicas": replicas, "updatedReplicas": replicas, "availableReplicas": replicas}
		}
		if r.URL.Query().Get("dryRun") == "" {
			f.objects[path] = object
			f.writes++
		}
		_ = json.NewEncoder(w).Encode(object)
	})
	s := newKubeTestService(t, 2*time.Second)
	id := createKubeTestCluster(t, s, server)
	return s, id, f
}
func templateTestRecord(id string) model.DeploymentRecord {
	return model.DeploymentRecord{ID: "release-1", TargetID: "target", ProjectID: "project", ApplicationID: "application", RuntimeID: id, Namespace: "demo", WorkloadName: "api", ContainerName: "api", Image: "registry.example.test/api@sha256:" + strings.Repeat("a", 64)}
}

func TestKubernetesDeploymentTemplateCreatesFirstWorkloadAndOwnedRerun(t *testing.T) {
	s, id, f := newTemplateAPIFixture(t)
	r := templateTestRecord(id)
	cfg := model.DefaultKubernetesDeploymentSettings()
	target := model.DeploymentTarget{ID: r.TargetID, ProjectID: r.ProjectID, RuntimeID: id, Namespace: r.Namespace, WorkloadName: r.WorkloadName, ContainerName: r.ContainerName}
	if err := s.CheckDeploymentTemplate(context.Background(), target, r.ApplicationID, cfg); err != nil {
		t.Fatal(err)
	}
	if f.writes != 0 {
		t.Fatal("只读检测修改了集群")
	}
	previous, uid, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second)
	if err != nil || previous != "" || uid == "" {
		t.Fatalf("首次模板部署失败: previous=%s uid=%s err=%v", previous, uid, err)
	}
	var deployment appsv1.Deployment
	if err = jsonRoundTrip(f.objects["/apis/apps/v1/namespaces/demo/deployments/api"], &deployment); err != nil {
		t.Fatal(err)
	}
	c := deployment.Spec.Template.Spec.Containers[0]
	if *deployment.Spec.Replicas != 1 || c.Resources.Requests.Cpu().String() != "200m" || c.Resources.Requests.Memory().String() != "300Mi" || c.Resources.Limits.Cpu().String() != "1" || c.Resources.Limits.Memory().String() != "1Gi" || len(c.Ports) != 0 || deployment.Spec.Template.Spec.HostNetwork {
		t.Fatal("首次部署没有使用真实默认资源和关闭端口", deployment.Spec)
	}
	if f.objects["/api/v1/namespaces/demo/services/api"] != nil {
		t.Fatal("默认关闭的端口创建了 Service")
	}
	cfg.NodePort = true
	cfg.Ports[0].Enabled = true
	cfg.Ports[1].Enabled = true
	cfg.Ports[0].NodePort = 30080
	cfg.Ports[1].NodePort = 30888
	r.ID = "release-2"
	previous, nextUID, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second)
	if err != nil || previous != r.Image || nextUID != uid {
		t.Fatal("同目标再次发布失败", previous, nextUID, err)
	}
	service := f.objects["/api/v1/namespaces/demo/services/api"]["spec"].(map[string]any)
	if service["type"] != "NodePort" || len(service["ports"].([]any)) != 2 {
		t.Fatal("开启的服务端口未执行", service)
	}
	before := f.writes
	r.ApplicationID = "other"
	if _, _, err = s.DeployTemplate(context.Background(), r, cfg, nil, time.Second); !errors.Is(err, ErrDeploymentTemplateConflict) || f.writes != before {
		t.Fatal("同名资源被其他应用接管", err)
	}
}
func jsonRoundTrip(value, target any) error {
	body, _ := json.Marshal(value)
	return json.Unmarshal(body, target)
}

func TestKubernetesDeploymentTemplateRejectsServiceConflictAndDeniedCreateBeforeWrites(t *testing.T) {
	for _, test := range []string{"同名服务", "无创建权限", "重复端口"} {
		t.Run(test, func(t *testing.T) {
			s, id, f := newTemplateAPIFixture(t)
			r := templateTestRecord(id)
			cfg := model.DefaultKubernetesDeploymentSettings()
			if test == "同名服务" {
				f.objects["/api/v1/namespaces/demo/services/api"] = map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "api", "namespace": "demo", "uid": "foreign", "resourceVersion": "1"}}
			}
			if test == "无创建权限" {
				policies := newIdentityTestPolicies()
				policies.grant("alice", map[string][]string{"demo": {"kubernetes.workload.read", "kubernetes.network.read"}, "": {"kubernetes.config.read", "kubernetes.config.create"}})
				s.authorizationResolver = policies.resolve
				ctx := identityTestContext(t, s, id, "alice")
				_, _, err := s.DeployTemplate(ctx, r, cfg, nil, time.Second)
				if !errors.Is(err, ErrRBACForbidden) || f.writes != 0 {
					t.Fatal("无权限仍创建资源", err, f.writes)
				}
				return
			}
			if test == "重复端口" {
				cfg.Ports[0].Enabled = true
				cfg.Ports[1].Enabled = true
				cfg.Ports[1].Port = cfg.Ports[0].Port
			}
			_, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, time.Second)
			if err == nil || f.writes != 0 {
				t.Fatal("检测失败后仍修改资源", err, f.writes)
			}
		})
	}
}

func TestKubernetesDeploymentTemplateNativeRejectionDoesNotReportSuccess(t *testing.T) {
	s, id, f := newTemplateAPIFixture(t)
	r := templateTestRecord(id)
	f.deny = "/deployments"
	f.objects["/api/v1/namespaces/demo"] = map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "demo", "uid": "namespace", "resourceVersion": "1"}}
	_, _, err := s.DeployTemplate(context.Background(), r, model.DefaultKubernetesDeploymentSettings(), nil, time.Second)
	if !errors.Is(err, ErrResourceForbidden) || f.writes != 0 || strings.Contains(err.Error(), "private") {
		t.Fatal("服务端拒绝未安全阻断", err, f.writes)
	}
}

func TestKubernetesDeploymentTemplateRegistryAndDisablePorts(t *testing.T) {
	s, id, f := newTemplateAPIFixture(t)
	r := templateTestRecord(id)
	cfg := model.DefaultKubernetesDeploymentSettings()
	cfg.NodePort = true
	cfg.Ports[0].Enabled = true
	cfg.Ports[0].NodePort = 30080
	credentials := RegistryPullConfig("registry.example.test", "fixture-user", "never-log-this")
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, credentials, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	secret := f.objects["/api/v1/namespaces/demo/secrets/api-registry"]
	if secret == nil || secret["type"] != "kubernetes.io/dockerconfigjson" {
		t.Fatal("没有创建本目标的拉取凭据")
	}
	spec := f.objects["/apis/apps/v1/namespaces/demo/deployments/api"]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if refs, ok := spec["imagePullSecrets"].([]any); !ok || len(refs) != 1 || refs[0].(map[string]any)["name"] != "api-registry" {
		t.Fatal("Deployment 未引用准确拉取密钥")
	}
	cfg.Ports[0].Enabled = false
	cfg.NodePort = false
	r.ID = "release-closed"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	service := f.objects["/api/v1/namespaces/demo/services/api"]["spec"].(map[string]any)
	selector := service["selector"].(map[string]any)
	if service["type"] != "ClusterIP" || len(selector) != 1 || selector["edo.io/disabled"] != r.ID {
		t.Fatal("关闭全部端口后仍匹配当前 Pod", service)
	}
	for _, p := range service["ports"].([]any) {
		if node, ok := p.(map[string]any)["nodePort"]; ok && node != float64(0) {
			t.Fatal("关闭后仍使用节点端口", service)
		}
	}
}
