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

func TestGitTagFallbackIsConsistentAcrossBuildAndDeployment(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, tc := range []struct{ name, ref, want string }{
		{"Tag", "refs/tags/v2.3.0", "v2.3.0"},
		{"分支", "refs/heads/main", commit[:12]},
		{"PR", "refs/pull/12/head", commit[:12]},
		{"固定提交", commit, commit[:12]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := model.PipelineRun{ID: "run", Ref: tc.ref, CommitSHA: commit}
			prepared := &executionContext{run: run, node: model.WorkflowNode{Config: model.WorkflowNodeConfig{ImageTag: "${EDO_GIT_TAG}"}}, variables: map[string]string{"EDO_GIT_TAG": "cannot-override", "EDO_COMMIT_SHA": "cannot-override"}}
			tag, err := executionNodeImageTag(prepared)
			if err != nil || tag != tc.want {
				t.Fatalf("镜像 Tag 未按 Git Tag / Commit 解析: %q %v", tag, err)
			}
			build := pipelineCommandEnvironment(&buildExecutionContext{run: run})
			deploy, err := deploymentScriptEnvironment(prepared)
			if err != nil {
				t.Fatal(err)
			}
			if err := (&Service{}).addDeploymentPipelineVariables(context.Background(), prepared); err != nil {
				t.Fatal(err)
			}
			for _, values := range []map[string]string{build, deploy, prepared.variables} {
				if values["EDO_GIT_TAG"] != tc.want || values["EDO_COMMIT_SHA"] != commit[:12] || values["EDO_GIT_REF"] != tc.ref {
					t.Fatalf("执行上下文中的版本变量不一致: %+v", values)
				}
			}
			prepared.node.Config.ImageTag = "user-defined"
			if tag, err := executionNodeImageTag(prepared); err != nil || tag != "user-defined" {
				t.Fatalf("用户配置的 Tag 被默认规则覆盖: %q %v", tag, err)
			}
		})
	}
	if tag, err := executionNodeImageTag(&executionContext{node: model.WorkflowNode{Config: model.WorkflowNodeConfig{ImageTag: "${EDO_GIT_TAG}"}}}); err == nil || tag != "" {
		t.Fatal("没有 Tag 或 Commit 时不能编造镜像版本", tag, err)
	}
}

func TestDefaultGitTagAllowsBranchWorkflowRun(t *testing.T) {
	s, _, _, repo := newPipelineTestService(t)
	ctx := context.Background()
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "branch_image", RepositoryID: repo})
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
	source := model.WorkflowNode{ID: "source", Name: "代码源", Type: model.WorkflowNodeTrigger, Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"manual", "push"}}}
	saved, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name, Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: []model.WorkflowStage{{ID: "build", Name: "构建", Tasks: []model.WorkflowNode{{ID: "image", Name: "镜像构建", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: plan.ID}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("b", 40)
	run, err := s.newResolvedWorkflowRun(ctx, app, saved.Workflow, source, "push", "refs/heads/main", commit, "admin", "", time.Now())
	if err != nil {
		t.Fatalf("默认镜像 Tag 阻断了正常分支运行: %v", err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	node := snapshot.Stages[0].Tasks[0]
	tag, err := executionNodeImageTag(&executionContext{run: *run, node: node, application: *app})
	if err != nil || tag != commit[:12] || run.CommitSHA != commit || run.Ref != "refs/heads/main" || node.Config.ImageTag != "${EDO_GIT_TAG}" {
		t.Fatalf("默认版本解析错误或原始版本引用被改写: %q %+v %v", tag, node.Config, err)
	}
	if saved.Workflow.Stages[0].Tasks[0].Config.ImageTag != "" {
		t.Fatal("自动版本不应回写用户工作流")
	}
}

func TestGitTagFallbackReachesRealWorkspaceShell(t *testing.T) {
	commit := strings.Repeat("c", 40)
	for _, ref := range []string{"refs/heads/main", "refs/tags/v3.1.0"} {
		t.Run(ref, func(t *testing.T) {
			run := model.PipelineRun{Ref: ref, CommitSHA: commit}
			var output bytes.Buffer
			result, err := runWorkspaceBuild(context.Background(), dockerengine.ScriptContainerInput{
				SourceDirectory: t.TempDir(), Timeout: 30 * time.Second,
				Script:            "printf '%s|%s|%s' \"$EDO_GIT_TAG\" \"$EDO_COMMIT_SHA\" \"$EDO_GIT_REF\"\n",
				SystemEnvironment: pipelineCommandEnvironment(&buildExecutionContext{run: run}),
				Stdout:            &output, Stderr: &output,
			})
			want := commit[:12]
			if ref == "refs/tags/v3.1.0" {
				want = "v3.1.0"
			}
			if err != nil || result.ExitCode != 0 || output.String() != want+"|"+commit[:12]+"|"+ref {
				t.Fatalf("真实脚本收到的版本不正确: %q %+v %v", output.String(), result, err)
			}
		})
	}
}

func TestCommitVariableUsesShortHashWithoutChangingRunIdentity(t *testing.T) {
	for _, tc := range []struct{ commit, want string }{
		{"0123456789abcdef0123456789abcdef01234567", "0123456789ab"},
		{strings.Repeat("a", 64), strings.Repeat("a", 12)},
		{"0123456789ab", "0123456789ab"},
		{"01234567", "01234567"},
		{"", ""},
	} {
		run := model.PipelineRun{Ref: "refs/heads/main", CommitSHA: tc.commit}
		prepared := &executionContext{run: run}
		deploy, err := deploymentScriptEnvironment(prepared)
		if err != nil {
			t.Fatal(err)
		}
		build := pipelineCommandEnvironment(&buildExecutionContext{run: run})
		for _, got := range []string{build["EDO_COMMIT_SHA"], deploy["EDO_COMMIT_SHA"], expandPipelineImageVariable("${EDO_COMMIT_SHA}", run, model.Application{}), expandPipelineBuildArgument("${EDO_COMMIT_SHA}", run), pipelineGitTag(run)} {
			if got != tc.want {
				t.Fatalf("内置 Commit 未按 12 位短哈希统一提供: 输入=%q 输出=%q 预期=%q", tc.commit, got, tc.want)
			}
		}
		if run.CommitSHA != tc.commit || prepared.run.CommitSHA != tc.commit {
			t.Fatal("内置变量转换改写了原始运行身份")
		}
	}
}
