package kube

import (
	"context"
	"errors"
	"testing"
	"time"

	"edo/internal/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestKubernetesDeploymentTemplateServiceTypesPreserveIdentityAndCleanTransitions(t *testing.T) {
	s, id, f := newTemplateAPIFixture(t)
	r := templateTestRecord(id)
	cfg := model.DefaultKubernetesDeploymentSettings()
	cfg.ServiceType = "LoadBalancer"
	cfg.Ports[0].Enabled, cfg.Ports[0].NodePort = true, 30080
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/namespaces/demo/services/api"
	service := f.objects[path]
	uid := service["metadata"].(map[string]any)["uid"]
	spec := service["spec"].(map[string]any)
	if spec["type"] != "LoadBalancer" {
		t.Fatal("LoadBalancer 类型没有实际执行", spec)
	}
	// 模拟 Kubernetes 分配的地址和用户在服务详情中另行设置的 LB 选项。
	spec["clusterIP"], spec["clusterIPs"], spec["ipFamilies"], spec["ipFamilyPolicy"] = "10.43.0.10", []any{"10.43.0.10"}, []any{"IPv4"}, "SingleStack"
	spec["loadBalancerClass"], spec["allocateLoadBalancerNodePorts"], spec["loadBalancerIP"] = "example.com/private", false, "192.0.2.10"
	spec["loadBalancerSourceRanges"], spec["healthCheckNodePort"], spec["externalTrafficPolicy"] = []any{"192.0.2.0/24"}, int64(30123), "Local"
	cfg.Ports[0].NodePort = 0
	r.ID = "release-lb-repeat"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	spec = f.objects[path]["spec"].(map[string]any)
	if spec["loadBalancerClass"] != "example.com/private" || spec["allocateLoadBalancerNodePorts"] != false || spec["clusterIP"] != "10.43.0.10" || spec["ports"].([]any)[0].(map[string]any)["nodePort"] != float64(30080) {
		t.Fatal("同类型更新破坏已有 LB 设置、地址或节点端口", spec)
	}
	cfg.ServiceType, cfg.NodePort = "NodePort", false
	r.ID = "release-nodeport"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	spec = f.objects[path]["spec"].(map[string]any)
	for _, key := range []string{"loadBalancerClass", "allocateLoadBalancerNodePorts", "loadBalancerIP", "loadBalancerSourceRanges", "healthCheckNodePort"} {
		if _, exists := spec[key]; exists {
			t.Fatal("离开 LB 后仍保留专有字段", key, spec)
		}
	}
	if spec["type"] != "NodePort" || spec["clusterIP"] != "10.43.0.10" || spec["externalTrafficPolicy"] != "Local" {
		t.Fatal("切换 NodePort 丢失地址或有效流量策略", spec)
	}
	cfg.ServiceType, cfg.NodePort = "ClusterIP", true
	cfg.Ports[0].NodePort = -1 // 当前类型不使用的旧草稿。
	r.ID = "release-clusterip"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	spec = f.objects[path]["spec"].(map[string]any)
	if spec["type"] != "ClusterIP" || spec["clusterIP"] != "10.43.0.10" || spec["externalTrafficPolicy"] != nil || spec["ports"].([]any)[0].(map[string]any)["nodePort"] != nil || f.objects[path]["metadata"].(map[string]any)["uid"] != uid {
		t.Fatal("ClusterIP 没有忽略旧开关或清理对外节点端口", spec)
	}
	cfg.ServiceType, cfg.ExternalName = "ExternalName", "database.example.com."
	cfg.Ports = []model.KubernetesDeploymentPort{{Enabled: true, Port: -80, Protocol: "unfinished"}}
	r.ID = "release-external"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	spec = f.objects[path]["spec"].(map[string]any)
	if spec["type"] != "ExternalName" || spec["externalName"] != cfg.ExternalName || f.objects[path]["metadata"].(map[string]any)["uid"] != uid {
		t.Fatal("DNS 别名没有执行或更换了 Service 身份", spec)
	}
	for _, key := range []string{"selector", "ports", "clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy"} {
		if _, exists := spec[key]; exists {
			t.Fatal("ExternalName 错误保留 Pod 或地址语义", key, spec)
		}
	}
	var deployment appsv1.Deployment
	if err := jsonRoundTrip(f.objects["/apis/apps/v1/namespaces/demo/deployments/api"], &deployment); err != nil || len(deployment.Spec.Template.Spec.Containers[0].Ports) != 0 {
		t.Fatal("ExternalName 隐藏端口草稿仍生成容器声明", deployment.Spec, err)
	}
	cfg.ServiceType, cfg.Ports = "ClusterIP", []model.KubernetesDeploymentPort{{Name: "http", Enabled: true, Port: 8080, TargetPort: 8080}}
	r.ID = "release-back-to-cluster"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	spec = f.objects[path]["spec"].(map[string]any)
	if spec["type"] != "ClusterIP" || spec["externalName"] != nil || spec["selector"].(map[string]any)["edo.io/target-id"] != r.TargetID || f.objects[path]["metadata"].(map[string]any)["uid"] != uid {
		t.Fatal("从别名恢复时没有交回官方地址分配或恢复 selector", spec)
	}
}

