package projecttransfer

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

// 在真实迁移后的数据库中验证关系查询和 GORM JSON 序列化字段。
func TestDiscoverDependenciesUsesPersistedRelationsAndTaskConfig(t *testing.T) {
	db := testutil.NewDatabase(t)
	source, target := uuid.NewString(), uuid.NewString()
	seedTransferProjects(t, db, source, target)
	scope := db.WithContext(database.WithDepartmentScope(t.Context(), database.DepartmentScope{ProjectID: source, DepartmentID: database.DefaultDepartmentID}))

	repo := &model.GitRepository{ID: uuid.NewString(), Name: "源码"}
	app := &model.Application{ID: uuid.NewString(), Name: "sample", RepositoryID: repo.ID}
	registry := &model.ImageRegistry{ID: uuid.NewString(), Name: "镜像"}
	build := &model.BuildPlan{ID: uuid.NewString(), Name: "构建", ImageRegistryID: registry.ID}
	environment := &model.Environment{ID: uuid.NewString(), Name: "测试主机组"}
	host := &model.Host{ID: uuid.NewString(), Name: "执行主机"}
	cluster := &model.KubernetesCluster{ID: uuid.NewString(), Name: "部署集群"}
	overrideCluster := &model.KubernetesCluster{ID: uuid.NewString(), Name: "自定义集群"}
	targetRow := &model.DeploymentTarget{ID: uuid.NewString(), Name: "集群目标", Platform: model.DeploymentKubernetes, RuntimeID: cluster.ID, HostIDs: []string{host.ID}, EnvironmentID: environment.ID}
	configFile := &model.DeliveryVariable{ID: uuid.NewString(), Name: "DEPLOY_CONFIG", Kind: "env", ScopeKey: uuid.NewString(), ApplicationID: app.ID, Usage: "deploy"}
	buildVariable := &model.DeliveryVariable{ID: uuid.NewString(), Name: "BUILD_ARG", Kind: "text", ScopeKey: uuid.NewString(), ApplicationID: app.ID, Usage: "build"}
	scheduleChannel := &model.NotificationChannel{ID: uuid.NewString(), Name: "回调"}
	plan := &model.DeploymentPlan{ID: uuid.NewString(), Name: "部署", DeploymentTargetID: targetRow.ID,
		ExecutionConfig: &model.DeploymentExecutionConfig{ConfigurationFiles: []model.DeploymentConfigFile{{ID: configFile.ID}}, DeploymentRegistryID: registry.ID}}
	workflow := &model.ReleaseWorkflow{ID: uuid.NewString(), ApplicationID: app.ID, Name: "流水线",
		Stages: []model.WorkflowStage{{Tasks: []model.WorkflowNode{{Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: build.ID, VariableNames: []string{buildVariable.Name}}},
			{Type: model.WorkflowNodeDeploy, Disabled: true, Config: model.WorkflowNodeConfig{DeploymentPlanID: plan.ID, DeploymentOverride: &model.WorkflowDeploymentOverride{DeploymentTarget: &model.DeploymentTarget{RuntimeID: overrideCluster.ID}},
				ComposeSource: &model.WorkflowComposeSource{Kind: "configuration", ConfigurationID: configFile.ID}}}}}}}
	releasePlan := &model.ReleasePlan{ID: uuid.NewString(), Version: "test-release"}
	group := &model.ReleaseGroup{ID: uuid.NewString(), ReleasePlanID: releasePlan.ID, Name: "group"}
	schedulePayload, err := json.Marshal(map[string]string{"channel_id": scheduleChannel.ID})
	if err != nil {
		t.Fatal(err)
	}
	schedule := &model.ScheduledTask{ID: uuid.NewString(), Name: "通知回调", Action: model.ScheduleNotification, Payload: datatypes.JSON(schedulePayload)}
	for _, item := range []any{repo, app, registry, build, environment, host, cluster, overrideCluster, targetRow, configFile, buildVariable, scheduleChannel, plan, workflow, releasePlan, group, schedule,
		&model.EnvironmentHost{EnvironmentID: environment.ID, HostID: host.ID},
		&model.HostCapability{HostID: host.ID, Kind: model.HostCapabilityKubernetes, RuntimeID: cluster.ID},
		&model.ReleaseGroupApplication{ID: uuid.NewString(), ReleaseGroupID: group.ID, ApplicationID: app.ID},
	} {
		if err := scope.Create(item).Error; err != nil {
			t.Fatalf("准备资源 %T: %v", item, err)
		}
	}
	otherScope := db.WithContext(database.WithDepartmentScope(t.Context(), database.DepartmentScope{ProjectID: target, DepartmentID: database.DefaultDepartmentID}))
	otherBuild := &model.BuildPlan{ID: uuid.NewString(), Name: "另一个项目已存在的引用", ImageRegistryID: registry.ID}
	if err := otherScope.Create(otherBuild).Error; err != nil {
		t.Fatal(err)
	}
	// 当前配置不引用此版本的变量；运行仍按已冻结的真实 UUID 取值。
	frozenOnly := &model.DeliveryVariable{ID: uuid.NewString(), Name: "OLD_BUILD_ARG", Kind: "text", ScopeKey: uuid.NewString(), Usage: "build"}
	if err := scope.Create(frozenOnly).Error; err != nil {
		t.Fatal(err)
	}
	frozen, err := json.Marshal(map[string]any{"variable_bindings": map[string][]model.DeliveryVariableBinding{"build": {{VariableID: frozenOnly.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	run := &model.PipelineRun{ID: uuid.NewString(), ApplicationID: app.ID, WorkflowID: workflow.ID, Status: model.PipelineRunRunning, StatusWriteback: true, WorkflowSnapshot: string(frozen)}
	if err := scope.Create(run).Error; err != nil {
		t.Fatal(err)
	}
	if err := scope.Create(&model.PipelineRunRepository{ID: uuid.NewString(), PipelineRunID: run.ID, RepositoryID: repo.ID}).Error; err != nil {
		t.Fatal(err)
	}
	graph, err := discoverDependencies(scope, []string{source, target})
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]resourceKey{
		{{"application", app.ID}, {"repository", repo.ID}},
		{{"workflow", workflow.ID}, {"application", app.ID}},
		{{"workflow", workflow.ID}, {"build_plan", build.ID}},
		{{"workflow", workflow.ID}, {"delivery_variable", buildVariable.ID}},
		{{"workflow", workflow.ID}, {"deployment_plan", plan.ID}},
		{{"workflow", workflow.ID}, {"cluster", overrideCluster.ID}},
		{{"workflow", workflow.ID}, {"delivery_variable", configFile.ID}},
		{{"deployment_plan", plan.ID}, {"deployment_target", targetRow.ID}},
		{{"deployment_plan", plan.ID}, {"delivery_variable", configFile.ID}},
		{{"deployment_plan", plan.ID}, {"image_registry", registry.ID}},
		{{"deployment_target", targetRow.ID}, {"host", host.ID}},
		{{"deployment_target", targetRow.ID}, {"cluster", cluster.ID}},
		{{"environment", environment.ID}, {"host", host.ID}},
		{{"host", host.ID}, {"cluster", cluster.ID}},
		{{"release_plan", releasePlan.ID}, {"application", app.ID}},
		{{"scheduled_task", schedule.ID}, {"notification_channel", scheduleChannel.ID}},
		{{"build_plan", otherBuild.ID}, {"image_registry", registry.ID}},
		{{"active_pipeline_run", run.ID}, {"repository", repo.ID}},
		{{"active_pipeline_run", run.ID}, {"delivery_variable", frozenOnly.ID}},
	} {
		if !slices.Contains(graph[pair[0]], pair[1]) {
			t.Errorf("缺少依赖 %v → %v；实际 %v", pair[0], pair[1], graph[pair[0]])
		}
	}
	if err := scope.Delete(workflow).Error; err != nil {
		t.Fatal(err)
	}
	if err := scope.Model(run).Update("status", model.PipelineRunSucceeded).Error; err != nil {
		t.Fatal(err)
	}
	graph, err = discoverDependencies(scope, []string{source})
	if err != nil {
		t.Fatal(err)
	}
	if len(graph[resourceKey{"workflow", workflow.ID}]) != 0 || len(graph[resourceKey{"build_plan", otherBuild.ID}]) != 0 {
		t.Fatal("已归档配置和未指定项目不参与依赖检查")
	}
	terminalDeps := graph[resourceKey{"pending_repository_check", run.ID}]
	if !slices.Contains(terminalDeps, resourceKey{"repository", repo.ID}) || len(graph[resourceKey{"active_pipeline_run", run.ID}]) != 0 {
		t.Fatalf("已结束运行只保留未回写的仓库依赖，其他快照只供历史查看: %v", terminalDeps)
	}
	if err := scope.Model(run).Update("repository_check_state", "success").Error; err != nil {
		t.Fatal(err)
	}
	graph, err = discoverDependencies(scope, []string{source})
	if err != nil {
		t.Fatal(err)
	}
	if len(graph[resourceKey{"pending_repository_check", run.ID}]) != 0 {
		t.Fatal("已结束且无待回写的历史运行不应阻断移动")
	}
}

func seedTransferProjects(t *testing.T, db *gorm.DB, projects ...string) {
	t.Helper()
	for _, id := range projects {
		if err := db.Create(&model.Project{ID: id, DepartmentID: database.DefaultDepartmentID, Name: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
}
