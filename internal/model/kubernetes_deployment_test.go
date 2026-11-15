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
