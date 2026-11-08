package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"edo/internal/model"
)

func TestUpstreamEnvironmentReferenceResolvesDeclaredOutputBeforeExecution(t *testing.T) {
	build := model.WorkflowNode{ID: "build", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildOutputs: []model.WorkflowBuildOutput{{Name: "BUILD_ARTIFACT", Paths: []string{"output"}}}}}
	deploy := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentInputVersion: 1, DeploymentInputs: []model.DeploymentInput{{Kind: "upstream", ArtifactReference: "${BUILD_ARTIFACT}", Path: "output.tar.gz"}}, ComposeSource: &model.WorkflowComposeSource{Kind: "upstream", ArtifactReference: "${BUILD_ARTIFACT}", Path: "compose.yaml"}}}
	stages := []model.WorkflowStage{{ID: "build-stage", Tasks: []model.WorkflowNode{build}}, {ID: "deploy-stage", Tasks: []model.WorkflowNode{deploy}}}
	if err := sanitizeDeploymentInputs(&deploy); err != nil {
		t.Fatal("尚未执行的命名产物引用被拒绝", err)
	}
	if !validComposeSource(deploy.Config.ComposeSource) {
		t.Fatal("Compose 不接受命名产物引用")
	}
	if err := resolveDeploymentBuildBindings(&deploy, stages); err != nil {
		t.Fatal(err)
	}
	input := deploy.Config.DeploymentInputs[0]
	if input.ArtifactNodeID != build.ID || input.ArtifactName != "BUILD_ARTIFACT" || input.ArtifactReference != "${BUILD_ARTIFACT}" || deploy.Config.ComposeSource.ArtifactNodeID != build.ID {
		t.Fatal("变量引用没有解析到准确的前序产物", input)
	}
	for _, invalid := range []string{"BUILD_ARTIFACT", "${EDO_IMAGE_TAG}", "${MISSING}", "${BUILD_ARTIFACT:-fallback}", "$(id)"} {
		if _, _, err := resolveUpstreamOutput(deploy.ID, invalid, stages); err == nil {
			t.Fatalf("无效或缺失的产物变量未拒绝: %s", invalid)
		}
	}
	duplicate := build
	duplicate.ID = "another-build"
	stages[0].Tasks = append(stages[0].Tasks, duplicate)
	if _, _, err := resolveUpstreamOutput(deploy.ID, "${BUILD_ARTIFACT}", stages); err == nil {
		t.Fatal("多个同名产物被静默选取")
	}
	for _, invalidStages := range [][]model.WorkflowStage{{{Tasks: []model.WorkflowNode{build, deploy}}}, {{Tasks: []model.WorkflowNode{deploy}}, {Tasks: []model.WorkflowNode{build}}}} {
		if _, _, err := resolveUpstreamOutput(deploy.ID, "${BUILD_ARTIFACT}", invalidStages); err == nil {
			t.Fatal("读取了同阶段或后序阶段的构建物")
		}
	}
}

func TestNamedOutputReferenceSavesAndFreezesWithWorkflow(t *testing.T) {
	s, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "named_reference", RepositoryID: repositoryID})
	if err != nil {
		t.Fatal(err)
	}
	flow, err := s.GetWorkflow(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := deploymentPlanTargetInput(t, s, model.DeploymentTarget{Name: "产物变量测试主机", Platform: model.DeploymentSSH, EnvironmentID: "output-env", HostID: "output-host", WorkingDirectory: "/srv/example", RolloutTimeout: 120})
	build := model.WorkflowNode{ID: "build", Type: model.WorkflowNodeBuild, Name: "自定义构建", Config: model.WorkflowNodeConfig{InlineBuild: &model.BuildPlan{Kind: model.BuildPlanScript, Script: "mkdir -p output", TimeoutSeconds: 120}, BuildOutputs: []model.WorkflowBuildOutput{{Name: "BUILD_ARTIFACT", Paths: []string{"output"}}}}}
	deploy := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Name: "自定义部署", Config: model.WorkflowNodeConfig{DeploymentSource: "inline", InlineDeploymentPlan: &model.WorkflowDeploymentPlan{Name: "自定义部署", Kind: model.DeploymentPlanScript, Script: "printf deployed", TimeoutSeconds: 120, DeploymentTarget: &model.DeploymentTarget{Platform: target.Platform, EnvironmentID: target.EnvironmentID, HostID: target.HostID, WorkingDirectory: target.WorkingDirectory, RolloutTimeout: target.RolloutTimeout}, ExecutionConfig: &model.DeploymentExecutionConfig{DeploymentInputVersion: 1, DeploymentInputs: []model.DeploymentInput{{Kind: "upstream", ArtifactReference: "${BUILD_ARTIFACT}", Path: "output.tar.gz"}}}}}}
	source := model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源", Config: model.WorkflowNodeConfig{Events: []string{"manual"}}}
	saved, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{SchemaVersion: model.WorkflowSchemaVersion, Name: flow.Workflow.Name, Revision: flow.Workflow.Revision, Activate: true, Source: source, Stages: []model.WorkflowStage{{ID: "build-stage", Name: "构建", Tasks: []model.WorkflowNode{build}}, {ID: "deploy-stage", Name: "部署", Tasks: []model.WorkflowNode{deploy}}}})
	if err != nil || !saved.Valid {
		t.Fatal("产物变量引用无法正常保存启用", err, saved)
	}
	run, err := s.newResolvedWorkflowRun(ctx, app, saved.Workflow, source, "manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", time.Now())
	if err != nil {
		t.Fatal("产物变量引用无法创建运行快照", err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	input := snapshot.Stages[1].Tasks[0].Config.DeploymentInputs[0]
	if input.ArtifactReference != "${BUILD_ARTIFACT}" || input.ArtifactName != "BUILD_ARTIFACT" || input.ArtifactNodeID != build.ID {
		t.Fatal("运行快照没有冻结准确产物身份", input)
	}
}
