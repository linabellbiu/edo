package model

import (
	"encoding/json"
	"testing"
)

func TestKubernetesHPAValidationAndStageSnapshot(t *testing.T) {
	for _, hpa := range []*KubernetesDeploymentHPA{
		{Enabled: true, MinReplicas: 0, MaxReplicas: 3, CPUUtilization: new(int32(80))},
		{Enabled: true, MinReplicas: 4, MaxReplicas: 3, CPUUtilization: new(int32(80))},
		{Enabled: true, MinReplicas: 1, MaxReplicas: 101, CPUUtilization: new(int32(80))},
		{Enabled: true, MinReplicas: 1, MaxReplicas: 3},
		{Enabled: true, MinReplicas: 1, MaxReplicas: 3, MemoryUtilization: new(int32(0))},
		{Enabled: true, MinReplicas: 1, MaxReplicas: 3, CPUUtilization: new(int32(1001))},
	} {
		cfg := DefaultKubernetesDeploymentSettings()
		cfg.HPA = hpa
		if ValidateKubernetesDeploymentSettings(cfg) == nil {
			t.Fatal("非法 HPA 配置被接受", hpa)
		}
	}
	base := DefaultKubernetesDeploymentSettings()
	patched, err := ApplyWorkflowKubernetesPatch(base, json.RawMessage(`{"hpa":{"enabled":true,"min_replicas":2,"max_replicas":5,"cpu_utilization":80}}`))
	if err != nil || !patched.AutoscalingEnabled() || base.HPA != nil {
		t.Fatal("阶段 HPA 覆盖污染来源或丢失", err)
	}
	copy := DeploymentExecutionFromNode((&DeploymentExecutionConfig{Kubernetes: patched}).NodeConfig())
	*copy.Kubernetes.HPA.CPUUtilization = 90
	if *patched.HPA.CPUUtilization != 80 {
		t.Fatal("运行快照与共享配置没有隔离")
	}
	cleared, err := ApplyWorkflowKubernetesPatch(patched, json.RawMessage(`{"hpa":null}`))
	if err != nil || cleared.AutoscalingEnabled() {
		t.Fatal("恢复阶段初始值未清除 HPA", err)
	}
}
