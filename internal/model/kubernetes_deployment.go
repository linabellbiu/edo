package model

import (
	"errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"log/slog"
)

type KubernetesDeploymentPort struct {
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Port       int32  `json:"port"`
	TargetPort int32  `json:"target_port"`
	NodePort   int32  `json:"node_port,omitempty"`
}

type KubernetesDeploymentSettings struct {
	Template      string                     `json:"template"`
	Replicas      int32                      `json:"replicas"`
	CPURequest    string                     `json:"cpu_request"`
	MemoryRequest string                     `json:"memory_request"`
	CPULimit      string                     `json:"cpu_limit"`
	MemoryLimit   string                     `json:"memory_limit"`
	NodePort      bool                       `json:"node_port"`
	Ports         []KubernetesDeploymentPort `json:"ports"`
}

func DefaultKubernetesDeploymentSettings() *KubernetesDeploymentSettings {
	return &KubernetesDeploymentSettings{Template: "web", Replicas: 1, CPURequest: "200m", MemoryRequest: "300Mi", CPULimit: "1", MemoryLimit: "1Gi", Ports: []KubernetesDeploymentPort{{Name: "http", Port: 8080, TargetPort: 8080}, {Name: "tcp", Port: 8888, TargetPort: 8888}}}
}

var ErrKubernetesDeploymentSettings = errors.New("Kubernetes 模板配置无效，请检查副本、资源申请与限制、端口及 NodePort")

func ValidateKubernetesDeploymentSettings(c *KubernetesDeploymentSettings) error {
	if c == nil {
		return nil
	}
	valid := c.Template == "web" && c.Replicas >= 1 && c.Replicas <= 100 && len(c.Ports) == 2
	for _, pair := range [][2]string{{c.CPURequest, c.CPULimit}, {c.MemoryRequest, c.MemoryLimit}} {
		a, errA := resource.ParseQuantity(pair[0])
		b, errB := resource.ParseQuantity(pair[1])
		valid = valid && len(pair[0]) <= 32 && len(pair[1]) <= 32 && errA == nil && errB == nil && a.Sign() > 0 && b.Sign() > 0 && a.Cmp(b) <= 0
	}
	names, ports, nodes := map[string]bool{}, map[int32]bool{}, map[int32]bool{}
	for _, p := range c.Ports {
		valid = valid && (p.Name == "http" || p.Name == "tcp") && !names[p.Name] && p.Port >= 1 && p.Port <= 65535 && p.TargetPort >= 1 && p.TargetPort <= 65535 && (p.NodePort == 0 || p.NodePort >= 30000 && p.NodePort <= 32767)
		names[p.Name] = true
		if p.Enabled {
			valid = valid && !ports[p.Port] && (p.NodePort == 0 || !nodes[p.NodePort])
			ports[p.Port] = true
			if p.NodePort != 0 {
				nodes[p.NodePort] = true
			}
		}
	}
	if valid {
		return nil
	}
	slog.Warn("Kubernetes 部署模板参数无效", "operation", "kubernetes_template_normalize", "err", ErrKubernetesDeploymentSettings)
	return ErrKubernetesDeploymentSettings
}
