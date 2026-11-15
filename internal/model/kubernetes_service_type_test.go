package model

import (
	"strings"
	"testing"
)

func TestKubernetesServiceTypeValidationAndInactiveDrafts(t *testing.T) {
	for _, typ := range []string{"", "ClusterIP", "NodePort", "LoadBalancer", "ExternalName"} {
		t.Run(typ, func(t *testing.T) {
			cfg := DefaultKubernetesDeploymentSettings()
			cfg.ServiceType, cfg.ExternalName = typ, "database.example.com."
			if typ == "ExternalName" {
				cfg.Ports = []KubernetesDeploymentPort{{Name: "", Enabled: true, Port: -1, TargetPort: 0, NodePort: -80, Protocol: "draft"}}
			}
			if err := ValidateKubernetesDeploymentSettings(cfg); err != nil || cfg.ExternalName != "database.example.com." {
				t.Fatal("合法服务类型或隐藏端口草稿被拒绝或改写", cfg, err)
			}
		})
	}
	legacy := DefaultKubernetesDeploymentSettings()
	legacy.NodePort = true
	if legacy.EffectiveServiceType() != "NodePort" {
		t.Fatal("历史 NodePort 未按原语义读取")
	}
	legacy.ServiceType = "ClusterIP"
	legacy.Ports[0].NodePort = -80
	legacy.Ports = append(legacy.Ports, KubernetesDeploymentPort{Name: "dns", Port: 53, TargetPort: 53, NodePort: -80})
	legacy.ExternalName = "INVALID DNS DRAFT"
	if legacy.EffectiveServiceType() != "ClusterIP" || ValidateKubernetesDeploymentSettings(legacy) != nil || !legacy.NodePort || legacy.Ports[0].NodePort != -80 {
		t.Fatal("显式类型没有覆盖旧开关，或修改了隐藏草稿", legacy)
	}
	same := *legacy
	same.Ports = append([]KubernetesDeploymentPort(nil), legacy.Ports...)
	for index := range same.Ports {
		same.Ports[index].NodePort = 0
	}
	if !KubernetesDeploymentNetworkEqual(legacy, &same) {
		t.Fatal("ClusterIP 无效节点端口草稿被当作生效网络变更")
	}
	legacy.ServiceType = "NodePort"
	if ValidateKubernetesDeploymentSettings(legacy) == nil {
		t.Fatal("切回 NodePort 后未重新验证隐藏的节点端口")
	}
	for _, host := range []string{"", ".", "UPPER.example.com", "example.com..", " example.com", "example.com ", "https://example.com", "example.com:80", "example.com/path", "127.0.0.1", "127.0.0.1.", "2001:db8::1", strings.Repeat("a", 64) + ".example.com"} {
		cfg := DefaultKubernetesDeploymentSettings()
		cfg.ServiceType, cfg.ExternalName = "ExternalName", host
		if ValidateKubernetesDeploymentSettings(cfg) == nil {
			t.Fatal("非法 ExternalName 被接受", host)
		}
	}
	unknown := DefaultKubernetesDeploymentSettings()
	unknown.ServiceType = "Headless"
	if ValidateKubernetesDeploymentSettings(unknown) == nil {
		t.Fatal("Headless 被伪装成 Service type")
	}
}
