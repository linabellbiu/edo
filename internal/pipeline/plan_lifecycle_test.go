package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"edo/internal/model"
)

func TestBuildPlanDeleteChecksDraftWorkflowsAndTemplates(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	plan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "引用保护构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile",
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "build_reference", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	var workflow model.ReleaseWorkflow
	if err := db.First(&workflow, "application_id = ?", application.ID).Error; err != nil {
		t.Fatal(err)
	}
	workflow.IsActive = false
	workflow.Stages = resourceReferenceStages(model.WorkflowNodeBuild, plan.ID)
	if err := db.Save(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteBuildPlan(ctx, plan.ID); !errors.Is(err, ErrBuildPlanInUse) {
		t.Fatalf("停用应用流水线的引用未阻止删除: %v", err)
	}

	workflow.Stages = []model.WorkflowStage{}
	if err := db.Save(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	template := model.ReleaseWorkflowTemplate{
		ID: "draft-build-template", SchemaVersion: model.WorkflowSchemaVersion,
		Name: "停用构建方案引用", Revision: 1, IsActive: false,
		Source: resourceReferenceSource(), Stages: resourceReferenceStages(model.WorkflowNodeBuild, plan.ID),
		CreatedBy: "admin", UpdatedBy: "admin", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := db.Create(&template).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteBuildPlan(ctx, plan.ID); !errors.Is(err, ErrBuildPlanInUse) {
		t.Fatalf("停用公共流水线方案的引用未阻止删除: %v", err)
	}
	if err := db.Delete(&template).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteBuildPlan(ctx, plan.ID); err != nil {
		t.Fatalf("解除所有引用后无法删除构建方案: %v", err)
	}
}

func TestDeploymentPlanLifecycleAndReferenceProtection(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	target := model.DeploymentTarget{
		ID: "lifecycle-target", Name: "生命周期部署位置", Platform: model.DeploymentSSH,
		EnvironmentID: "lifecycle-environment", HostID: "lifecycle-host", WorkingDirectory: "/srv/app",
		RolloutTimeout: 120,
	}
	targetInput := deploymentPlanTargetInput(t, service, target)
	plan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "生命周期部署", Kind: model.DeploymentPlanScript, Script: "set -eu\necho deploy\n",
		TimeoutSeconds: 120, DeploymentTarget: targetInput,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetDeploymentPlanActive(ctx, plan.ID, false); err != nil {
		t.Fatalf("停用部署方案失败: %v", err)
	}
	plans, err := service.ListDeploymentPlans(ctx)
	if err != nil || len(plans) != 1 || plans[0].IsActive {
		t.Fatalf("停用方案未保留在普通列表: plans=%+v err=%v", plans, err)
	}

	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "deploy_reference", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	var workflow model.ReleaseWorkflow
	if err := db.First(&workflow, "application_id = ?", application.ID).Error; err != nil {
		t.Fatal(err)
	}
	workflow.IsActive = false
	workflow.Stages = resourceReferenceStages(model.WorkflowNodeDeploy, plan.ID)
	if err := db.Save(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteDeploymentPlan(ctx, plan.ID); !errors.Is(err, ErrDeploymentPlanInUse) {
		t.Fatalf("停用应用流水线的引用未阻止删除: %v", err)
	}

	workflow.Stages = []model.WorkflowStage{}
	if err := db.Save(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	template := model.ReleaseWorkflowTemplate{
		ID: "draft-deployment-template", SchemaVersion: model.WorkflowSchemaVersion,
		Name: "停用部署方案引用", Revision: 1, IsActive: false,
		Source: resourceReferenceSource(), Stages: resourceReferenceStages(model.WorkflowNodeDeploy, plan.ID),
		CreatedBy: "admin", UpdatedBy: "admin", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := db.Create(&template).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteDeploymentPlan(ctx, plan.ID); !errors.Is(err, ErrDeploymentPlanInUse) {
		t.Fatalf("停用公共流水线方案的引用未阻止删除: %v", err)
	}
	if err := db.Delete(&template).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteDeploymentPlan(ctx, plan.ID); err != nil {
		t.Fatalf("解除所有引用后无法删除部署方案: %v", err)
	}
	plans, err = service.ListDeploymentPlans(ctx)
	if err != nil || len(plans) != 0 {
		t.Fatalf("软删除方案仍出现在普通列表: plans=%+v err=%v", plans, err)
	}
	var deleted model.DeploymentPlan
	if err := db.Unscoped().First(&deleted, "id = ?", plan.ID).Error; err != nil || !deleted.DeletedAt.Valid {
		t.Fatalf("部署方案未软删除: plan=%+v err=%v", deleted, err)
	}
	if err := service.SetDeploymentPlanActive(ctx, plan.ID, true); !errors.Is(err, ErrDeploymentPlanNotFound) {
		t.Fatalf("软删除方案仍可修改状态: %v", err)
	}
	if _, err := service.UpdateDeploymentPlan(ctx, plan.ID, DeploymentPlanInput{
		Name: plan.Name, Kind: model.DeploymentPlanScript, Script: "echo changed\n",
		TimeoutSeconds: 120, DeploymentTarget: targetInput,
	}); !errors.Is(err, ErrDeploymentPlanNotFound) {
		t.Fatalf("软删除方案仍可更新: %v", err)
	}
	if err := service.DeleteDeploymentPlan(ctx, plan.ID); !errors.Is(err, ErrDeploymentPlanNotFound) {
		t.Fatalf("重复删除未返回不存在: %v", err)
	}
	var targetCount int64
	if err := db.Model(&model.DeploymentTarget{}).Where("id = ?", plan.DeploymentTargetID).Count(&targetCount).Error; err != nil || targetCount != 0 {
		t.Fatalf("删除方案应清理无法独立管理的内部执行位置: count=%d err=%v", targetCount, err)
	}
}

func TestDisabledDeploymentPlanRejectedByNewWorkflowValidation(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	ctx := context.Background()
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "停用验证构建", Kind: model.BuildPlanScript, Script: "echo ok > output", ArtifactPath: "output",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "停用验证部署", Kind: model.DeploymentPlanScript, Script: "echo deploy\n", TimeoutSeconds: 120,
		DeploymentTarget: deploymentPlanTargetInput(t, service, model.DeploymentTarget{
			ID: "disabled-validation-target", Name: "停用验证位置", Platform: model.DeploymentSSH,
			EnvironmentID: "disabled-validation-environment", HostID: "disabled-validation-host", RolloutTimeout: 120,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetDeploymentPlanActive(ctx, plan.ID, false); err != nil {
		t.Fatal(err)
	}
	source, stages := testStageWorkflowGraph(buildPlan.ID, plan.ID)
	issues := service.validateWorkflow(ctx, &model.Application{}, model.WorkflowSchemaVersion, source, stages)
	if !hasWorkflowIssue(issues, "missing_deployment_plan") {
		t.Fatalf("新流水线验证未拒绝停用部署方案: %+v", issues)
	}
}

func resourceReferenceSource() model.WorkflowNode {
	return model.WorkflowNode{
		ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源",
		Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"push"}},
	}
}

func resourceReferenceStages(nodeType model.WorkflowNodeType, resourceID string) []model.WorkflowStage {
	config := model.WorkflowNodeConfig{}
	if nodeType == model.WorkflowNodeBuild {
		config.BuildPlanID = resourceID
	} else {
		config.DeploymentPlanID = resourceID
	}
	return []model.WorkflowStage{{
		ID: "stage", Name: "阶段",
		Tasks: []model.WorkflowNode{{ID: "task", Type: nodeType, Name: "任务", Config: config}},
	}}
}
