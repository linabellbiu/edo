package model

import (
	"reflect"
	"sort"
)

// KubernetesDeploymentNetworkEqual 比较网络配置语义，不改写方案、草稿或历史快照。
// 旧版关闭的两个示例与新版单个示例等价，主动删除全部端口仍属于配置变更。
func KubernetesDeploymentNetworkEqual(a, b *KubernetesDeploymentSettings) bool {
	leftNodePort, left := kubernetesNetworkComparison(a)
	rightNodePort, right := kubernetesNetworkComparison(b)
	return leftNodePort == rightNodePort && reflect.DeepEqual(left, right)
}

// KubernetesDeploymentNetworkDefault 区分无需网络修改权限的默认配置与自定义端口。
func KubernetesDeploymentNetworkDefault(c *KubernetesDeploymentSettings) bool {
	nodePort, ports := kubernetesNetworkComparison(c)
	return !nodePort && (len(ports) == 0 || kubernetesDefaultPortExample(ports))
}

func kubernetesNetworkComparison(c *KubernetesDeploymentSettings) (bool, []KubernetesDeploymentPort) {
	if c == nil {
		c = DefaultKubernetesDeploymentSettings()
	}
	ports := normalizedKubernetesNetworkPorts(c.Ports)
	if kubernetesDefaultPortExample(ports) {
		ports = normalizedKubernetesNetworkPorts([]KubernetesDeploymentPort{{Name: "http", Port: 8080, TargetPort: 8080}})
	}
	return c.NodePort, ports
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
