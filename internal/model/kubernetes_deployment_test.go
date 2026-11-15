package model

import "testing"

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
		c.Ports[1].Enabled = true
		c.Ports[1].Port = c.Ports[0].Port
	}, func(c *KubernetesDeploymentSettings) { c.NodePort = true; c.Ports[0].NodePort = 80 }} {
		bad := DefaultKubernetesDeploymentSettings()
		mutate(bad)
		if err := ValidateKubernetesDeploymentSettings(bad); err == nil {
			t.Fatal("非法模板被接受", bad)
		}
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
