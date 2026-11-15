package model

import (
	"reflect"
	"sort"
)

// KubernetesDeploymentNetworkEqual 比较网络配置语义，不改写方案、草稿或历史快照。
// 旧版关闭的两个示例与新版单个示例等价，主动删除全部端口仍属于配置变更。
func KubernetesDeploymentNetworkEqual(a, b *KubernetesDeploymentSettings) bool {
	leftType, leftName, left := kubernetesNetworkComparison(a)
	rightType, rightName, right := kubernetesNetworkComparison(b)
	return leftType == rightType && leftName == rightName && reflect.DeepEqual(left, right)
}

// KubernetesDeploymentNetworkDefault 区分无需网络修改权限的默认配置与自定义端口。
func KubernetesDeploymentNetworkDefault(c *KubernetesDeploymentSettings) bool {
	serviceType, _, ports := kubernetesNetworkComparison(c)
	return serviceType == "ClusterIP" && (len(ports) == 0 || kubernetesDefaultPortExample(ports))
}

func kubernetesNetworkComparison(c *KubernetesDeploymentSettings) (string, string, []KubernetesDeploymentPort) {
	if c == nil {
		c = DefaultKubernetesDeploymentSettings()
	}
	serviceType := c.EffectiveServiceType()
	if serviceType == "ExternalName" {
		// 外部 DNS 别名不使用 Pod 选择器和端口草稿，也不沿用旧 NodePort 开关。
		return serviceType, c.ExternalName, nil
	}
	ports := normalizedKubernetesNetworkPorts(c.Ports)
	if serviceType == "ClusterIP" {
		for index := range ports {
			ports[index].NodePort = 0 // 当前类型不使用的节点端口仅是保留草稿。
		}
	}
	if kubernetesDefaultPortExample(ports) {
		ports = normalizedKubernetesNetworkPorts([]KubernetesDeploymentPort{{Name: "http", Port: 8080, TargetPort: 8080}})
	}
	return serviceType, "", ports
}

func normalizedKubernetesNetworkPorts(input []KubernetesDeploymentPort) []KubernetesDeploymentPort {
	if len(input) == 0 {
		return nil
	}
	ports := append([]KubernetesDeploymentPort(nil), input...)
	for i := range ports {
		if ports[i].Protocol == "" {
			ports[i].Protocol = "TCP"
		}
	}
	sort.Slice(ports, func(i, j int) bool {
		a, b := ports[i], ports[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		if a.TargetPort != b.TargetPort {
			return a.TargetPort < b.TargetPort
		}
		if a.Protocol != b.Protocol {
			return a.Protocol < b.Protocol
		}
		if a.NodePort != b.NodePort {
			return a.NodePort < b.NodePort
		}
		return !a.Enabled && b.Enabled
	})
	return ports
}

func kubernetesDefaultPortExample(ports []KubernetesDeploymentPort) bool {
	current := normalizedKubernetesNetworkPorts([]KubernetesDeploymentPort{{Name: "http", Port: 8080, TargetPort: 8080}})
	if reflect.DeepEqual(ports, current) {
		return true
	}
	legacy := normalizedKubernetesNetworkPorts([]KubernetesDeploymentPort{{Name: "http", Port: 8080, TargetPort: 8080}, {Name: "tcp", Port: 8888, TargetPort: 8888}})
	return reflect.DeepEqual(ports, legacy)
}
