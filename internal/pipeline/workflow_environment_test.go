package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"edo/internal/model"
)

func TestWorkflowDeploymentNodeUsesOnlyPlanAndResolvesItsTarget(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "方案目标快照构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "plan-owned-target", Name: "方案内的部署目标", Platform: model.DeploymentDocker,
		RuntimeID: "local", RolloutTimeout: 120,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "方案内聚合部署目标", Kind: model.DeploymentPlanDocker,
		DockerConfig: model.DockerContainerConfig{
			PortMappings: []model.DockerPortMapping{{HostPort: 18080, ContainerPort: 8080}},
			Network:      "bridge", Command: []string{"server", "--port", "8080"},
		},
		ServiceName: "api", TimeoutSeconds: 120, DeploymentTarget: deploymentPlanTargetInput(t, service, target),
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "target_snapshot", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, stages := testStageWorkflowGraph(buildPlan.ID, deploymentPlan.ID)
	saved, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name,
		Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: stages,
	})
	if err != nil || !saved.Valid {
		t.Fatalf("只选部署方案的阶段式流水线未能启用: result=%+v err=%v", saved, err)
	}
	deployTasks := workflowTasks(saved.Workflow.Stages)
	deploy := deployTasks[len(deployTasks)-1]
	if deploy.Config.DeploymentPlanID != deploymentPlan.ID {
		t.Fatalf("部署节点保留了不应由流水线配置的位置字段: %+v", deploy.Config)
	}
	application, err = service.FindApplication(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.newResolvedWorkflowRun(
		ctx, application, application.Workflow, application.Workflow.Source, "push", "refs/heads/main",
		"0123456789012345678901234567890123456789", "system", "", now,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	resolvedTasks := workflowTasks(snapshot.Stages)
	resolved := resolvedTasks[len(resolvedTasks)-1]
	resolvedTarget := snapshot.DeploymentTargets[resolved.ID]
	if resolvedTarget.ID != deploymentPlan.DeploymentTargetID {
		t.Fatalf("运行快照没有从部署方案解析不可变目标: node=%+v targets=%+v", resolved, snapshot.DeploymentTargets)
	}
	if resolvedTarget.WorkloadName == "" || !strings.HasPrefix(resolvedTarget.WorkloadName, application.Name+"-") {
		t.Fatalf("运行快照没有固定自动生成的 Docker 容器名称: %+v", resolvedTarget)
	}
	config := snapshot.DeploymentPlans[resolved.ID].DockerConfig
	if len(config.PortMappings) != 1 || config.PortMappings[0].HostPort != 18080 || config.Network != "bridge" || len(config.Command) != 3 {
		t.Fatalf("运行快照没有保存 Docker 启动配置: %+v", config)
	}
	if _, err := service.UpdateBuildPlan(ctx, buildPlan.ID, BuildPlanInput{
		Name: buildPlan.Name, Kind: model.BuildPlanScript, Script: "printf artifact > output",
		ArtifactPath: "output", TimeoutSeconds: 120,
	}); err != nil {
		t.Fatalf("修改被引用的构建方案失败: %v", err)
	}
	application, err = service.FindApplication(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.newResolvedWorkflowRun(
		ctx, application, application.Workflow, application.Workflow.Source, "push", "refs/heads/main",
		"1123456789012345678901234567890123456789", "system", "", now,
	); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("方案修改破坏制品链后仍创建流水线运行: %v", err)
	}
}

func TestShellTaskScriptBytesRemainUnchangedInSavedAndRunSnapshots(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "script_snapshot", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	const script = "  printf 'first'  \n\nprintf 'second'\n  "
	source := model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源", Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"manual"}}}
	stages := []model.WorkflowStage{{ID: "build", Name: "构建", Tasks: []model.WorkflowNode{
		{ID: "shell", Type: model.WorkflowNodeShell, Name: "执行脚本", Config: model.WorkflowNodeConfig{
			Script: script, WorkingDirectory: ".", TimeoutSeconds: 60,
		}},
	}}}
	saved, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name,
		Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: stages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Workflow.Stages[0].Tasks[0].Config.Script != script {
		t.Fatalf("保存流水线改写了脚本字节: %q", saved.Workflow.Stages[0].Tasks[0].Config.Script)
	}
	if saved.Workflow.Stages[0].Tasks[0].Config.RuntimeImage != model.DefaultRuntimeImage {
		t.Fatalf("Shell 任务未补齐默认运行镜像: %+v", saved.Workflow.Stages[0].Tasks[0].Config)
	}
	application, err = service.FindApplication(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.newResolvedWorkflowRun(
		ctx, application, application.Workflow, application.Workflow.Source, "manual",
		"refs/heads/main", "0123456789012345678901234567890123456789", "admin", "", time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Stages[0].Tasks[0].Config.Script != script {
		t.Fatalf("运行快照改写了脚本字节: %q", snapshot.Stages[0].Tasks[0].Config.Script)
	}
	if snapshot.Stages[0].Tasks[0].Config.RuntimeImage != model.DefaultRuntimeImage {
		t.Fatalf("运行快照缺少固定运行镜像: %+v", snapshot.Stages[0].Tasks[0].Config)
	}
}

func TestShellTaskRejectsFloatingRuntimeImage(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "shell_runtime_validation", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := service.GetWorkflow(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源", Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"manual"}}}
	stages := []model.WorkflowStage{{ID: "test", Name: "测试", Tasks: []model.WorkflowNode{{
		ID: "shell", Type: model.WorkflowNodeShell, Name: "执行脚本", Config: model.WorkflowNodeConfig{
			Script: "echo ok", RuntimeImage: "alpine:latest", WorkingDirectory: ".", TimeoutSeconds: 60,
		},
	}}}}
	result, err := service.ValidateWorkflow(context.Background(), application.ID, WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name,
		Revision: workflow.Workflow.Revision, Source: source, Stages: stages,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range result.Issues {
		if issue.Code == "invalid_shell_task" {
			return
		}
	}
	t.Fatalf("浮动 Shell 运行镜像未被流水线校验拒绝: %+v", result.Issues)
}

func TestStageWorkflowValidatesDeploymentPlanOwnedTarget(t *testing.T) {
	service, db, _, _ := newPipelineTestService(t)
	ctx := context.Background()
	fileBuildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "部署方案校验构建", Kind: model.BuildPlanScript,
		Script: "mkdir -p dist && printf ready > dist/app", ArtifactPath: "dist",
	})
	if err != nil {
		t.Fatal(err)
	}
	imageBuildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "部署方案镜像构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	targets := []model.DeploymentTarget{
		{ID: "stage-ssh-target", Name: "SSH 目标", Platform: model.DeploymentSSH, HostID: "host-1", IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: "stage-docker-target", Name: "Docker 目标", Platform: model.DeploymentDocker, RuntimeID: "docker-1", WorkloadName: "api", IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: "stage-disabled-target", Name: "已停用目标", Platform: model.DeploymentSSH, HostID: "host-2", IsActive: false, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&targets).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.DeploymentTarget{}).Where("id = ?", targets[2].ID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	plans := []model.DeploymentPlan{
		{ID: "stage-valid-plan", Name: "有效 SSH 方案", Kind: model.DeploymentPlanScript, DeploymentTargetID: targets[0].ID, Script: "echo deploy", TimeoutSeconds: 120, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: "stage-missing-target-plan", Name: "缺少目标的方案", Kind: model.DeploymentPlanScript, Script: "echo deploy", TimeoutSeconds: 120, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: "stage-disabled-target-plan", Name: "目标已停用的方案", Kind: model.DeploymentPlanScript, DeploymentTargetID: targets[2].ID, Script: "echo deploy", TimeoutSeconds: 120, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: "stage-mismatch-plan", Name: "目标错配的方案", Kind: model.DeploymentPlanScript, DeploymentTargetID: targets[1].ID, Script: "echo deploy", TimeoutSeconds: 120, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: "stage-valid-docker-plan", Name: "有效 Docker 方案", Kind: model.DeploymentPlanDocker, DeploymentTargetID: targets[1].ID, ServiceName: "api", TimeoutSeconds: 120, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&plans).Error; err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		buildPlanID string
		planID      string
		issue       string
	}{
		{name: "有效文件制品方案", buildPlanID: fileBuildPlan.ID, planID: plans[0].ID},
		{name: "有效镜像制品方案", buildPlanID: imageBuildPlan.ID, planID: plans[4].ID},
		{name: "镜像不能交给主机脚本", buildPlanID: imageBuildPlan.ID, planID: plans[0].ID, issue: "artifact_kind_mismatch"},
		{name: "文件不能交给 Docker", buildPlanID: fileBuildPlan.ID, planID: plans[4].ID, issue: "artifact_kind_mismatch"},
		{name: "方案缺少目标", buildPlanID: fileBuildPlan.ID, planID: plans[1].ID, issue: "missing_deployment_target"},
		{name: "方案目标已停用", buildPlanID: fileBuildPlan.ID, planID: plans[2].ID, issue: "missing_deployment_target"},
		{name: "方案与目标类型错配", buildPlanID: fileBuildPlan.ID, planID: plans[3].ID, issue: "deployment_plan_target_mismatch"},
		{name: "方案不存在", buildPlanID: fileBuildPlan.ID, planID: "missing-plan", issue: "missing_deployment_plan"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, stages := testStageWorkflowGraph(test.buildPlanID, test.planID)
			issues := service.validateWorkflow(ctx, &model.Application{}, model.WorkflowSchemaVersion, source, stages)
			if test.issue == "" {
				if len(issues) != 0 {
					t.Fatalf("有效阶段式流水线被拒绝: %+v", issues)
				}
				return
			}
			if !hasWorkflowIssue(issues, test.issue) {
				t.Fatalf("缺少预期问题 %s: %+v", test.issue, issues)
			}
		})
	}

	source, stages := testStageWorkflowGraph(fileBuildPlan.ID, plans[0].ID)
	stages[0].Tasks = nil
	if issues := service.validateWorkflow(ctx, &model.Application{}, model.WorkflowSchemaVersion, source, stages); !hasWorkflowIssue(issues, "missing_artifact_source") {
		t.Fatalf("部署前缺少构建任务时未被拒绝: %+v", issues)
	}

	source, stages = testStageWorkflowGraph("missing-build-plan", plans[0].ID)
	issues := service.validateWorkflow(ctx, &model.Application{}, model.WorkflowSchemaVersion, source, stages)
	if !hasWorkflowIssue(issues, "missing_build_plan") {
		t.Fatalf("无效构建方案未被拒绝: %+v", issues)
	}
	if hasWorkflowIssue(issues, "missing_artifact_source") {
		t.Fatalf("构建任务已存在时不应误报缺少构建任务: %+v", issues)
	}
}

func TestStageWorkflowRejectsDeployAfterScriptBuildWithoutArtifact(t *testing.T) {
	service, db, _, _ := newPipelineTestService(t)
	ctx := context.Background()
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "不保存产物构建", Kind: model.BuildPlanScript, Script: "echo test",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "no-artifact-target", Name: "无产物目标", Platform: model.DeploymentSSH,
		HostID: "host-1", IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	plan := model.DeploymentPlan{
		ID: "no-artifact-deploy", Name: "无产物部署", Kind: model.DeploymentPlanScript,
		DeploymentTargetID: target.ID, Script: "echo deploy", TimeoutSeconds: 120,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	source, stages := testStageWorkflowGraph(buildPlan.ID, plan.ID)
	issues := service.validateWorkflow(ctx, &model.Application{}, model.WorkflowSchemaVersion, source, stages)
	if !hasWorkflowIssue(issues, "artifact_not_saved") {
		t.Fatalf("未保存文件制品的 Shell 构建仍可连接部署任务: %+v", issues)
	}
}

func TestWorkflowDraftAllowsMissingDeploymentPlanButCannotActivate(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "草稿构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile",
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "draft_app", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, stages := testStageWorkflowGraph(buildPlan.ID, "")
	draft, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name,
		Revision: workflow.Workflow.Revision, Source: source, Stages: stages,
	})
	if err != nil || draft.Valid || !hasWorkflowIssue(draft.Issues, "missing_deployment_plan") {
		t.Fatalf("未完成的部署任务草稿应当可保存并返回明确问题: result=%+v err=%v", draft, err)
	}
	_, err = service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: draft.Workflow.Name,
		Revision: draft.Workflow.Revision, Activate: true,
		Source: draft.Workflow.Source, Stages: draft.Workflow.Stages,
	})
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("缺少部署方案的流水线仍可启用: %v", err)
	}
}
