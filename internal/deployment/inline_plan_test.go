package deployment

import (
	"edo/internal/database"
	"edo/internal/model"
	"testing"
)

func TestInlineDeploymentRollbackChecksCurrentWorkflowWithoutLibraryRecord(t *testing.T) {
	f := newKubernetesRollbackFixture(t, model.DeploymentSucceeded)
	ctx := deploymentExecutionTestContext("project-a")
	environment := model.Environment{ID: "inline-rollback-env", ProjectID: "project-a", DepartmentID: database.DefaultDepartmentID, Name: "自定义部署环境", IsActive: true}
	if err := f.db.WithContext(ctx).Create(&environment).Error; err != nil {
		t.Fatal(err)
	}
	definition := &model.WorkflowDeploymentPlan{Name: "流水线内回退", Kind: model.DeploymentPlanKubernetes, TimeoutSeconds: 30, DeploymentTarget: &model.DeploymentTarget{Platform: model.DeploymentKubernetes, EnvironmentID: environment.ID, RuntimeID: f.source.RuntimeID, Namespace: f.source.Namespace, WorkloadName: f.source.WorkloadName, ContainerName: f.source.ContainerName}}
	workflow := model.ReleaseWorkflow{ID: "inline-rollback-workflow", ApplicationID: f.source.ApplicationID, ProjectID: "project-a", DepartmentID: database.DefaultDepartmentID, Name: "流水线内回退", SchemaVersion: model.WorkflowSchemaVersion, Revision: 1, IsActive: true, Stages: []model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentSource: "inline", InlineDeploymentPlan: definition}}}}}}
	if err := f.db.WithContext(ctx).Create(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	planID, targetID := model.InlineDeploymentIdentity(workflow.ID, "deploy")
	f.source.DeploymentPlanID, f.source.TargetID, f.source.WorkflowNodeID, f.source.EnvironmentID = planID, targetID, "deploy", environment.ID
	if err := f.db.WithContext(ctx).Save(&f.source).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.WithContext(ctx).Model(&model.PipelineRun{}).Where("uuid = ?", f.source.PipelineRunID).Update("workflow_id", workflow.ID).Error; err != nil {
		t.Fatal(err)
	}
	preview, err := f.service.RollbackPreview(ctx, f.source.ID)
	if err != nil || !preview.CanRollback {
		t.Fatalf("没有方案库记录的流水线自定义部署应可按原归属回退: %+v %v", preview, err)
	}
	definition.DeploymentTarget.Namespace = "changed"
	if err := f.db.WithContext(ctx).Save(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	preview, err = f.service.RollbackPreview(ctx, f.source.ID)
	if err != nil || preview.CanRollback {
		t.Fatalf("修改当前流水线目标后不应回退原位置: %+v %v", preview, err)
	}
	if f.writes != 0 {
		t.Fatal("预检不应修改工作负载")
	}
}
