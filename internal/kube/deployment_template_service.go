package kube

import (
	"errors"

	"edo/internal/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var (
	ErrDeploymentTemplateHeadlessType      = errors.New("现有 Service 是无头服务，不能直接改为 NodePort 或 LoadBalancer，请先在服务与网络页面核对")
	ErrDeploymentTemplateExternalNamePorts = errors.New("从 ExternalName 切换为其他服务类型时，请至少启用一个服务端口")
)

// checkDeploymentTemplateServiceType 在任何资源写入之前核对不可直接转换的现场。
func checkDeploymentTemplateServiceType(current map[string]any, cfg *model.KubernetesDeploymentSettings) error {
	if current == nil {
		return nil
	}
	var live corev1.Service
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(current, &live); err != nil {
		return ErrDeploymentTemplateCheck
	}
	typ := corev1.ServiceType(cfg.EffectiveServiceType())
	enabled := false
	for _, port := range cfg.Ports {
		enabled = enabled || port.Enabled
	}
	if enabled && (typ == corev1.ServiceTypeNodePort || typ == corev1.ServiceTypeLoadBalancer) && live.Spec.ClusterIP == corev1.ClusterIPNone {
		return ErrDeploymentTemplateHeadlessType
	}
	if live.Spec.Type == corev1.ServiceTypeExternalName && typ != corev1.ServiceTypeExternalName {
		for _, port := range cfg.Ports {
			if port.Enabled {
				return nil
			}
		}
		return ErrDeploymentTemplateExternalNamePorts
	}
	return nil
}

// deploymentTemplateService 仅更新本模板声明的服务语义；地址与对象身份由 Kubernetes 管理。
func deploymentTemplateService(current map[string]any, cfg *model.KubernetesDeploymentSettings, recordID string, metadata metav1.ObjectMeta, labels map[string]string, ports []corev1.ServicePort) map[string]any {
	typ := corev1.ServiceType(cfg.EffectiveServiceType())
	var live corev1.Service
	if current != nil {
		_ = runtime.DefaultUnstructuredConverter.FromUnstructured(current, &live)
	}
	var service map[string]any
	if typ == corev1.ServiceTypeExternalName {
		svc := &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metadata, Spec: corev1.ServiceSpec{Type: typ, ExternalName: cfg.ExternalName}}
		service = templateObject(svc)
	} else if len(ports) > 0 {
		svc := &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metadata, Spec: corev1.ServiceSpec{Type: typ, Selector: labels, Ports: ports}}
		if typ == corev1.ServiceTypeNodePort || typ == corev1.ServiceTypeLoadBalancer {
			for index := range svc.Spec.Ports {
				if svc.Spec.Ports[index].NodePort != 0 {
					continue
				}
				for _, previous := range live.Spec.Ports {
					protocol := previous.Protocol
					if protocol == "" {
						protocol = corev1.ProtocolTCP
					}
					if previous.Name == svc.Spec.Ports[index].Name && protocol == svc.Spec.Ports[index].Protocol {
						svc.Spec.Ports[index].NodePort = previous.NodePort
					}
				}
			}
		}
		service = templateObject(svc)
	} else if current != nil {
		// 保留已有 Service 的身份与地址，关闭选择器及对外端口；不虚构新端口或删除服务。
		for index := range live.Spec.Ports {
			live.Spec.Ports[index].NodePort = 0
		}
		svc := &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metadata, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{"edo.io/disabled": recordID}, Ports: live.Spec.Ports}}
		service = templateObject(svc)
		typ = corev1.ServiceTypeClusterIP
	}
	if service == nil {
		return nil
	}
	spec := service["spec"].(map[string]any)
	if typ == corev1.ServiceTypeExternalName {
		// ExternalName 是 DNS 别名，不存在 Pod 选择器、虚拟 IP 或端口代理语义。
		for _, key := range []string{"selector", "ports", "clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy", "externalIPs", "sessionAffinity", "sessionAffinityConfig", "externalTrafficPolicy", "internalTrafficPolicy", "trafficDistribution", "publishNotReadyAddresses"} {
			spec[key] = nil
		}
	} else {
		spec["externalName"] = nil
		// MergePatch 需要显式移除旧的停用选择器及其他历史匹配键。
		selector := spec["selector"].(map[string]any)
		for key := range live.Spec.Selector {
			if _, exists := selector[key]; !exists {
				selector[key] = nil
			}
		}
		if typ == corev1.ServiceTypeClusterIP && len(live.Spec.ExternalIPs) == 0 {
			spec["externalTrafficPolicy"] = nil
		}
	}
	if typ != corev1.ServiceTypeLoadBalancer {
		for _, key := range []string{"allocateLoadBalancerNodePorts", "loadBalancerClass", "healthCheckNodePort", "loadBalancerIP", "loadBalancerSourceRanges"} {
			spec[key] = nil
		}
	}
	bindTemplateIdentity(service, current)
	return service
}
