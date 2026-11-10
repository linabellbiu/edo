package pipeline

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"edo/internal/model"
)

func TestBuildTargetListValidation(t *testing.T) {
	for _, value := range []string{",api", "web,", "web,,api", "web,WEB", "web,$(id)", "web,../api", strings.Repeat("a", 129), strings.Join(strings.Fields("a b c d e f g h i j k l m n o p q"), ",")} {
		if _, err := normalizeBuildPlanInput(BuildPlanInput{Name: "构建", Kind: model.BuildPlanDockerfile, TargetStage: value}); !errors.Is(err, ErrInvalidBuildPlan) {
			t.Fatalf("接受了无效 target %q: %v", value, err)
		}
	}
	service, mock := newPipelineMockService(t)
	expectBuildPlanInsert(mock)
	plan, err := service.CreateBuildPlan(context.Background(), "admin", BuildPlanInput{Name: "多阶段构建", Kind: model.BuildPlanDockerfile, TargetStage: " web, api "})
	if err != nil || plan.TargetStage != "web,api" {
		t.Fatalf("列表未按输入顺序规范化保存: plan=%+v err=%v", plan, err)
	}
}

func TestBuildTargetsExpandFreezeImagesAndComposeSources(t *testing.T) {
	inline := &model.BuildPlan{Kind: model.BuildPlanDockerfile, TargetStage: "web,api"}
	original := []model.WorkflowStage{{ID: "build-stage", Tasks: []model.WorkflowNode{{ID: "build", Name: "镜像构建", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: "plan", InlineBuild: inline, ImageName: "ignored-for-multiple", ImageTag: "${EDO_GIT_TAG}"}}}}, {ID: "deploy-stage", Tasks: []model.WorkflowNode{{ID: "deploy", Type: model.WorkflowNodeDeploy}}}}
	workflow := model.ReleaseWorkflow{Stages: cloneWorkflowStages(original)}
	plans := map[string]workflowBuildPlanSnapshot{"build": {ID: "plan", Kind: model.BuildPlanDockerfile, TargetStage: "web,api", ImageRegistryID: "registry"}}
	bindings := map[string][]model.DeliveryVariableBinding{"build": {{Name: "BUILD_TOKEN", IsSecret: true}}}
	if err := expandBuildTargets(&workflow, plans, bindings); err != nil {
		t.Fatal(err)
	}
	nodes := workflow.Stages[0].Tasks
	if len(nodes) != 2 || nodes[0].ID != "build" || nodes[0].ID == nodes[1].ID {
		t.Fatalf("target 任务身份无效: %+v", nodes)
	}
	run := model.PipelineRun{ID: "run", Ref: "refs/tags/v2", CommitSHA: strings.Repeat("a", 40)}
	for i, target := range []string{"web", "api"} {
		node := nodes[i]
		if plans[node.ID].TargetStage != target || node.Config.InlineBuild.TargetStage != target || node.Config.ImageName != target || !reflect.DeepEqual(bindings[node.ID], bindings["build"]) {
			t.Fatalf("target 参数、名称或变量绑定丢失: %+v %+v", node, plans[node.ID])
		}
		image, err := localExecutionImage(&executionContext{run: run, node: node})
		if err != nil || image != "edo.local/"+target+":v2" {
			t.Fatalf("target 镜像或共享 Tag 错误: %q %v", image, err)
		}
	}
	autoComposeArtifactSources(workflow.Stages, plans, map[string]workflowDeploymentPlanSnapshot{"deploy": {Kind: model.DeploymentPlanComposeStack}})
	if got := workflow.Stages[1].Tasks[0].Config.ArtifactNodeIDs; !reflect.DeepEqual(got, []string{nodes[0].ID, nodes[1].ID}) {
		t.Fatalf("Compose 没有收集全部 target: %v", got)
	}
	if !reflect.DeepEqual(original[0].Tasks[0].Config.InlineBuild, inline) || inline.TargetStage != "web,api" {
		t.Fatal("运行展开修改了原流水线定义")
	}
}

func TestBuildTargetsLibraryAndInlineRunSnapshots(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(map[bool]string{false: "通用方案", true: "流水线自定义"}[inline], func(t *testing.T) {
			s, _, _, repositoryID := newPipelineTestService(t)
			ctx := context.Background()
			app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "target_snapshot", RepositoryID: repositoryID})
			if err != nil {
				t.Fatal(err)
			}
			registry := createTestImageRegistry(t, s, "目标镜像仓库")
			plan, err := s.CreateBuildPlan(ctx, "admin", BuildPlanInput{Name: "通用多阶段", Kind: model.BuildPlanDockerfile, TargetStage: "web,api", ImageRegistryID: registry.ID})
			if err != nil {
				t.Fatal(err)
			}
			workflow, err := s.GetWorkflow(ctx, app.ID)
			if err != nil {
				t.Fatal(err)
			}
			node := model.WorkflowNode{ID: "build", Name: "镜像构建", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: plan.ID}}
			if inline {
				node.Config = model.WorkflowNodeConfig{InlineBuild: plan}
			}
			source := model.WorkflowNode{ID: "source", Name: "代码源", Type: model.WorkflowNodeTrigger, Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"manual"}}}
			saved, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name, Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: []model.WorkflowStage{{ID: "stage", Name: "构建", Tasks: []model.WorkflowNode{node}}}})
			if err != nil {
				t.Fatal(err)
			}
			run, err := s.newResolvedWorkflowRun(ctx, app, saved.Workflow, source, "manual", "refs/tags/v1", strings.Repeat("b", 40), "admin", "", time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := parseWorkflowSnapshot(run)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Stages[0].Tasks) != 2 {
				t.Fatalf("运行未展开两个 target: %+v", snapshot.Stages)
			}
			for i, target := range []string{"web", "api"} {
				id := snapshot.Stages[0].Tasks[i].ID
				if snapshot.BuildPlans[id].TargetStage != target {
					t.Fatalf("快照 target 丢失: %+v", snapshot.BuildPlans)
				}
			}
			if plan.TargetStage != "web,api" || len(saved.Workflow.Stages[0].Tasks) != 1 {
				t.Fatal("运行展开污染了通用方案或已保存流水线")
			}
		})
	}
}

func TestBuildTargetsCannotReuseOneArtifactForWholeGroup(t *testing.T) {
	workflow := &model.ReleaseWorkflow{Stages: []model.WorkflowStage{{Tasks: []model.WorkflowNode{{ID: "build", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: "plan"}}, {ID: "deploy", Type: model.WorkflowNodeDeploy}}}}}
	_, _, ok := manualArtifactBuildNodes(workflow, model.BuildRun{BuildPlanID: "plan", WorkflowNodeID: "build"}, model.BuildPlan{ID: "plan", Kind: model.BuildPlanDockerfile, TargetStage: "web,api"}, model.Artifact{Kind: model.ArtifactKindOCIImage})
	if ok {
		t.Fatal("一个 target 制品不能跳过整组构建")
	}
}
