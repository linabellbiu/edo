package pipeline

import (
	"context"
	"testing"
	"time"

	"edo/internal/deployment"
	"edo/internal/dockerengine"
	"edo/internal/model"
)

func TestEnsureInitialDeliverySettingsCreatesBuildFlowWithoutGroups(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()

	settings, err := service.EnsureInitialDeliverySettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Created || settings.BuildPlanID == "" || settings.WorkflowTemplateID == "" {
		t.Fatalf("默认交付设置不完整: %+v", settings)
	}

	result, err := service.GetWorkflowTemplate(ctx, settings.WorkflowTemplateID)
	if err != nil {
		t.Fatal(err)
	}
	template := result.WorkflowTemplate
	if !result.Valid || !template.IsActive || template.Source.Config.Branch != "*" ||
		template.Source.Config.TagPattern != defaultWorkflowTagPattern ||
		len(template.Source.Config.Events) != 1 || template.Source.Config.Events[0] != "manual" ||
		len(template.Stages) != 1 || len(template.Stages[0].Tasks) != 1 ||
		template.Stages[0].Tasks[0].Type != model.WorkflowNodeBuild {
		t.Fatalf("默认流水线应只包含构建，不隐式绑定部署环境: result=%+v", result)
	}

	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "quick_start_app", RepositoryID: repositoryID,
		WorkflowTemplateID: settings.WorkflowTemplateID,
	})
	if err != nil {
		t.Fatalf("默认流水线无法用于新应用: %v", err)
	}
	if application.Workflow == nil || !application.Workflow.IsActive || len(application.Workflow.Stages) != 1 ||
		application.Workflow.Source.Config.TagPattern != defaultWorkflowTagPattern {
		t.Fatalf("新应用没有获得已启用的完整默认流水线: %+v", application.Workflow)
	}

	again, err := service.EnsureInitialDeliverySettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Created {
		t.Fatal("重复初始化不得再次创建默认交付设置")
	}
	for _, resource := range []struct {
		name  string
		value any
		want  int64
	}{
		{name: "构建方案", value: &model.BuildPlan{}, want: 1},
		{name: "部署方案", value: &model.DeploymentPlan{}, want: 0},
		{name: "部署目标", value: &model.DeploymentTarget{}, want: 0},
		{name: "环境分组", value: &model.Environment{}, want: 0},
		{name: "分组主机关联", value: &model.EnvironmentHost{}, want: 0},
		{name: "流水线方案", value: &model.ReleaseWorkflowTemplate{}, want: 1},
	} {
		var count int64
		if err := db.Unscoped().Model(resource.value).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != resource.want {
			t.Fatalf("%s重复初始化后的数量为 %d，期望 %d", resource.name, count, resource.want)
		}
	}
	var host model.Host
	if err := db.First(&host, "uuid = ?", model.BuiltinLocalHostID).Error; err != nil || !host.IsBuiltin {
		t.Fatalf("不创建默认分组时仍应保留内置本地主机: host=%+v err=%v", host, err)
	}
}

func TestEnsureInitialDeliverySettingsFallsBackWhenLocalDockerUnavailable(t *testing.T) {
	service, db, _, _ := newPipelineTestService(t)
	if err := db.Model(&model.HostCapability{}).
		Where("host_id = ? AND kind = ?", model.BuiltinLocalHostID, model.HostCapabilityDocker).
		Update("status", model.HostCapabilityUnreachable).Error; err != nil {
		t.Fatal(err)
	}

	settings, err := service.EnsureInitialDeliverySettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Created || settings.BuildPlanID == "" || settings.WorkflowTemplateID == "" {
		t.Fatalf("本地 Docker 不可用时不应伪造部署方案: %+v", settings)
	}
	var deploymentCount int64
	if err := db.Model(&model.DeploymentPlan{}).Count(&deploymentCount).Error; err != nil {
		t.Fatal(err)
	}
	if deploymentCount != 0 {
		t.Fatalf("本地 Docker 不可用时创建了部署方案: %d", deploymentCount)
	}
	result, err := service.GetWorkflowTemplate(context.Background(), settings.WorkflowTemplateID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || !result.WorkflowTemplate.IsActive || len(result.WorkflowTemplate.Stages) != 1 ||
		result.WorkflowTemplate.Stages[0].Tasks[0].Type != model.WorkflowNodeBuild {
		t.Fatalf("降级后的默认构建流水线无效: %+v", result)
	}
}

func TestEnsureInitialDeliverySettingsDoesNotRestoreDeletedDefaults(t *testing.T) {
	service, db, _, _ := newPipelineTestService(t)
	settings, err := service.EnsureInitialDeliverySettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteWorkflowTemplate(context.Background(), settings.WorkflowTemplateID); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteBuildPlan(context.Background(), settings.BuildPlanID); err != nil {
		t.Fatal(err)
	}

	again, err := service.EnsureInitialDeliverySettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again.Created {
		t.Fatal("管理员删除默认资源后不得在重启时静默恢复")
	}
	var buildPlanCount int64
	if err := db.Unscoped().Model(&model.BuildPlan{}).Count(&buildPlanCount).Error; err != nil {
		t.Fatal(err)
	}
	if buildPlanCount != 1 {
		t.Fatalf("软删除的默认构建方案没有阻止重新初始化: %d", buildPlanCount)
	}
}

func TestInitialDeliveryPreservesExistingLocalDeployment(t *testing.T) {
	service, db, _, _ := newPipelineTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// 模拟旧版初始化记录；升级不得删除环境、成员关系或已使用的部署目标。
	environment := model.Environment{
		ID: "edo-local-environment", Name: "本地环境", CreatedBy: "system",
		IsActive: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&environment).Error; err != nil {
		t.Fatal(err)
	}
	membership := model.EnvironmentHost{EnvironmentID: environment.ID, HostID: model.BuiltinLocalHostID, CreatedAt: now}
	if err := db.Create(&membership).Error; err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreateDeploymentPlan(ctx, "system", DeploymentPlanInput{
		Name: "快速开始 本地 Docker 部署", Kind: model.DeploymentPlanDocker,
		DeploymentTarget: &deployment.TargetInput{
			Name: "快速开始 本地 Docker", Platform: model.DeploymentDocker,
			EnvironmentID: environment.ID, RuntimeID: dockerengine.LocalEndpointID, RolloutTimeout: 300,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := service.EnsureInitialDeliverySettings(ctx)
	if err != nil || settings.Created {
		t.Fatalf("存在交付配置时不应重新初始化: settings=%+v err=%v", settings, err)
	}
	var current model.DeploymentPlan
	if err := db.Preload("DeploymentTarget").First(&current, "uuid = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.DeploymentTarget == nil || current.DeploymentTarget.RuntimeID != dockerengine.LocalEndpointID ||
		current.DeploymentTarget.HostID != model.BuiltinLocalHostID || current.DeploymentTarget.EnvironmentID != environment.ID {
		t.Fatalf("已有本地部署关联被改变: %+v", current)
	}
	var savedMembership model.EnvironmentHost
	if err := db.First(&savedMembership, "environment_id = ? AND host_id = ?", environment.ID, model.BuiltinLocalHostID).Error; err != nil {
		t.Fatalf("已有环境主机关系被删除: %v", err)
	}
}