func TestKubernetesDeploymentTemplateExternalNameWithoutPortsAndUnsafeTransitions(t *testing.T) {
	s, id, f := newTemplateAPIFixture(t)
	r := templateTestRecord(id)
	cfg := model.DefaultKubernetesDeploymentSettings()
	cfg.ServiceType, cfg.ExternalName, cfg.Ports = "ExternalName", "db.example.com", nil
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil || f.objects["/api/v1/namespaces/demo/services/api"] == nil {
		t.Fatal("零端口的 ExternalName 没有创建服务", err)
	}
	before := f.writes
	cfg.ServiceType = "ClusterIP"
	target := model.DeploymentTarget{ID: r.TargetID, ProjectID: r.ProjectID, RuntimeID: id, Namespace: r.Namespace, WorkloadName: r.WorkloadName, ContainerName: r.ContainerName}
	if err := s.CheckDeploymentTemplate(context.Background(), target, r.ApplicationID, cfg); !errors.Is(err, ErrDeploymentTemplateExternalNamePorts) || f.writes != before {
		t.Fatal("未在预核验阻止别名零端口转换", err)
	}
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); !errors.Is(err, ErrDeploymentTemplateExternalNamePorts) || f.writes != before {
		t.Fatal("危险转换修改了 Deployment 或 Service", err)
	}
	service := f.objects["/api/v1/namespaces/demo/services/api"]
	spec := service["spec"].(map[string]any)
	spec["type"], spec["clusterIP"] = string(corev1.ServiceTypeClusterIP), corev1.ClusterIPNone
	delete(spec, "externalName")
	cfg.ServiceType, cfg.Ports = "NodePort", []model.KubernetesDeploymentPort{{Name: "http", Enabled: true, Port: 8080, TargetPort: 8080}}
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); !errors.Is(err, ErrDeploymentTemplateHeadlessType) || f.writes != before {
		t.Fatal("无头服务被直接改为 NodePort 或修改了其他资源", err)
	}
}

func TestKubernetesDeploymentTemplateLoadBalancerKeepsNativeNodePortAllocationSetting(t *testing.T) {
	s, id, f := newTemplateAPIFixture(t)
	r := templateTestRecord(id)
	cfg := model.DefaultKubernetesDeploymentSettings()
	cfg.ServiceType, cfg.Ports[0].Enabled = "LoadBalancer", true
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/namespaces/demo/services/api"
	spec := f.objects[path]["spec"].(map[string]any)
	spec["allocateLoadBalancerNodePorts"] = false
	r.ID = "release-no-nodeport-allocation"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	spec = f.objects[path]["spec"].(map[string]any)
	if spec["allocateLoadBalancerNodePorts"] != false || spec["ports"].([]any)[0].(map[string]any)["nodePort"] != nil {
		t.Fatal("未分配节点端口的 LB 被自行改成自动分配或伪造端口", spec)
	}
	cfg.Ports[0].NodePort = 30090
	r.ID = "release-explicit-nodeport"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	spec = f.objects[path]["spec"].(map[string]any)
	if spec["allocateLoadBalancerNodePorts"] != false || spec["ports"].([]any)[0].(map[string]any)["nodePort"] != float64(30090) {
		t.Fatal("官方允许的显式 NodePort 没有准确提交", spec)
	}
}
