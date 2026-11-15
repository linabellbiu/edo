package deployment

import (
	"edo/internal/model"
	"encoding/json"
	"testing"
)

func TestKubernetesTemplateSnapshotRetainsOriginalConfiguration(t *testing.T) {
	f := newKubernetesRollbackFixture(t, model.DeploymentQueued)
	original := model.DefaultKubernetesDeploymentSettings()
	snapshot := map[string]any{"authority_id": "operator", "deployment_plans": map[string]any{f.source.WorkflowNodeID: map[string]any{"id": f.source.DeploymentPlanID, "kubernetes": original}}}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.Model(&model.PipelineRun{}).Where("uuid = ?", f.source.PipelineRunID).Update("workflow_snapshot", string(body)).Error; err != nil {
		t.Fatal(err)
	}
	changed := model.DefaultKubernetesDeploymentSettings()
	changed.Replicas = 5
	var plan model.DeploymentPlan
	if err = f.db.First(&plan, "uuid = ?", f.source.DeploymentPlanID).Error; err != nil {
		t.Fatal(err)
	}
	plan.ExecutionConfig = &model.DeploymentExecutionConfig{Kubernetes: changed}
	if err = f.db.Save(&plan).Error; err != nil {
		t.Fatal(err)
	}
	cfg, err := f.service.kubernetesTemplateSnapshot(deploymentExecutionTestContext("project-a"), &f.source)
	if err != nil || cfg == nil || cfg.Replicas != 1 || cfg.CPURequest != "200m" || cfg.NodePort {
		t.Fatal("执行读取了新方案或丢失默认快照", cfg, err)
	}
	cfg.Replicas = 8
	again, err := f.service.kubernetesTemplateSnapshot(deploymentExecutionTestContext("project-a"), &f.source)
	if err != nil || again.Replicas != 1 {
		t.Fatal("读取副本污染持久化快照", again, err)
	}
	foreign := f.source
	foreign.DeploymentPlanID = "other"
	if _, err = f.service.kubernetesTemplateSnapshot(deploymentExecutionTestContext("project-a"), &foreign); err == nil {
		t.Fatal("借用了其他部署方案的模板快照")
	}
	if f.writes != 0 {
		t.Fatal("快照读取触发集群写入")
	}
}
