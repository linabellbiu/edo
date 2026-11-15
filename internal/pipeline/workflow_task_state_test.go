package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"edo/internal/model"
)

func TestWorkflowTaskSaveKeepsOtherTasksAndDisabledState(t *testing.T) {
	s, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "task_state", RepositoryID: repositoryID, PollIntervalSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := WorkflowInput{SchemaVersion: model.WorkflowSchemaVersion, Name: result.Workflow.Name, Revision: result.Workflow.Revision, Activate: true,
		Source: result.Workflow.Source, Stages: []model.WorkflowStage{{ID: "stage", Name: "任务", Tasks: []model.WorkflowNode{
			{ID: "shell", Name: "检查", Type: model.WorkflowNodeShell, Config: model.WorkflowNodeConfig{Script: "echo ok"}},
			{ID: "unfinished", Name: "未完成部署", Type: model.WorkflowNodeDeploy, Disabled: true},
			{ID: "approval", Name: "关闭的审核", Type: model.WorkflowNodeApproval, Disabled: true},
		}}}}
	result, err = s.SaveApplicationWorkflow(ctx, application.ID, result.Workflow.ID, "admin", input)
	if err != nil || !result.Valid {
		t.Fatalf("关闭的未完成任务不应阻止启用：result=%+v err=%v", result, err)
	}
	application, err = s.FindApplication(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.newResolvedWorkflowRun(ctx, application, result.Workflow, result.Workflow.Source, "manual", "main", "0123456789012345678901234567890123456789", "admin", "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil || len(workflowTasks(snapshot.Stages)) != 1 || workflowTasks(snapshot.Stages)[0].ID != "shell" || snapshot.ApprovalEnabled {
		t.Fatalf("关闭的部署或审核进入运行快照：snapshot=%+v err=%v", snapshot, err)
	}
	input = WorkflowInput{SchemaVersion: model.WorkflowSchemaVersion, Name: "不能连带保存的名称", Revision: result.Workflow.Revision, TaskID: "shell", Source: result.Workflow.Source, Stages: cloneWorkflowStages(result.Workflow.Stages)}
	input.Source.Config.Events = nil
	input.Stages[0].Name = "不能连带保存的阶段"
	input.Stages[0].Tasks[0].Disabled = true
	input.Stages[0].Tasks[1].Disabled = false
	input.Stages[0].Tasks[1].Config.DeploymentInputVersion = 99
	result, err = s.SaveApplicationWorkflow(ctx, application.ID, result.Workflow.ID, "admin", input)
	if err != nil || !result.Workflow.IsActive {
		t.Fatalf("独立保存不应启用校验其他草稿或关闭流水线：result=%+v err=%v", result, err)
	}
	saved, err := s.FindApplicationWorkflow(ctx, application.ID, result.Workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name == input.Name || saved.Stages[0].Name != "任务" || !saved.Stages[0].Tasks[1].Disabled || saved.Stages[0].Tasks[1].Config.DeploymentInputVersion != 0 || len(saved.Source.Config.Events) == 0 {
		t.Fatalf("任务保存覆盖了其他草稿：%+v", saved)
	}
	if !saved.Stages[0].Tasks[0].Disabled {
		t.Fatal("重新读取后任务被自动开启")
	}
	input.Revision = saved.Revision
	input.Stages[0].Tasks[0].Name = "关闭后再次更新"
	result, err = s.SaveApplicationWorkflow(ctx, application.ID, saved.ID, "admin", input)
	if err != nil || !result.Workflow.Stages[0].Tasks[0].Disabled || !result.Workflow.IsActive {
		t.Fatalf("更新不应重新开启任务：result=%+v err=%v", result, err)
	}
	if _, err := s.SaveApplicationWorkflow(ctx, application.ID, saved.ID, "admin", input); !errors.Is(err, ErrWorkflowRevisionConflict) {
		t.Fatalf("任务独立保存必须保留版本冲突检查：%v", err)
	}
	if _, err := s.newResolvedWorkflowRun(ctx, application, result.Workflow, result.Workflow.Source, "manual", "main", "0123456789012345678901234567890123456789", "admin", "", time.Now().UTC()); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("全部任务关闭时不能创建空运行：%v", err)
	}
}

func TestWorkflowTemplateTaskSaveAddsOnlySelectedTaskAndPreservesActive(t *testing.T) {
	s, _, _, _ := newPipelineTestService(t)
	ctx := context.Background()
	result, err := s.CreateWorkflowTemplate(ctx, "admin", WorkflowTemplateInput{WorkflowInput: WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: "独立保存任务", Activate: true,
		Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源", Config: model.WorkflowNodeConfig{Events: []string{"manual"}}},
		Stages: []model.WorkflowStage{{ID: "first", Name: "检查", Tasks: []model.WorkflowNode{{ID: "check", Name: "检查", Type: model.WorkflowNodeShell, Config: model.WorkflowNodeConfig{Script: "echo ok"}}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	input := WorkflowTemplateInput{WorkflowInput: WorkflowInput{SchemaVersion: model.WorkflowSchemaVersion, Revision: result.WorkflowTemplate.Revision, TaskID: "new", Stages: []model.WorkflowStage{{ID: "new-stage", Name: "新阶段", Tasks: []model.WorkflowNode{
		{ID: "new", Type: model.WorkflowNodeDeploy, Name: "Kubernetes", Disabled: true},
		{ID: "other", Type: model.WorkflowNodeDeploy, Name: "不能连带保存", Config: model.WorkflowNodeConfig{DeploymentInputVersion: 99}},
	}}}}}
	result, err = s.SaveWorkflowTemplate(ctx, result.WorkflowTemplate.ID, "admin", input)
	if err != nil || !result.WorkflowTemplate.IsActive {
		t.Fatalf("新任务保存失败：result=%+v err=%v", result, err)
	}
	if len(result.WorkflowTemplate.Stages) != 2 || len(result.WorkflowTemplate.Stages[1].Tasks) != 1 || result.WorkflowTemplate.Stages[1].Tasks[0].ID != "new" || !result.WorkflowTemplate.Stages[1].Tasks[0].Disabled {
		t.Fatalf("新任务连带保存了其他草稿或丢失关闭状态：%+v", result.WorkflowTemplate.Stages)
	}
	input = WorkflowTemplateInput{WorkflowInput: WorkflowInput{SchemaVersion: model.WorkflowSchemaVersion, Name: result.WorkflowTemplate.Name, SaveOnly: true, Revision: result.WorkflowTemplate.Revision, Source: result.WorkflowTemplate.Source, Stages: cloneWorkflowStages(result.WorkflowTemplate.Stages)}}
	input.Stages[1].Tasks[0].Disabled = false
	result, err = s.SaveWorkflowTemplate(ctx, result.WorkflowTemplate.ID, "admin", input)
	if err != nil || !result.WorkflowTemplate.IsActive || result.Valid {
		t.Fatalf("普通保存应允许未完成配置并保留流水线状态：result=%+v err=%v", result, err)
	}
}

func TestWorkflowDisabledUpstreamCannotBeReplacedByAnotherBuild(t *testing.T) {
	s, _, _, _ := newPipelineTestService(t)
	stages := []model.WorkflowStage{
		{ID: "builds", Name: "构建", Tasks: []model.WorkflowNode{{ID: "closed", Name: "已关闭的构建", Type: model.WorkflowNodeBuild, Disabled: true}}},
		{ID: "deploy", Name: "部署", Tasks: []model.WorkflowNode{{ID: "dependent", Name: "部署任务", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{ArtifactNodeID: "closed"}}}},
	}
	source := model.WorkflowNode{ID: "source", Name: "代码源", Type: model.WorkflowNodeTrigger, Config: model.WorkflowNodeConfig{Events: []string{"manual"}}}
	issues := s.validateWorkflow(context.Background(), nil, model.WorkflowSchemaVersion, source, stages)
	if !hasWorkflowIssue(issues, "disabled_upstream_task") || hasWorkflowIssue(issues, "missing_build_plan") {
		t.Fatalf("应准确报告关闭的依赖，不检查关闭任务自身的配置：%+v", issues)
	}
}
