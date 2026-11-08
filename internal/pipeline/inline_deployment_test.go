package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"edo/internal/deployment"
	"edo/internal/model"
	"edo/internal/sshdeploy"
)

type inlineComposeHomeProbe struct{ calls int }

func (p *inlineComposeHomeProbe) RunHostDeploymentScript(_ context.Context, input sshdeploy.Input) (sshdeploy.Result, error) {
	p.calls++
	_, _ = io.WriteString(input.Stdout, "/home/edo-test")
	return sshdeploy.Result{Started: true}, nil
}

func TestInlineDeploymentPlansStayInWorkflowAndFreezeExecution(t *testing.T) {
	for _, kind := range []model.DeploymentPlanKind{model.DeploymentPlanDocker, model.DeploymentPlanCompose, model.DeploymentPlanComposeStack, model.DeploymentPlanScript, model.DeploymentPlanKubernetes} {
		t.Run(string(kind), func(t *testing.T) {
			s, db, _, repositoryID := newPipelineTestService(t)
			homeProbe := &inlineComposeHomeProbe{}
			ctx := context.Background()
			buildInput := BuildPlanInput{Name: "自定义方案构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile"}
			platform := model.DeploymentDocker
			if kind == model.DeploymentPlanScript {
				platform = model.DeploymentSSH
				buildInput = BuildPlanInput{Name: "自定义文件构建", Kind: model.BuildPlanScript, Script: "mkdir -p dist", ArtifactPath: "dist"}
			}
			if kind == model.DeploymentPlanComposeStack {
				platform = model.DeploymentSSH
			}
			if kind == model.DeploymentPlanKubernetes || kind == model.DeploymentPlanComposeStack {
				if kind == model.DeploymentPlanKubernetes {
					platform = model.DeploymentKubernetes
				}
				registry, err := s.CreateRegistry(ctx, "admin", RegistryInput{Name: "自定义镜像仓库", Provider: model.RegistryGeneric, Endpoint: "https://registry.example.com", Namespace: "apps"})
				if err != nil {
					t.Fatal(err)
				}
				buildInput.ImageRegistryID = registry.ID
			}
			build, err := s.CreateBuildPlan(ctx, "admin", buildInput)
			if err != nil {
				t.Fatal(err)
			}
			targetInput := deploymentPlanTargetInput(t, s, model.DeploymentTarget{Name: "流水线自定义目标", Platform: platform, EnvironmentID: "inline-environment", HostID: "inline-host", RuntimeID: "inline-runtime", WorkingDirectory: "/srv/inline", Namespace: "default", WorkloadName: "inline-app", ContainerName: "app", RolloutTimeout: 120})
			definition := &model.WorkflowDeploymentPlan{Name: "当前流水线部署", Kind: kind, TimeoutSeconds: 120, Script: "printf original", ComposeYAML: "services:\n  app:\n    restart: unless-stopped\n", ServiceName: "app", DeploymentTarget: &model.DeploymentTarget{Platform: platform, EnvironmentID: targetInput.EnvironmentID, HostID: targetInput.HostID, RuntimeID: targetInput.RuntimeID, WorkingDirectory: targetInput.WorkingDirectory, Namespace: targetInput.Namespace, WorkloadName: targetInput.WorkloadName, ContainerName: targetInput.ContainerName}}
			if kind == model.DeploymentPlanComposeStack {
				definition.ComposeYAML = "services:\n  app:\n    image: registry.example.com/apps/inline_compose_stack:${IMAGE_TAG}\n"
				definition.DeploymentTarget.WorkingDirectory = ""
				s.deployments = deployment.NewService(db, nil, nil, homeProbe, nil, "", nil)
			}
			app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "inline_" + string(kind), RepositoryID: repositoryID, PollIntervalSeconds: 60})
			if err != nil {
				t.Fatal(err)
			}
			workflow, err := s.GetWorkflow(ctx, app.ID)
			if err != nil {
				t.Fatal(err)
			}
			source, stages := testStageWorkflowGraph(build.ID, "")
			var deployID string
			for i := range stages {
				for j := range stages[i].Tasks {
					if stages[i].Tasks[j].Type == model.WorkflowNodeDeploy {
						stages[i].Tasks[j].Config.DeploymentSource = "inline"
						stages[i].Tasks[j].Config.InlineDeploymentPlan = definition
						if kind == model.DeploymentPlanComposeStack {
							stages[i].Tasks[j].Config.ArtifactNodeID = ""
							stages[i].Tasks[j].Config.Compose = &model.ComposeDeploymentSettings{ProjectName: "inline-app", TagVariable: "IMAGE_TAG"}
						}
						deployID = stages[i].Tasks[j].ID
					}
				}
			}
			var plansBefore, targetsBefore int64
			db.Model(&model.DeploymentPlan{}).Count(&plansBefore)
			db.Model(&model.DeploymentTarget{}).Count(&targetsBefore)
			saved, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name, Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: stages})
			if err != nil {
				t.Fatalf("自定义方案无法保存启用: %v，问题：%+v", err, saved)
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
			planID, targetID := model.InlineDeploymentIdentity(app.Workflow.ID, deployID)
			if snapshot.DeploymentPlans[deployID].ID != planID || snapshot.DeploymentPlans[deployID].Kind != kind || snapshot.DeploymentTargets[deployID].ID != targetID {
				t.Fatal("自定义方案未完整冻结为可执行快照")
			}
			if kind == model.DeploymentPlanComposeStack && (snapshot.DeploymentTargets[deployID].WorkingDirectory != "/home/edo-test/edo/inline_compose_stack/config" || !snapshot.DeploymentTargets[deployID].ManagedWorkingDirectory || homeProbe.calls != 1 || definition.DeploymentTarget.WorkingDirectory != "") {
				t.Fatal("自动目录应固定在运行快照中，不能写回方案或变更已有资源")
			}
			var plansAfter, targetsAfter int64
			db.Model(&model.DeploymentPlan{}).Count(&plansAfter)
			db.Model(&model.DeploymentTarget{}).Count(&targetsAfter)
			if plansBefore != plansAfter || targetsBefore != targetsAfter {
				t.Fatal("流水线自定义方案不应创建方案库或目标记录")
			}
			for _, node := range workflowTasks(saved.Workflow.Stages) {
				if node.ID == deployID && (node.Config.DeploymentPlanID != "" || node.Config.InlineDeploymentPlan == nil) {
					t.Fatal("保存的流水线不应绑定虚构的方案库记录")
				}
			}
			definition.Script = "printf changed"
			frozen, err := parseWorkflowSnapshot(run)
			if err != nil || kind == model.DeploymentPlanScript && frozen.DeploymentPlans[deployID].Script != "printf original" {
				t.Fatal("草稿变更影响了已固定运行")
			}
			otherPlan, otherTarget := model.InlineDeploymentIdentity("another-workflow", deployID)
			if otherPlan == planID || otherTarget == targetID {
				t.Fatal("不同流水线复用了执行身份")
			}
		})
	}
}

