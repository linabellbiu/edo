package pipeline

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"edo/internal/dockerengine"
	"edo/internal/model"
)

func TestAutomaticImageTagFreezesDefaultAndConfiguredNames(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, ref := range []string{"refs/tags/v2.0.0", "refs/heads/main"} {
		for _, name := range []string{"", "custom/api", "${EDO_APPLICATION_NAME}/api"} {
			t.Run(ref+"/"+name, func(t *testing.T) {
				s, db, _, repo := newPipelineTestService(t)
				ctx := context.Background()
				app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "auto_image", RepositoryID: repo})
				if err != nil {
					t.Fatal(err)
				}
				plan, err := s.CreateBuildPlan(ctx, "admin", BuildPlanInput{Name: "镜像构建", Kind: model.BuildPlanDockerfile})
				if err != nil {
					t.Fatal(err)
				}
				workflow, err := s.GetWorkflow(ctx, app.ID)
				if err != nil {
					t.Fatal(err)
				}
				source := model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源", Config: model.WorkflowNodeConfig{Events: []string{"manual"}, Branch: "main"}}
				task := model.WorkflowNode{ID: "image", Name: "镜像构建", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: plan.ID, ImageName: name}}
				saved, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{Name: workflow.Workflow.Name, Revision: workflow.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Activate: true, Source: source, Stages: []model.WorkflowStage{{ID: "build", Name: "构建", Tasks: []model.WorkflowNode{task}}}})
				if err != nil {
					t.Fatal(err)
				}
				run, err := s.newResolvedWorkflowRun(ctx, app, saved.Workflow, source, "manual", ref, commit, "admin", "", time.Now())
				if err != nil {
					t.Fatal(err)
				}
				snapshot, err := parseWorkflowSnapshot(run)
				if err != nil {
					t.Fatal(err)
				}
				node := snapshot.Stages[0].Tasks[0]
				wantName := strings.ReplaceAll(name, "${EDO_APPLICATION_NAME}", app.Name)
				if name == "" {
					wantName = app.Name
				}
				wantTag := commit[:12]
				if strings.HasPrefix(ref, "refs/tags/") {
					wantTag = "v2.0.0"
				}
				if node.Config.ImageName != wantName || run.CommitSHA != commit {
					t.Fatal("最终名称未冻结或完整 Commit 被截断")
				}
				// 构建启动前应用改名，不得改变这次已经确定的镜像名称。
				app.Name = "renamed_application"
				run.NodeExecution = true
				prepared := &executionContext{run: *run, node: node, application: *app, registry: model.ImageRegistry{Endpoint: "https://registry.example.com", Namespace: "team"}}
				image, _, err := s.executionImage(ctx, prepared)
				if err != nil || image != "registry.example.com/team/"+wantName+":"+wantTag {
					t.Fatal("仓库镜像地址未采用实际名称与自动版本", image, err)
				}
				local, err := localExecutionImage(prepared)
				if err != nil || local != "edo.local/"+wantName+":"+wantTag {
					t.Fatal("本地镜像与仓库镜像的名称或版本不一致", local, err)
				}
				environment := pipelineCommandEnvironment(&buildExecutionContext{run: *run, node: node, application: *app, plan: snapshot.BuildPlans[node.ID]})
				if environment["EDO_IMAGE_TAG"] != wantTag || expandPipelineBuildArgument("${EDO_IMAGE_TAG}", *run) != wantTag {
					t.Fatal("构建环境或参数中的内置版本与实际输出不一致")
				}
				var stored model.ReleaseWorkflow
				if err := db.First(&stored, "uuid = ?", saved.Workflow.ID).Error; err != nil {
					t.Fatal(err)
				}
				if stored.Stages[0].Tasks[0].Config.ImageName != name || stored.Stages[0].Tasks[0].Config.ImageTag != "" {
					t.Fatal("默认值被回写到用户工作流")
				}
				next, err := s.newResolvedWorkflowRun(ctx, app, saved.Workflow, source, "manual", ref, commit, "admin", "", time.Now())
				if err != nil {
					t.Fatal(err)
				}
				nextSnapshot, err := parseWorkflowSnapshot(next)
				nextName := strings.ReplaceAll(name, "${EDO_APPLICATION_NAME}", app.Name)
				if name == "" {
					nextName = app.Name
				}
				if err != nil || nextSnapshot.Stages[0].Tasks[0].Config.ImageName != nextName || snapshot.Stages[0].Tasks[0].Config.ImageName != wantName {
					t.Fatal("下一次运行未采用新名称或修改了旧运行", err)
				}
			})
		}
	}
}

func TestBuiltinImageTagReachesWorkspaceAndKeepsFrozenVersions(t *testing.T) {
	for _, ref := range []string{"refs/tags/v2.0.0", "refs/heads/main"} {
		run := model.PipelineRun{Ref: ref, CommitSHA: strings.Repeat("b", 40)}
		var output bytes.Buffer
		_, err := runWorkspaceBuild(t.Context(), dockerengine.ScriptContainerInput{SourceDirectory: t.TempDir(), Timeout: 30 * time.Second, Script: "printf '%s' \"${EDO_IMAGE_TAG}\"", SystemEnvironment: pipelineCommandEnvironment(&buildExecutionContext{run: run}), Stdout: &output, Stderr: &output})
		if err != nil || output.String() != pipelineGitTag(run) {
			t.Fatal("工作区脚本未取得自动镜像版本", err)
		}
	}
	prepared := &buildExecutionContext{run: model.PipelineRun{Ref: "refs/tags/current", CommitSHA: strings.Repeat("c", 40)}, node: model.WorkflowNode{Config: model.WorkflowNodeConfig{ImageTag: "frozen-version"}}, plan: workflowBuildPlanSnapshot{Kind: model.BuildPlanDockerfile}}
	if pipelineCommandEnvironment(prepared)["EDO_IMAGE_TAG"] != "frozen-version" || validScriptEnvironmentVariables(map[string]string{"EDO_IMAGE_TAG": "override"}) {
		t.Fatal("历史版本被改变或用户变量可以覆盖内置版本")
	}
}

func TestBuiltinImageTagAvailableToDeploymentWithoutImageBuild(t *testing.T) {
	for _, ref := range []string{"refs/tags/v2.0.0", "refs/heads/main"} {
		prepared := &executionContext{run: model.PipelineRun{Ref: ref, CommitSHA: strings.Repeat("d", 40)}, node: model.WorkflowNode{ID: "deploy"}, deploymentPlan: model.DeploymentPlan{ComposeYAML: "services:\n  api:\n    image: registry.example.com/team/api:${EDO_IMAGE_TAG}\n"}, variables: map[string]string{"EDO_IMAGE_TAG": "user-override"}}
		if err := (&Service{}).addDeploymentPipelineVariables(t.Context(), prepared); err != nil || prepared.variables["EDO_IMAGE_TAG"] != pipelineGitTag(prepared.run) {
			t.Fatal("仅部署流水线缺少内置 Tag 或允许用户覆盖", err)
		}
	}
}
