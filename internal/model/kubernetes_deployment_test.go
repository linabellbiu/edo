package model

import (
	"fmt"
	"testing"
)

func TestKubernetesTemplateValidationAndImmutableCopy(t *testing.T) {
	cfg := DefaultKubernetesDeploymentSettings()
	input := &DeploymentExecutionConfig{Kubernetes: cfg}
	node := WorkflowNodeConfig{}
	ApplyDeploymentExecution(&node, input)
	node.Kubernetes.Ports[0].Enabled = true
	if cfg.Ports[0].Enabled {
		t.Fatal("运行解析污染了共享模板")
	}
	for _, mutate := range []func(*KubernetesDeploymentSettings){func(c *KubernetesDeploymentSettings) { c.CPURequest = "2" }, func(c *KubernetesDeploymentSettings) { c.MemoryRequest = "2Gi" }, func(c *KubernetesDeploymentSettings) { c.Replicas = 0 }, func(c *KubernetesDeploymentSettings) {
		c.Ports[0].Enabled = true
		c.Ports = append(c.Ports, KubernetesDeploymentPort{Name: "api", Enabled: true, Port: c.Ports[0].Port, TargetPort: 8081})
	}, func(c *KubernetesDeploymentSettings) { c.NodePort = true; c.Ports[0].NodePort = 80 }} {
		bad := DefaultKubernetesDeploymentSettings()
		mutate(bad)
		if err := ValidateKubernetesDeploymentSettings(bad); err == nil {
			t.Fatal("非法模板被接受", bad)
		}
	}
}

func TestKubernetesTemplateDynamicPortsValidateNamesProtocolsAndLimits(t *testing.T) {
	defaults := DefaultKubernetesDeploymentSettings()
	if len(defaults.Ports) != 1 || defaults.Ports[0].Name != "http" || defaults.Ports[0].Enabled || defaults.Ports[0].Port != 8080 || defaults.Ports[0].Protocol != "" {
		t.Fatal("默认模板应只保留关闭的 HTTP 示例", defaults.Ports)
	}
	for _, count := range []int{0, 1, 32} {
		cfg := DefaultKubernetesDeploymentSettings()
		cfg.Ports = nil
		for index := range count {
			cfg.Ports = append(cfg.Ports, KubernetesDeploymentPort{Name: fmt.Sprintf("api-%d", index), Enabled: true, Port: int32(8000 + index), TargetPort: int32(9000 + index)})
		}
		if err := ValidateKubernetesDeploymentSettings(cfg); err != nil {
			t.Fatal("合法动态端口数量被拒绝", count, err)
		}
	}
	legacy := DefaultKubernetesDeploymentSettings()
	legacy.Ports = append(legacy.Ports, KubernetesDeploymentPort{Name: "tcp", Port: 8888, TargetPort: 8888})
	if err := ValidateKubernetesDeploymentSettings(legacy); err != nil || legacy.Ports[1].Protocol != "" {
		t.Fatal("旧双端口配置失效或被改写", err, legacy.Ports)
	}
	valid := DefaultKubernetesDeploymentSettings()
	valid.Ports = []KubernetesDeploymentPort{{Name: "dns-udp", Enabled: true, Port: 53, TargetPort: 5353, Protocol: "UDP"}, {Name: "dns-tcp", Enabled: true, Port: 53, TargetPort: 5353, Protocol: "TCP"}, {Name: "sctp-api", Enabled: true, Port: 5000, TargetPort: 5001, Protocol: "SCTP"}}
	if err := ValidateKubernetesDeploymentSettings(valid); err != nil {
		t.Fatal("同服务端口的不同协议被拒绝", err)
	}
	for name, mutate := range map[string]func(*KubernetesDeploymentSettings){
		"端口数量超限": func(c *KubernetesDeploymentSettings) { c.Ports = make([]KubernetesDeploymentPort, 33) },
		"空名称":    func(c *KubernetesDeploymentSettings) { c.Ports[0].Name = "" },
		"名称大写":   func(c *KubernetesDeploymentSettings) { c.Ports[0].Name = "HTTP" },
		"纯数字名称":  func(c *KubernetesDeploymentSettings) { c.Ports[0].Name = "8080" },
		"名称过长":   func(c *KubernetesDeploymentSettings) { c.Ports[0].Name = "a-very-long-name-16" },
		"禁用行名称重复": func(c *KubernetesDeploymentSettings) {
			c.Ports = append(c.Ports, KubernetesDeploymentPort{Name: "http", Port: 8081, TargetPort: 8081})
		},
		"非法协议": func(c *KubernetesDeploymentSettings) { c.Ports[0].Protocol = "ICMP" },
		"重复默认TCP": func(c *KubernetesDeploymentSettings) {
			c.Ports[0].Enabled = true
			c.Ports = append(c.Ports, KubernetesDeploymentPort{Name: "api", Enabled: true, Port: 8080, TargetPort: 8081, Protocol: "TCP"})
		},
		"跨协议节点端口重复": func(c *KubernetesDeploymentSettings) {
			c.NodePort = true
			c.Ports[0].NodePort = 30080
			c.Ports = append(c.Ports, KubernetesDeploymentPort{Name: "dns", Enabled: true, Port: 53, TargetPort: 53, Protocol: "UDP", NodePort: 30080})
		},
		"非法服务端口": func(c *KubernetesDeploymentSettings) { c.Ports[0].Port = 65536 },
		"非法容器端口": func(c *KubernetesDeploymentSettings) { c.Ports[0].TargetPort = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultKubernetesDeploymentSettings()
			mutate(cfg)
			if ValidateKubernetesDeploymentSettings(cfg) == nil {
				t.Fatal("非法动态端口被接受", cfg.Ports)
			}
		})
	}
}