func TestInlineDeploymentRejectsAmbiguousSourceAndStripsResourceIdentity(t *testing.T) {
	definition := &model.WorkflowDeploymentPlan{Name: "自定义", Kind: model.DeploymentPlanDocker, TimeoutSeconds: 120, DeploymentTarget: &model.DeploymentTarget{ID: "foreign-target", ProjectID: "foreign-project", DepartmentID: "foreign-department", Platform: model.DeploymentDocker, EnvironmentID: "selected-environment"}}
	node := model.WorkflowNode{ID: "deploy", Name: "部署", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentSource: "inline", DeploymentPlanID: "foreign-plan", InlineDeploymentPlan: definition}}
	if err := sanitizeWorkflowNode(&node); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatal("同时传入两个来源未拒绝")
	}
	node.Config.DeploymentPlanID = ""
	if err := sanitizeWorkflowNode(&node); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(node.Config.InlineDeploymentPlan)
	var clean model.WorkflowDeploymentPlan
	if err := json.Unmarshal(encoded, &clean); err != nil {
		t.Fatal(err)
	}
	if clean.DeploymentTarget.ID != "" || clean.DeploymentTarget.ProjectID != "" || clean.DeploymentTarget.DepartmentID != "" {
		t.Fatal("不应接受客户端伪造的目标身份或归属")
	}
	node.Type = model.WorkflowNodeShell
	if err := sanitizeWorkflowNode(&node); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatal("非部署任务接受了自定义部署配置")
	}
}
