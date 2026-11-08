package deployment

import (
	"context"
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

func TestMultiHostRollbackResolvesExactOriginalHost(t *testing.T) {
	s, db, runtimeID := newDeploymentTestService(t)
	ctx := context.Background()
	target, err := s.CreateTarget(ctx, "admin", TargetInput{Name: "逐主机回退", Platform: model.DeploymentDocker, EnvironmentID: dockerTestEnvironmentID, HostIDs: []string{dockerTestHostID}, WorkloadName: "sample", RolloutTimeout: 120})
	if err != nil {
		t.Fatal(err)
	}
	repository := model.GitRepository{ID: "multi-host-rollback-repository", Name: "回退仓库", Provider: model.GitProviderGeneric, CloneURL: "https://example.invalid/repo.git", AuthType: model.GitAuthNone, CreatedBy: "admin"}
	if err = db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&model.Application{ID: "app", Name: "rollback_app", RepositoryID: repository.ID, CreatedBy: "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	definition := &model.WorkflowDeploymentPlan{Name: "逐主机回退", Kind: model.DeploymentPlanDocker, TimeoutSeconds: 120, DeploymentTarget: target}
	workflow := model.ReleaseWorkflow{ID: "multi-host-rollback-workflow", ApplicationID: "app", ProjectID: target.ProjectID, DepartmentID: target.DepartmentID, Name: "逐主机回退", SchemaVersion: 1, Revision: 1, Stages: []model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentSource: "inline", InlineDeploymentPlan: definition}}}}}}
	if err = db.Create(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	nodeID := model.DeploymentHostNodeID("deploy", dockerTestHostID)
	planID, targetID := model.InlineDeploymentIdentity(workflow.ID, nodeID)
	source := &model.DeploymentRecord{ApplicationID: "app", ProjectID: target.ProjectID, DepartmentID: target.DepartmentID, HostID: dockerTestHostID, RuntimeID: runtimeID, EnvironmentID: dockerTestEnvironmentID, DeploymentPlanID: planID, TargetID: targetID, WorkflowNodeID: nodeID}
	plan, resolved, err := s.rollbackPlanTarget(ctx, source, model.PipelineRun{WorkflowID: workflow.ID})
	if err != nil || plan.ID != planID || resolved.ID != targetID || resolved.HostID != dockerTestHostID {
		t.Fatalf("自定义多主机回退身份错误: %+v %+v %v", plan, resolved, err)
	}
	definition.DeploymentTarget.HostIDs = []string{}
	if err = db.Save(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.rollbackPlanTarget(ctx, source, model.PipelineRun{WorkflowID: workflow.ID}); err == nil {
		t.Fatal("流程已移除主机后不得重新接管原主机")
	}
	library := model.DeploymentPlan{ID: "multi-host-library", Name: "多主机方案库回退", Kind: model.DeploymentPlanDocker, DeploymentTargetID: target.ID, IsActive: true, CreatedBy: "admin"}
	if err = db.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	source.DeploymentPlanID = library.ID
	source.TargetID = model.DeploymentHostNodeID(target.ID, dockerTestHostID)
	_, resolved, err = s.rollbackPlanTarget(ctx, source, model.PipelineRun{})
	if err != nil || resolved.ID != source.TargetID || resolved.HostID != source.HostID {
		t.Fatalf("库方案多主机回退身份错误: %+v %v", resolved, err)
	}
}
