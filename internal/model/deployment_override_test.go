package model

import (
	"encoding/json"
	"testing"
)

func TestWorkflowDeploymentOverrideMergePatchPreservesExplicitValuesAndIsolation(t *testing.T) {
	base := DefaultKubernetesDeploymentSettings()
	base.NodePort = true
	base.Ports[0].Enabled = true
	patched, err := ApplyWorkflowKubernetesPatch(base, json.RawMessage(`{"node_port":false,"replicas":2}`))
	if err != nil || patched.NodePort || patched.Replicas != 2 || patched.CPURequest != base.CPURequest {
		t.Fatal("覆盖没有保留显式 false 或继承未修改值", patched, err)
	}
	patched.Ports[0].Enabled = false
	if !base.NodePort || !base.Ports[0].Enabled || base.Replicas != 1 {
		t.Fatal("阶段覆盖污染了通用方案")
	}
	if inherited, err := ApplyWorkflowKubernetesPatch(base, json.RawMessage(`{}`)); err != nil || !inherited.NodePort {
		t.Fatal("空补丁没有继承方案", inherited, err)
	}
	cleared, err := ApplyWorkflowKubernetesPatch(base, json.RawMessage(`{"ports":[]}`))
	if err != nil || len(cleared.Ports) != 0 || len(base.Ports) != 1 || !base.Ports[0].Enabled {
		t.Fatal("删除本阶段全部端口未保留空数组或污染来源", cleared, err)
	}
	for _, raw := range []string{`null`, `[]`, `true`, `{"unknown":null}`, `{"ports":[{"name":"http","enabled":true,"port":8080,"target_port":8080,"unknown":null}]}`, `{"replicas":0}`, `{"node_port":"false"}`} {
		if _, err := ApplyWorkflowKubernetesPatch(base, json.RawMessage(raw)); err == nil {
			t.Fatal("不合法或未知参数未拒绝", raw)
		}
	}
	if err := ValidateWorkflowDeploymentOverride(&WorkflowDeploymentOverride{DeploymentTarget: &DeploymentTarget{Platform: DeploymentKubernetes, ProjectID: "forged"}}); err == nil {
		t.Fatal("接受了客户端伪造的目标归属")
	}
}