func TestKubernetesTemplateAdvancedRuntimeValidation(t *testing.T) {
	zero := int64(0)
	valid := DefaultKubernetesDeploymentSettings()
	valid.Command = []string{"/app/server"}
	valid.Args = []string{"--config=/etc/app/config.yaml", "literal $(date)"}
	valid.WorkingDir = "/app"
	valid.ImagePullPolicy = "IfNotPresent"
	valid.TerminationGracePeriodSeconds = &zero
	valid.Strategy = "RollingUpdate"
	valid.MaxSurge = "1"
	valid.MaxUnavailable = "0"
	valid.ReadinessProbe = &KubernetesDeploymentProbe{Kind: "http", Port: 8080, Path: "/ready", Scheme: "HTTP", PeriodSeconds: 10, TimeoutSeconds: 2, FailureThreshold: 3}
	valid.LivenessProbe = &KubernetesDeploymentProbe{Kind: "tcp", Port: 8888}
	if err := ValidateKubernetesDeploymentSettings(valid); err != nil {
		t.Fatal("合法运行参数被拒绝", err)
	}
	for name, mutate := range map[string]func(*KubernetesDeploymentSettings){
		"命令控制字符": func(c *KubernetesDeploymentSettings) { c.Args = []string{"bad\x00arg"} },
		"相对目录":   func(c *KubernetesDeploymentSettings) { c.WorkingDir = "../app" },
		"非规范目录":  func(c *KubernetesDeploymentSettings) { c.WorkingDir = "/app/../tmp" },
		"未知镜像策略": func(c *KubernetesDeploymentSettings) { c.ImagePullPolicy = "sometimes" },
		"负终止宽限":  func(c *KubernetesDeploymentSettings) { value := int64(-1); c.TerminationGracePeriodSeconds = &value },
		"错误探针": func(c *KubernetesDeploymentSettings) {
			c.ReadinessProbe = &KubernetesDeploymentProbe{Kind: "exec", Port: 80}
		},
		"探针端口": func(c *KubernetesDeploymentSettings) {
			c.ReadinessProbe = &KubernetesDeploymentProbe{Kind: "tcp", Port: 0}
		},
		"探针路径": func(c *KubernetesDeploymentSettings) {
			c.ReadinessProbe = &KubernetesDeploymentProbe{Kind: "http", Port: 80, Path: "health"}
		},
		"TCP带HTTP参数": func(c *KubernetesDeploymentSettings) {
			c.ReadinessProbe = &KubernetesDeploymentProbe{Kind: "tcp", Port: 80, Scheme: "HTTPS"}
		},
		"探针超限": func(c *KubernetesDeploymentSettings) {
			c.StartupProbe = &KubernetesDeploymentProbe{Kind: "http", Port: 80, PeriodSeconds: 3601}
		},
		"滚动全部为零":  func(c *KubernetesDeploymentSettings) { c.MaxSurge = "0%"; c.MaxUnavailable = "0" },
		"滚动比例越界":  func(c *KubernetesDeploymentSettings) { c.MaxSurge = "101%" },
		"重建带滚动参数": func(c *KubernetesDeploymentSettings) { c.Strategy = "Recreate"; c.MaxSurge = "1" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultKubernetesDeploymentSettings()
			mutate(cfg)
			if ValidateKubernetesDeploymentSettings(cfg) == nil {
				t.Fatal("非法高级配置被接受")
			}
		})
	}
}
