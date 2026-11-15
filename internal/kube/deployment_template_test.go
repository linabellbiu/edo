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
	corev1 "k8s.io/api/core/v1"
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
			if strings.HasSuffix(path, "/horizontalpodautoscalers") {
				items := []any{}
				for key, value := range f.objects {
					if strings.HasPrefix(key, path+"/") {
						items = append(items, value)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscalerList", "metadata": map[string]any{}, "items": items})
				return
			}
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
		if r.Method == http.MethodDelete {
			old := f.objects[path]
			if old == nil {
				w.WriteHeader(404)
				return
			}
			meta := old["metadata"].(map[string]any)
			preconditions, _ := object["preconditions"].(map[string]any)
			if preconditions["uid"] != meta["uid"] || preconditions["resourceVersion"] != meta["resourceVersion"] {
				t.Error("删除未携带准确身份")
				w.WriteHeader(409)
				return
			}
			delete(f.objects, path)
			f.writes++
			_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Success"})
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
	if len(c.Command) != 0 || len(c.Args) != 0 || c.WorkingDir != "" || c.ImagePullPolicy != "" || c.ReadinessProbe != nil || c.LivenessProbe != nil || c.StartupProbe != nil || deployment.Spec.Template.Spec.TerminationGracePeriodSeconds != nil || deployment.Spec.Strategy.Type != "" {
		t.Fatal("未配置的运行字段覆盖了 Kubernetes 默认值")
	}
	if f.objects["/api/v1/namespaces/demo/services/api"] != nil {
		t.Fatal("默认关闭的端口创建了 Service")
	}
	cfg.NodePort = true
	cfg.Ports[0].Enabled = true
	cfg.Ports = append(cfg.Ports, model.KubernetesDeploymentPort{Name: "tcp", Enabled: true, Port: 8888, TargetPort: 8888})
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
				cfg.Ports = append(cfg.Ports, model.KubernetesDeploymentPort{Name: "api", Enabled: true, Port: cfg.Ports[0].Port, TargetPort: 8081})
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
	cfg.Ports[0].Enabled = true
	r.ID = "release-reopened"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	selector = f.objects["/api/v1/namespaces/demo/services/api"]["spec"].(map[string]any)["selector"].(map[string]any)
	if _, disabled := selector["edo.io/disabled"]; disabled || selector["edo.io/target-id"] != r.TargetID {
		t.Fatal("重新开启端口后仍保留停用选择器", selector)
	}

}

func TestKubernetesDeploymentTemplateDynamicProtocolsMappingsAndPortRemoval(t *testing.T) {
	s, id, f := newTemplateAPIFixture(t)
	r := templateTestRecord(id)
	cfg := model.DefaultKubernetesDeploymentSettings()
	cfg.NodePort = true
	cfg.Ports = []model.KubernetesDeploymentPort{
		{Name: "dns-udp", Enabled: true, Port: 53, TargetPort: 5353, Protocol: "UDP", NodePort: 30053},
		{Name: "dns-tcp", Enabled: true, Port: 53, TargetPort: 5353, NodePort: 30054},
		{Name: "sctp-api", Enabled: true, Port: 5000, TargetPort: 5001, Protocol: "SCTP", NodePort: 30500},
		{Name: "http", Enabled: true, Port: 8080, TargetPort: 8080, Protocol: "UDP", NodePort: 30808},
		{Name: "metrics", Enabled: true, Port: 9153, TargetPort: 5353, Protocol: "TCP", NodePort: 30915},
	}
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	var workload appsv1.Deployment
	var service corev1.Service
	if err := jsonRoundTrip(f.objects["/apis/apps/v1/namespaces/demo/deployments/api"], &workload); err != nil {
		t.Fatal(err)
	}
	if err := jsonRoundTrip(f.objects["/api/v1/namespaces/demo/services/api"], &service); err != nil {
		t.Fatal(err)
	}
	container := workload.Spec.Template.Spec.Containers[0]
	if len(container.Ports) != 4 || len(service.Spec.Ports) != 5 {
		t.Fatal("共享目标端口未去重或服务映射丢失", container.Ports, service.Spec.Ports)
	}
	for index, port := range service.Spec.Ports {
		input := cfg.Ports[index]
		if port.Name != input.Name || string(port.Protocol) != input.EffectiveProtocol() || port.Port != input.Port || port.TargetPort.IntVal != input.TargetPort || port.NodePort != input.NodePort || port.AppProtocol != nil {
			t.Fatal("端口映射或协议未准确执行，或 UDP 被误标为 HTTP", port, input)
		}
	}
	if container.Ports[0].Protocol != corev1.ProtocolUDP || container.Ports[1].Protocol != corev1.ProtocolTCP || container.Ports[2].Protocol != corev1.ProtocolSCTP {
		t.Fatal("容器端口没有保留真实传输协议", container.Ports)
	}
	for index := range cfg.Ports {
		cfg.Ports[index].NodePort = 0
	}
	cfg.Ports[0].Protocol, cfg.Ports[0].Port = "TCP", 54
	r.ID = "release-protocol-changed"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	service = corev1.Service{}
	if err := jsonRoundTrip(f.objects["/api/v1/namespaces/demo/services/api"], &service); err != nil {
		t.Fatal(err)
	}
	if service.Spec.Ports[0].NodePort != 0 || service.Spec.Ports[1].NodePort != 30054 {
		t.Fatal("修改协议后错误继承旧节点端口，或未保留不变端口", service.Spec.Ports)
	}
	cfg.Ports, cfg.NodePort = []model.KubernetesDeploymentPort{}, false
	r.ID = "release-ports-removed"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	workload = appsv1.Deployment{}
	if err := jsonRoundTrip(f.objects["/apis/apps/v1/namespaces/demo/deployments/api"], &workload); err != nil {
		t.Fatal(err)
	}
	selector := f.objects["/api/v1/namespaces/demo/services/api"]["spec"].(map[string]any)["selector"].(map[string]any)
	if len(workload.Spec.Template.Spec.Containers[0].Ports) != 0 || len(selector) != 1 || selector["edo.io/disabled"] != r.ID {
		t.Fatal("删除全部端口后仍有容器声明或服务后端", workload.Spec, selector)
	}
	cfg.Ports = []model.KubernetesDeploymentPort{{Name: "http", Enabled: true, Port: 8080, TargetPort: 8080}}
	r.ID = "release-http-added"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	service = corev1.Service{}
	if err := jsonRoundTrip(f.objects["/api/v1/namespaces/demo/services/api"], &service); err != nil || len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Name != "http" || service.Spec.Ports[0].Protocol != corev1.ProtocolTCP || service.Spec.Ports[0].AppProtocol != nil || service.Spec.Selector["edo.io/disabled"] != "" {
		t.Fatal("重新添加示例端口未保留用户名称、所选协议与服务选择器", service.Spec, err)
	}
}

func TestKubernetesDeploymentTemplateExamplePortNameIsUserEditable(t *testing.T) {
	s, id, f := newTemplateAPIFixture(t)
	r := templateTestRecord(id)
	cfg := model.DefaultKubernetesDeploymentSettings()
	cfg.Ports[0].Enabled = true
	var originalUID string
	for _, name := range []string{"http", "web-api", "metrics"} {
		cfg.Ports[0].Name = name
		r.ID = "release-port-" + name
		_, uid, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if originalUID == "" {
			originalUID = uid
		} else if uid != originalUID {
			t.Fatal("修改端口名创建了另一工作负载")
		}
		var service corev1.Service
		if err = jsonRoundTrip(f.objects["/api/v1/namespaces/demo/services/api"], &service); err != nil {
			t.Fatal(err)
		}
		if service.Name != "api" || len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Name != name || service.Spec.Ports[0].Protocol != corev1.ProtocolTCP || service.Spec.Ports[0].AppProtocol != nil {
			t.Fatal("端口名称被固定或名称影响了协议", service.Spec.Ports)
		}
		var workload appsv1.Deployment
		if err = jsonRoundTrip(f.objects["/apis/apps/v1/namespaces/demo/deployments/api"], &workload); err != nil {
			t.Fatal(err)
		}
		ports := workload.Spec.Template.Spec.Containers[0].Ports
		if len(ports) != 1 || ports[0].Name != name || ports[0].Protocol != corev1.ProtocolTCP {
			t.Fatal("容器声明未使用用户输入的端口名称", ports)
		}
		if f.objects["/api/v1/namespaces/demo/services/http"] != nil {
			t.Fatal("把示例端口名当作固定服务创建")
		}
	}
}

func TestKubernetesDeploymentTemplateAdvancedRuntimeReachesAPI(t *testing.T) {
	s, id, f := newTemplateAPIFixture(t)
	r := templateTestRecord(id)
	cfg := model.DefaultKubernetesDeploymentSettings()
	grace := int64(45)
	cfg.Command = []string{"/app/server"}
	cfg.Args = []string{"--mode=web", "literal $(date)"}
	cfg.WorkingDir = "/app"
	cfg.ImagePullPolicy = "Always"
	cfg.TerminationGracePeriodSeconds = &grace
	cfg.ReadinessProbe = &model.KubernetesDeploymentProbe{Kind: "http", Port: 8080, Path: "/ready", Scheme: "HTTP", InitialDelaySeconds: 2, PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 3}
	cfg.LivenessProbe = &model.KubernetesDeploymentProbe{Kind: "tcp", Port: 8888, PeriodSeconds: 10}
	cfg.StartupProbe = &model.KubernetesDeploymentProbe{Kind: "http", Port: 8080, Path: "/started", FailureThreshold: 30}
	cfg.Strategy = "RollingUpdate"
	cfg.MaxSurge = "1"
	cfg.MaxUnavailable = "0"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	if err := jsonRoundTrip(f.objects["/apis/apps/v1/namespaces/demo/deployments/api"], &deployment); err != nil {
		t.Fatal(err)
	}
	c := deployment.Spec.Template.Spec.Containers[0]
	if len(c.Command) != 1 || c.Command[0] != "/app/server" || len(c.Args) != 2 || c.Args[1] != "literal $(date)" || c.WorkingDir != "/app" || c.ImagePullPolicy != "Always" {
		t.Fatal("启动参数被解析或遗漏", c)
	}
	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet.Path != "/ready" || c.ReadinessProbe.HTTPGet.Port.IntVal != 8080 || c.ReadinessProbe.PeriodSeconds != 5 || c.LivenessProbe == nil || c.LivenessProbe.TCPSocket.Port.IntVal != 8888 || c.StartupProbe == nil || c.StartupProbe.FailureThreshold != 30 {
		t.Fatal("探针未按配置写入", c)
	}
	if deployment.Spec.Template.Spec.TerminationGracePeriodSeconds == nil || *deployment.Spec.Template.Spec.TerminationGracePeriodSeconds != 45 || deployment.Spec.Strategy.Type != "RollingUpdate" || deployment.Spec.Strategy.RollingUpdate == nil || deployment.Spec.Strategy.RollingUpdate.MaxSurge.IntVal != 1 || deployment.Spec.Strategy.RollingUpdate.MaxUnavailable.IntVal != 0 {
		t.Fatal("滚动发布及宽限配置未写入", deployment.Spec)
	}
	cfg.Strategy = "Recreate"
	cfg.MaxSurge = ""
	cfg.MaxUnavailable = ""
	cfg.ReadinessProbe = nil
	cfg.LivenessProbe = nil
	cfg.StartupProbe = nil
	r.ID = "release-recreate"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	deployment = appsv1.Deployment{}
	if err := jsonRoundTrip(f.objects["/apis/apps/v1/namespaces/demo/deployments/api"], &deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Strategy.Type != "Recreate" || deployment.Spec.Strategy.RollingUpdate != nil {
		t.Fatal("切换重建保留了旧滚动字段", deployment.Spec.Strategy)
	}
	if deployment.Spec.Template.Spec.Containers[0].ReadinessProbe != nil {
		t.Fatal("关闭探针后仍保留旧检测")
	}
	cfg.Strategy = ""
	cfg.TerminationGracePeriodSeconds = nil
	r.ID = "release-defaults"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	current := f.objects["/apis/apps/v1/namespaces/demo/deployments/api"]["spec"].(map[string]any)
	if strategy, set := current["strategy"]; set && strategy != nil && len(strategy.(map[string]any)) != 0 {
		t.Fatal("取消覆盖后没有清除旧 Recreate 策略", strategy)
	}
	pod := current["template"].(map[string]any)["spec"].(map[string]any)
	if _, set := pod["terminationGracePeriodSeconds"]; set {
		t.Fatal("取消覆盖后没有清除旧终止宽限期", pod["terminationGracePeriodSeconds"])
	}
}
