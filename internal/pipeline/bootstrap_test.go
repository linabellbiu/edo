package pipeline

import (
	"context"
	"testing"

	"edo/internal/dockerengine"
	"edo/internal/model"
)

func TestEnsureInitialDeliverySettingsCreatesCompleteLocalDockerFlow(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()

	settings, err := service.EnsureInitialDeliverySettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Created || !settings.LocalDockerDeployment || settings.BuildPlanID == "" ||
		settings.DeploymentPlanID == "" || settings.WorkflowTemplateID == "" {
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
		len(template.Stages) != 2 || len(template.Stages[0].Tasks) != 1 || len(template.Stages[1].Tasks) != 1 ||
		template.Stages[0].Tasks[0].Type != model.WorkflowNodeBuild ||
		template.Stages[1].Tasks[0].Type != model.WorkflowNodeDeploy {
		t.Fatalf("默认流水线不是可直接使用的构建部署流程: result=%+v", result)
	}

	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "quick_start_app", RepositoryID: repositoryID,
		WorkflowTemplateID: settings.WorkflowTemplateID,
	})
	if err != nil {
		t.Fatalf("默认流水线无法用于新应用: %v", err)
	}
	if application.Workflow == nil || !application.Workflow.IsActive || len(application.Workflow.Stages) != 2 ||
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
		{name: "部署方案", value: &model.DeploymentPlan{}, want: 1},
		{name: "部署目标", value: &model.DeploymentTarget{}, want: 1},
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
	if !settings.Created || settings.LocalDockerDeployment || settings.DeploymentPlanID != "" {
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
	if err := service.DeleteDeploymentPlan(context.Background(), settings.DeploymentPlanID); err != nil {
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

func TestInitialDeliveryUsesBuiltinLocalDockerRuntime(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	settings, err := service.EnsureInitialDeliverySettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var plan model.DeploymentPlan
	if err := service.db.Preload("DeploymentTarget").First(&plan, "id = ?", settings.DeploymentPlanID).Error; err != nil {
		t.Fatal(err)
	}
	if plan.DeploymentTarget == nil || plan.DeploymentTarget.RuntimeID != dockerengine.LocalEndpointID ||
		plan.DeploymentTarget.WorkloadName != "" ||
		plan.DockerConfig.Network != "bridge" || plan.DockerConfig.RestartPolicy != "unless-stopped" {
		t.Fatalf("默认部署方案没有使用安全的本地 Docker 配置: %+v", plan)
	}
}
