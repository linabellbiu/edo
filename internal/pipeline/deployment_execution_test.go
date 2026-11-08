package pipeline

import (
	"context"
	"testing"
	"time"

	"edo/internal/model"
)

func TestDeploymentPlanOwnsExecutionAndFreezesNewRuns(t *testing.T) {
	s, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	build, err := s.CreateBuildPlan(ctx, "admin", BuildPlanInput{Name: "方案执行构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile"})
	if err != nil {
		t.Fatal(err)
	}
	target := deploymentPlanTargetInput(t, s, model.DeploymentTarget{Name: "方案执行目标", Platform: model.DeploymentDocker, EnvironmentID: "execution-env", HostID: "execution-host", RuntimeID: "execution-runtime", WorkloadName: "execution-app", RolloutTimeout: 120})
	input := DeploymentPlanInput{Name: "方案执行参数", Kind: model.DeploymentPlanDocker, DeploymentTarget: target, TimeoutSeconds: 120,
		ExecutionConfig: &model.DeploymentExecutionConfig{DeploymentInputVersion: 1, DeploymentScript: "printf plan-original", ArtifactBuildPlanID: build.ID, DeploymentTagReference: "${IMAGE_TAG}", EnvironmentVariables: map[string]string{"MODE": "plan"}}}
	plan, err := s.CreateDeploymentPlan(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	var stored model.DeploymentPlan
	if err = db.First(&stored, "uuid = ?", plan.ID).Error; err != nil || stored.ExecutionConfig == nil || stored.ExecutionConfig.DeploymentScript != "printf plan-original" {
		t.Fatal("方案未持久化执行参数", err)
	}
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "plan_execution", RepositoryID: repositoryID, PollIntervalSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	flow, err := s.GetWorkflow(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, stages := testStageWorkflowGraph(build.ID, plan.ID)
	var deployID string
	for i := range stages {
		for j := range stages[i].Tasks {
			node := &stages[i].Tasks[j]
			if node.Type == model.WorkflowNodeDeploy {
				deployID = node.ID
				node.Config.DeploymentInputVersion = 1
				node.Config.DeploymentScript = "printf legacy-ignored"
			}
		}
	}
	saved, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{SchemaVersion: model.WorkflowSchemaVersion, Name: flow.Workflow.Name, Revision: flow.Workflow.Revision, Activate: true, Source: source, Stages: stages})
	if err != nil {
		t.Fatalf("方案执行配置未参与正常校验: %v %+v", err, saved)
	}
	app, err = s.FindApplication(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.newResolvedWorkflowRun(ctx, app, app.Workflow, app.Workflow.Source, "manual", "refs/heads/main", "0123456789012345678901234567890123456789", "admin", "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	var frozen model.WorkflowNodeConfig
	for _, node := range workflowTasks(snapshot.Stages) {
		if node.ID == deployID {
			frozen = node.Config
		}
	}
	if frozen.DeploymentScript != "printf plan-original" || frozen.EnvironmentVariables["MODE"] != "plan" || frozen.ArtifactNodeID == "" || frozen.ArtifactBuildPlanID != "" {
		t.Fatalf("运行未冻结方案参数或前序构建绑定: %+v", frozen)
	}
	input.ExecutionConfig.DeploymentScript = "printf plan-next"
	if _, err = s.UpdateDeploymentPlan(ctx, plan.ID, input); err != nil {
		t.Fatal(err)
	}
	old, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range workflowTasks(old.Stages) {
		if node.ID == deployID && node.Config.DeploymentScript != "printf plan-original" {
			t.Fatal("方案修改污染原运行")
		}
	}
	next, err := s.newResolvedWorkflowRun(ctx, app, app.Workflow, app.Workflow.Source, "manual", "refs/heads/main", "0123456789012345678901234567890123456789", "admin", "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	nextSnapshot, err := parseWorkflowSnapshot(next)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range workflowTasks(nextSnapshot.Stages) {
		if node.ID == deployID && node.Config.DeploymentScript != "printf plan-next" {
			t.Fatal("新运行未采用当前方案")
		}
	}
}

func TestDeploymentExecutionLegacyAndExplicitClear(t *testing.T) {
	legacy := model.WorkflowNodeConfig{DeploymentScript: "printf legacy", VariableNames: []string{"ORIGINAL"}, Notifications: []model.WorkflowNotificationRule{{ID: "notice"}}, DeploymentPlanID: "plan"}
	model.ApplyDeploymentExecution(&legacy, nil)
	if legacy.DeploymentScript != "printf legacy" {
		t.Fatal("旧方案丢失任务配置")
	}
	model.ApplyDeploymentExecution(&legacy, &model.DeploymentExecutionConfig{})
	if legacy.DeploymentScript != "" || len(legacy.VariableNames) != 0 || len(legacy.Notifications) != 1 || legacy.DeploymentPlanID != "plan" {
		t.Fatal("明确清空复活旧参数或修改任务通知")
	}
	config := &model.DeploymentExecutionConfig{EnvironmentVariables: map[string]string{"MODE": "original"}, Compose: &model.ComposeDeploymentSettings{Services: []string{"api"}}}
	model.ApplyDeploymentExecution(&legacy, config)
	legacy.EnvironmentVariables["MODE"] = "changed"
	legacy.Compose.Services[0] = "changed"
	if config.EnvironmentVariables["MODE"] != "original" || config.Compose.Services[0] != "api" {
		t.Fatal("运行配置与方案共享可变内存")
	}
}

func TestDeploymentBuildBindingsRequireUniqueEarlierStage(t *testing.T) {
	build := model.WorkflowNode{ID: "current-build-uuid", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: "reusable-build-plan"}}
	for _, tc := range []struct {
		name   string
		stages []model.WorkflowStage
		valid  bool
	}{
		{"unique", []model.WorkflowStage{{Tasks: []model.WorkflowNode{build}}, {Tasks: []model.WorkflowNode{{ID: "deploy"}}}}, true},
		{"missing", []model.WorkflowStage{{Tasks: []model.WorkflowNode{{ID: "deploy"}}}}, false},
		{"same-stage", []model.WorkflowStage{{Tasks: []model.WorkflowNode{build, {ID: "deploy"}}}}, false},
		{"ambiguous", []model.WorkflowStage{{Tasks: []model.WorkflowNode{build, build}}, {Tasks: []model.WorkflowNode{{ID: "deploy"}}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := model.WorkflowNode{ID: "deploy", Config: model.WorkflowNodeConfig{DeploymentInputs: []model.DeploymentInput{{Kind: "upstream", Path: "output.tar.gz", ArtifactBuildPlanID: "reusable-build-plan"}}}}
			err := resolveDeploymentBuildBindings(&node, tc.stages)
			if (err == nil) != tc.valid {
				t.Fatalf("前序唯一绑定判断错误: %v", err)
			}
			if tc.valid && (node.Config.DeploymentInputs[0].ArtifactNodeID != build.ID || node.Config.DeploymentInputs[0].ArtifactBuildPlanID != "") {
				t.Fatal("没有固定当前任务身份")
			}
		})
	}
}

func TestDeploymentExecutionRejectsInvalidScriptAndReferences(t *testing.T) {
	for _, config := range []*model.DeploymentExecutionConfig{
		{DeploymentInputVersion: 1, DeploymentScript: "if true; then\n"},
		{DeploymentTagReference: "IMAGE_TAG"},
		{ArtifactBuildPlanID: "build", ArtifactNodeID: "node"},
		{DeploymentInputVersion: 1, DeploymentInputs: []model.DeploymentInput{{Kind: "upstream", Path: "../escape", ArtifactBuildPlanID: "build"}}},
		{DeploymentInputVersion: 1, DeploymentInputs: []model.DeploymentInput{{Kind: "upstream", Path: "output", ArtifactBuildPlanID: "build", ArtifactNodeID: "node"}}},
		{EnvironmentVariables: map[string]string{"IMAGE_TAG": "overridden"}},
	} {
		if normalizeDeploymentExecution(model.DeploymentPlanDocker, "", config) == nil {
			t.Fatal("无效方案执行参数通过校验")
		}
	}
}

func TestDeploymentPlanBuildReferencesProtectDraftPlans(t *testing.T) {
	s, _, _, _ := newPipelineTestService(t)
	ctx := context.Background()
	build, err := s.CreateBuildPlan(ctx, "admin", BuildPlanInput{Name: "引用保护构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile"})
	if err != nil {
		t.Fatal(err)
	}
	input := DeploymentPlanInput{Name: "引用保护部署", Kind: model.DeploymentPlanDocker, Reusable: true, ExecutionConfig: &model.DeploymentExecutionConfig{ArtifactBuildPlanID: build.ID}}
	plan, err := s.CreateDeploymentPlan(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteBuildPlan(ctx, build.ID); err != ErrBuildPlanInUse {
		t.Fatal("部署方案引用未阻止构建方案删除", err)
	}
	input.ExecutionConfig = &model.DeploymentExecutionConfig{}
	if _, err = s.UpdateDeploymentPlan(ctx, plan.ID, input); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteBuildPlan(ctx, build.ID); err != nil {
		t.Fatal("显式移除引用后仍无法删除", err)
	}
}

func TestComposePlanExecutionKeepsAutomaticArtifactSources(t *testing.T) {
	config := &model.DeploymentExecutionConfig{ArtifactNodeID: "old-node", ArtifactNodeIDs: []string{"old-node"}, ComposeImages: []model.ComposeImageBinding{{Service: "app", ArtifactNodeID: "old-node"}}, Compose: &model.ComposeDeploymentSettings{ProjectName: "project", TagVariable: "IMAGE_TAG"}}
	if err := normalizeDeploymentExecution(model.DeploymentPlanComposeStack, "services:\n  app:\n    image: registry.example.com/app:${IMAGE_TAG}\n", config); err != nil {
		t.Fatal(err)
	}
	if config.ArtifactNodeID != "" || len(config.ArtifactNodeIDs) != 0 || config.Compose.TagVariable != "${IMAGE_TAG}" {
		t.Fatal("旧的自动收集结果不应变成新约束")
	}
}
