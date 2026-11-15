package model

import "testing"

func TestWorkflowPlanSnapshotClonesConfigurationAndRejectsResourceIdentity(t *testing.T) {
	base := DeploymentPlan{ID: "source", Name: "通用方案", Kind: DeploymentPlanKubernetes, TimeoutSeconds: 600, ProjectID: "project", ExecutionConfig: &DeploymentExecutionConfig{Kubernetes: DefaultKubernetesDeploymentSettings()}}
	snapshot := NewWorkflowDeploymentPlanSnapshot(base, nil)
	if err := ValidateWorkflowDeploymentPlanSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	base.ExecutionConfig.Kubernetes.CPURequest = "400m"
	pinned := WorkflowDeploymentSnapshotBase(base, &WorkflowDeploymentOverride{PlanSnapshot: snapshot})
	if pinned.ID != "source" || pinned.ProjectID != "project" || pinned.ExecutionConfig.Kubernetes.CPURequest != "200m" {
		t.Fatal("没有正确隔离来源配置", pinned)
	}
	pinned.ExecutionConfig.Kubernetes.CPURequest = "300m"
	if snapshot.ExecutionConfig.Kubernetes.CPURequest != "200m" {
		t.Fatal("有效配置修改污染了初始快照")
	}
	snapshot.DeploymentTargetID = "target"
	snapshot.DeploymentTarget = &DeploymentTarget{Platform: DeploymentKubernetes, ProjectID: "forged"}
	if err := ValidateWorkflowDeploymentPlanSnapshot(snapshot); err == nil {
		t.Fatal("接受伪造目标归属")
	}
}
