package model

import (
	"reflect"
	"testing"
)

func TestKubernetesDeploymentNetworkComparisonKeepsLegacyDefaultsAndExplicitDeletion(t *testing.T) {
	current := &KubernetesDeploymentSettings{Ports: []KubernetesDeploymentPort{{Name: "http", Port: 8080, TargetPort: 8080}}}
	legacy := &KubernetesDeploymentSettings{Ports: []KubernetesDeploymentPort{{Name: "http", Port: 8080, TargetPort: 8080}, {Name: "tcp", Port: 8888, TargetPort: 8888}}}
	empty := &KubernetesDeploymentSettings{Ports: []KubernetesDeploymentPort{}}
	for _, pair := range [][2]*KubernetesDeploymentSettings{{current, legacy}, {legacy, current}, {nil, current}, {empty, {}}} {
		if !KubernetesDeploymentNetworkEqual(pair[0], pair[1]) {
			t.Fatal("兼容默认值或空端口自身被识别为修改", pair)
		}
	}
	for _, cfg := range []*KubernetesDeploymentSettings{nil, current, legacy, empty, {}} {
		if !KubernetesDeploymentNetworkDefault(cfg) {
			t.Fatal("关闭的默认网络配置要求额外权限", cfg)
		}
	}
	if KubernetesDeploymentNetworkEqual(current, empty) || KubernetesDeploymentNetworkEqual(empty, legacy) {
		t.Fatal("主动删除端口示例未被识别为变更")
	}
	before := append([]KubernetesDeploymentPort(nil), legacy.Ports...)
	_ = KubernetesDeploymentNetworkEqual(legacy, current)
	if !reflect.DeepEqual(legacy.Ports, before) || current.Ports[0].Protocol != "" {
		t.Fatal("权限比较改写了保存配置")
	}
}

func TestKubernetesDeploymentNetworkComparisonNormalizesProtocolAndOrder(t *testing.T) {
	a := &KubernetesDeploymentSettings{Ports: []KubernetesDeploymentPort{{Name: "dns-udp", Enabled: true, Port: 53, TargetPort: 1053, Protocol: "UDP"}, {Name: "dns-tcp", Enabled: true, Port: 53, TargetPort: 2053}}}
	b := &KubernetesDeploymentSettings{Ports: []KubernetesDeploymentPort{{Name: "dns-tcp", Enabled: true, Port: 53, TargetPort: 2053, Protocol: "TCP"}, {Name: "dns-udp", Enabled: true, Port: 53, TargetPort: 1053, Protocol: "UDP"}}}
	before := append([]KubernetesDeploymentPort(nil), a.Ports...)
	if !KubernetesDeploymentNetworkEqual(a, b) {
		t.Fatal("端口顺序或默认 TCP 导致误判")
	}
	if !reflect.DeepEqual(a.Ports, before) {
		t.Fatal("排序或协议默认值污染输入")
	}
	if KubernetesDeploymentNetworkDefault(a) {
		t.Fatal("自定义网络被视为默认")
	}
}

func TestKubernetesDeploymentNetworkComparisonDetectsEveryConfiguredChange(t *testing.T) {
	for name, mutate := range map[string]func(*KubernetesDeploymentSettings){
		"全局 NodePort": func(c *KubernetesDeploymentSettings) { c.NodePort = true },
		"端口名称":        func(c *KubernetesDeploymentSettings) { c.Ports[0].Name = "web" },
		"服务端口":        func(c *KubernetesDeploymentSettings) { c.Ports[0].Port = 8081 },
		"容器映射":        func(c *KubernetesDeploymentSettings) { c.Ports[0].TargetPort = 8081 },
		"启用":          func(c *KubernetesDeploymentSettings) { c.Ports[0].Enabled = true },
		"UDP 协议":      func(c *KubernetesDeploymentSettings) { c.Ports[0].Protocol = "UDP" },
		"节点端口":        func(c *KubernetesDeploymentSettings) { c.Ports[0].NodePort = 30080 },
		"添加关闭端口": func(c *KubernetesDeploymentSettings) {
			c.Ports = append(c.Ports, KubernetesDeploymentPort{Name: "metrics", Port: 9090, TargetPort: 9090})
		},
	} {
		t.Run(name, func(t *testing.T) {
			base := &KubernetesDeploymentSettings{Ports: []KubernetesDeploymentPort{{Name: "http", Port: 8080, TargetPort: 8080}}}
			changed := &KubernetesDeploymentSettings{Ports: append([]KubernetesDeploymentPort(nil), base.Ports...)}
			mutate(changed)
			if KubernetesDeploymentNetworkEqual(base, changed) || KubernetesDeploymentNetworkDefault(changed) {
				t.Fatal("网络配置变更被默认值逻辑忽略", changed)
			}
		})
	}
	emptyNodePort := &KubernetesDeploymentSettings{NodePort: true}
	if KubernetesDeploymentNetworkDefault(emptyNodePort) || KubernetesDeploymentNetworkEqual(emptyNodePort, &KubernetesDeploymentSettings{}) {
		t.Fatal("空端口列表忽略了全局 NodePort 开关")
	}
}
