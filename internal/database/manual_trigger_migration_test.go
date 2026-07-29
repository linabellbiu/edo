package database

import (
	"context"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/model"
)

func TestManualReleaseNodeMigrationKeepsExecutionSnapshots(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:manual_trigger_upgrade?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err := db.AutoMigrate(
		&model.GitRepository{}, &model.ReleaseWorkflowTemplate{}, &model.Application{},
		&model.ReleaseWorkflow{}, &model.PipelineRun{},
		&model.ReleasePlanExecution{}, &model.ReleasePlanExecutionItem{},
	); err != nil {
		t.Fatalf("创建手动触发迁移测试表失败: %v", err)
	}

	now := time.Now().UTC()
	repository := model.GitRepository{
		ID: "manual-trigger-repository", Name: "手动触发迁移仓库", Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/app.git", DefaultBranch: "main", AuthType: model.GitAuthNone,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	applications := []model.Application{
		{ID: "linked-application", Name: "关联方案应用", RepositoryID: repository.ID, Branch: "main", PollIntervalSeconds: 3, SyncStatus: model.ApplicationSyncIdle, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: "direct-application", Name: "直接手动应用", RepositoryID: repository.ID, Branch: "main", PollIntervalSeconds: 3, SyncStatus: model.ApplicationSyncIdle, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: "current-application", Name: "当前结构应用", RepositoryID: repository.ID, Branch: "main", PollIntervalSeconds: 3, SyncStatus: model.ApplicationSyncIdle, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&applications).Error; err != nil {
		t.Fatal(err)
	}

	legacyNodes := []model.WorkflowNode{
		{ID: "legacy-manual", Type: model.WorkflowNodeManualRelease, Name: "手动发布", Position: model.WorkflowPosition{X: 20, Y: 40}, Config: model.WorkflowNodeConfig{Environment: "prod"}},
		{ID: "code-trigger", Type: model.WorkflowNodeTrigger, Name: "生产分支", Position: model.WorkflowPosition{X: 260, Y: 40}, Config: model.WorkflowNodeConfig{Environment: "prod", Branch: "main", Events: []string{"push"}}},
		{ID: "deploy", Type: model.WorkflowNodeDeploy, Name: "部署", Position: model.WorkflowPosition{X: 500, Y: 40}, Config: model.WorkflowNodeConfig{Environment: "prod"}},
	}
	legacyEdges := []model.WorkflowEdge{
		{ID: "manual-trigger", Source: "legacy-manual", Target: "code-trigger"},
		{ID: "trigger-deploy", Source: "code-trigger", Target: "deploy"},
	}
	template := model.ReleaseWorkflowTemplate{
		ID: "legacy-template", Name: "旧手动发布方案", Revision: 4, IsActive: true,
		Nodes: legacyNodes, Edges: legacyEdges, Viewport: model.WorkflowViewport{Zoom: 1},
		CreatedBy: "admin", UpdatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&template).Error; err != nil {
		t.Fatal(err)
	}
	workflows := []model.ReleaseWorkflow{
		{
			ID: "linked-workflow", ApplicationID: applications[0].ID, WorkflowTemplateID: template.ID,
			WorkflowTemplateRevision: template.Revision, Name: "关联旧流水线", Revision: 9, IsActive: true,
			Nodes: legacyNodes, Edges: legacyEdges, Viewport: model.WorkflowViewport{Zoom: 1},
			CreatedBy: "admin", UpdatedBy: "admin", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "direct-workflow", ApplicationID: applications[1].ID, Name: "直接手动流水线", Revision: 2, IsActive: true,
			Nodes: []model.WorkflowNode{
				{ID: "direct-manual", Type: model.WorkflowNodeManualRelease, Name: "手动发布", Config: model.WorkflowNodeConfig{Environment: "prod", Description: "选择版本后发布"}},
				{ID: "direct-deploy", Type: model.WorkflowNodeDeploy, Name: "部署", Config: model.WorkflowNodeConfig{Environment: "prod"}},
			},
			Edges:    []model.WorkflowEdge{{ID: "direct-edge", Source: "direct-manual", Target: "direct-deploy"}},
			Viewport: model.WorkflowViewport{Zoom: 1}, CreatedBy: "admin", UpdatedBy: "admin", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "current-workflow", ApplicationID: applications[2].ID, Name: "当前流水线", Revision: 6, IsActive: true,
			Nodes: []model.WorkflowNode{
				{ID: "current-trigger", Type: model.WorkflowNodeTrigger, Name: "代码触发", Config: model.WorkflowNodeConfig{Environment: "prod", Branch: "main", Events: []string{"push", "manual"}}},
				{ID: "current-deploy", Type: model.WorkflowNodeDeploy, Name: "部署", Config: model.WorkflowNodeConfig{Environment: "prod"}},
			},
			Edges:    []model.WorkflowEdge{{ID: "current-edge", Source: "current-trigger", Target: "current-deploy"}},
			Viewport: model.WorkflowViewport{Zoom: 1}, CreatedBy: "admin", UpdatedBy: "admin", CreatedAt: now, UpdatedAt: now,
		},
	}
	if err := db.Create(&workflows).Error; err != nil {
		t.Fatal(err)
	}

	legacySnapshot := `{"nodes":[{"id":"legacy-manual","type":"manual_release"},{"id":"code-trigger","type":"trigger"}],"edges":[{"id":"manual-trigger","source":"legacy-manual","target":"code-trigger"}]}`
	runs := []model.PipelineRun{
		{
			ID: "blocked-without-snapshot", ApplicationID: applications[0].ID, Trigger: "manual",
			Ref: "refs/heads/main", CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Status: model.PipelineRunBlocked, Stage: "configured", CurrentNodeID: "legacy-manual", WorkflowSnapshot: "",
			CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "blocked-plan-snapshot", ApplicationID: applications[0].ID,
			ReleasePlanExecutionID: "execution", ReleasePlanExecutionItemID: "execution-item", Trigger: "release_plan",
			Ref: "refs/heads/main", CommitSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Status: model.PipelineRunBlocked, Stage: "manual_release", CurrentNodeID: "legacy-manual", WorkflowSnapshot: legacySnapshot,
			CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "completed-history", ApplicationID: applications[0].ID, Trigger: "manual",
			Ref: "refs/heads/main", CommitSHA: "cccccccccccccccccccccccccccccccccccccccc",
			Status: model.PipelineRunSucceeded, Stage: "completed", CurrentNodeID: "legacy-manual", WorkflowSnapshot: legacySnapshot,
			CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
		},
	}
	if err := db.Create(&runs).Error; err != nil {
		t.Fatal(err)
	}
	execution := model.ReleasePlanExecution{
		ID: "execution", ReleasePlanID: "plan", RequestID: "manual-trigger-migration",
		Status: model.ReleasePlanExecutionPending, Snapshot: `{"groups":[]}`,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatal(err)
	}
	item := model.ReleasePlanExecutionItem{
		ID: "execution-item", ReleasePlanExecutionID: execution.ID, ReleaseGroupID: "group",
		ReleaseGroupApplicationID: "membership", ApplicationID: applications[0].ID,
		PipelineRunID: runs[1].ID, Status: model.ReleasePlanExecutionItemPending,
		Ref: runs[1].Ref, CommitSHA: runs[1].CommitSHA, SourceNodeID: "legacy-manual",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.Transaction(migrateManualReleaseNodesToTriggerEvents); err != nil {
		t.Fatalf("迁移手动发布节点失败: %v", err)
	}

	assertMigratedManualTriggerState(t, db, legacySnapshot)
	var firstTemplate model.ReleaseWorkflowTemplate
	var firstWorkflow model.ReleaseWorkflow
	if err := db.First(&firstTemplate, "id = ?", template.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&firstWorkflow, "id = ?", workflows[0].ID).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.Transaction(migrateManualReleaseNodesToTriggerEvents); err != nil {
		t.Fatalf("重复执行手动触发迁移失败: %v", err)
	}
	assertMigratedManualTriggerState(t, db, legacySnapshot)
	var repeatedTemplate model.ReleaseWorkflowTemplate
	var repeatedWorkflow model.ReleaseWorkflow
	if err := db.First(&repeatedTemplate, "id = ?", template.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&repeatedWorkflow, "id = ?", workflows[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if repeatedTemplate.Revision != firstTemplate.Revision || repeatedWorkflow.Revision != firstWorkflow.Revision ||
		!reflect.DeepEqual(repeatedTemplate.Nodes, firstTemplate.Nodes) || !reflect.DeepEqual(repeatedTemplate.Edges, firstTemplate.Edges) ||
		!reflect.DeepEqual(repeatedWorkflow.Nodes, firstWorkflow.Nodes) || !reflect.DeepEqual(repeatedWorkflow.Edges, firstWorkflow.Edges) {
		t.Fatalf("重复迁移改变了已经规范化的流水线: template=%+v workflow=%+v", repeatedTemplate, repeatedWorkflow)
	}
}

func assertMigratedManualTriggerState(t *testing.T, db *gorm.DB, legacySnapshot string) {
	t.Helper()

	var template model.ReleaseWorkflowTemplate
	if err := db.First(&template, "id = ?", "legacy-template").Error; err != nil {
		t.Fatal(err)
	}
	if template.Revision != 5 || !template.IsActive || len(template.Nodes) != 2 || len(template.Edges) != 1 {
		t.Fatalf("流水线方案没有等价折叠旧手动节点: %+v", template)
	}
	templateTrigger := workflowNodeByID(template.Nodes, "code-trigger")
	if templateTrigger == nil || !workflowEventExists(templateTrigger.Config.Events, "push") ||
		!workflowEventExists(templateTrigger.Config.Events, "manual") || len(templateTrigger.Config.Events) != 2 {
		t.Fatalf("流水线方案代码触发节点没有启用手动事件: %+v", templateTrigger)
	}
	if template.Edges[0].Source != "code-trigger" || template.Edges[0].Target != "deploy" {
		t.Fatalf("流水线方案折叠后连线错误: %+v", template.Edges)
	}

	var linked model.ReleaseWorkflow
	if err := db.First(&linked, "id = ?", "linked-workflow").Error; err != nil {
		t.Fatal(err)
	}
	if linked.Revision != 10 || linked.WorkflowTemplateRevision != template.Revision || !linked.IsActive ||
		len(linked.Nodes) != 2 || workflowNodeByID(linked.Nodes, "legacy-manual") != nil {
		t.Fatalf("关联应用流水线没有同步方案修订和等价图: %+v", linked)
	}

	var direct model.ReleaseWorkflow
	if err := db.First(&direct, "id = ?", "direct-workflow").Error; err != nil {
		t.Fatal(err)
	}
	directSource := workflowNodeByID(direct.Nodes, "direct-manual")
	if direct.Revision != 3 || !direct.IsActive || directSource == nil || directSource.Type != model.WorkflowNodeTrigger ||
		!reflect.DeepEqual(directSource.Config.Events, []string{"manual"}) || len(direct.Edges) != 1 || direct.Edges[0].Source != directSource.ID {
		t.Fatalf("直接连接流程节点的手动入口没有原地转换: %+v", direct)
	}

	var current model.ReleaseWorkflow
	if err := db.First(&current, "id = ?", "current-workflow").Error; err != nil {
		t.Fatal(err)
	}
	if current.Revision != 6 || len(current.Nodes) != 2 {
		t.Fatalf("当前 trigger.events 结构不应被重复迁移: %+v", current)
	}

	var blocked model.PipelineRun
	if err := db.First(&blocked, "id = ?", "blocked-without-snapshot").Error; err != nil {
		t.Fatal(err)
	}
	if blocked.CurrentNodeID != "code-trigger" || blocked.WorkflowSnapshot != "" || blocked.Status != model.PipelineRunBlocked {
		t.Fatalf("无快照的待执行运行没有安全重映射入口: %+v", blocked)
	}
	for _, runID := range []string{"blocked-plan-snapshot", "completed-history"} {
		var run model.PipelineRun
		if err := db.First(&run, "id = ?", runID).Error; err != nil {
			t.Fatal(err)
		}
		if run.CurrentNodeID != "legacy-manual" || run.WorkflowSnapshot != legacySnapshot {
			t.Fatalf("迁移改写了历史或在途执行快照: %+v", run)
		}
	}
	var item model.ReleasePlanExecutionItem
	if err := db.First(&item, "id = ?", "execution-item").Error; err != nil {
		t.Fatal(err)
	}
	if item.SourceNodeID != "legacy-manual" {
		t.Fatalf("迁移改写了发布计划执行项的不可变入口: %+v", item)
	}
}

func workflowNodeByID(nodes []model.WorkflowNode, id string) *model.WorkflowNode {
	for i := range nodes {
		if nodes[i].ID == id {
			return &nodes[i]
		}
	}
	return nil
}
