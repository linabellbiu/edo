package pipeline

import (
	"context"
	"testing"

	"zrt/internal/credential"
	"zrt/internal/model"
	"zrt/internal/repository"
)

type pipelineRefLister struct {
	refs repository.RefResult
}

func (l pipelineRefLister) ListRefs(context.Context, model.GitRepository, string) (repository.RefResult, error) {
	return l.refs, nil
}

func TestSyncApplicationPollsPushNodeWithoutWebhook(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	service.repositories = repository.NewService(
		db,
		secrets,
		credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "test", SHA: "test-commit"}},
		}},
		4,
	)
	application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "事件触发应用", RepositoryID: repositoryID,
		Environments: []EnvironmentInput{{
			Key: "test", Name: "测试环境", Branch: "test",
			WatchPush: true, WatchPullRequest: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	application, run, err := service.SyncApplication(context.Background(), application.ID, "manual_sync")
	if err != nil {
		t.Fatalf("仓库可读取时不应检查失败: %v", err)
	}
	if run != nil {
		t.Fatalf("首次读取 Push 分支只应建立基线: %+v", run)
	}
	if application.SyncStatus != model.ApplicationSyncSynced || application.LastObservedCommit != "test-commit" {
		t.Fatalf("仓库检查状态错误: %+v", application)
	}
}

func TestSyncApplicationAutomaticallyExecutesPolledPushChange(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	lister := &pipelineRefLister{refs: repository.RefResult{
		Branches: []repository.GitRef{{Name: "main", SHA: "first-commit"}},
	}}
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets), lister, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "定时检查分支")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}

	application, run, err := service.SyncApplication(context.Background(), application.ID, "manual_sync")
	if err != nil || run != nil || application.LastObservedCommit != "first-commit" {
		t.Fatalf("首次检查没有正确建立基线: application=%+v run=%+v err=%v", application, run, err)
	}
	lister.refs = repository.RefResult{Branches: []repository.GitRef{{Name: "main", SHA: "second-commit"}}}

	application, run, err = service.SyncApplication(context.Background(), application.ID, "poll")
	if err != nil {
		t.Fatalf("定时检查 Push 变化失败: %v", err)
	}
	if application.SyncStatus != model.ApplicationSyncChanged || run == nil || run.Trigger != "poll_push" ||
		run.Ref != "refs/heads/main" || run.CommitSHA != "second-commit" || run.Status != model.PipelineRunRunning ||
		run.CurrentNodeID != "deploy-dev" || run.ExecutionJobID == "" {
		t.Fatalf("Push 变化没有自动提交构建发布任务: application=%+v run=%+v", application, run)
	}
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	_, duplicate, err := service.SyncApplication(context.Background(), application.ID, "poll")
	if err != nil || duplicate != nil {
		t.Fatalf("切换触发节点来源不应重复发布当前 Commit: run=%+v err=%v", duplicate, err)
	}
	var runCount int64
	if err := db.Model(&model.PipelineRun{}).Where("application_id = ?", application.ID).Count(&runCount).Error; err != nil || runCount != 1 {
		t.Fatalf("切换流水线启用状态后出现重复运行: count=%d err=%v", runCount, err)
	}
}

func TestSyncApplicationAcceptsReadableRepositoryWithoutMatchingRef(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	service.repositories = repository.NewService(
		db,
		secrets,
		credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: "main-commit"}},
		}},
		4,
	)
	application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "分支尚未创建", RepositoryID: repositoryID, Branch: "test",
		PollEnabled: true, PollIntervalSeconds: 3,
	})
	if err != nil {
		t.Fatal(err)
	}

	application, run, err := service.SyncApplication(context.Background(), application.ID, "manual_sync")
	if err != nil {
		t.Fatalf("仓库可读取时不应因分支缺失而失败: %v", err)
	}
	if run != nil {
		t.Fatalf("未匹配引用时不应创建流水线运行: %+v", run)
	}
	if application.SyncStatus != model.ApplicationSyncSynced || application.SyncMessage != "所有仓库均可读取；未找到流水线配置的分支、PR 或 Tag" {
		t.Fatalf("仓库检查状态错误: %+v", application)
	}
}

func TestSyncApplicationPollsEveryConfiguredCustomRef(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	lister := &pipelineRefLister{refs: repository.RefResult{
		Branches: []repository.GitRef{
			{Name: "feature/payment", SHA: "payment-1"},
			{Name: "feature/search", SHA: "search-1"},
			{Name: "main", SHA: "main-1"},
		},
		Tags: []repository.GitRef{{Name: "v1.0.0", SHA: "tag-1"}},
		PullRequests: []repository.PullRequestRef{{
			Number: 12, Ref: "refs/pull/12/head", SHA: "pr-1",
			SourceBranch: "bugfix/payment", TargetBranch: "feature/payment",
		}},
	}}
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets), lister, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "自定义分支定时检查")
	var workflow model.ReleaseWorkflow
	if err := db.First(&workflow, "application_id = ?", application.ID).Error; err != nil {
		t.Fatal(err)
	}
	for i := range workflow.Nodes {
		if workflow.Nodes[i].Type == model.WorkflowNodeTrigger {
			workflow.Nodes[i].Config.Branch = "feature/*"
			workflow.Nodes[i].Config.Events = []string{"push", "pr", "tag"}
			workflow.Nodes[i].Config.TagPattern = "v*"
		}
	}
	workflow.IsActive = true
	if err := db.Save(&workflow).Error; err != nil {
		t.Fatal(err)
	}

	_, run, err := service.SyncApplication(context.Background(), application.ID, "manual_sync")
	if err != nil || run != nil {
		t.Fatalf("首次检查只应建立所有 Ref 的基线: run=%+v err=%v", run, err)
	}
	var baselineCount int64
	if err := db.Model(&model.ApplicationRepositoryObservation{}).Count(&baselineCount).Error; err != nil || baselineCount != 4 {
		t.Fatalf("自定义分支、PR 和 Tag 基线不完整: count=%d err=%v", baselineCount, err)
	}

	lister.refs = repository.RefResult{
		Branches: []repository.GitRef{
			{Name: "feature/payment", SHA: "payment-2"},
			{Name: "feature/search", SHA: "search-1"},
			{Name: "main", SHA: "main-2"},
		},
		Tags: []repository.GitRef{
			{Name: "v1.0.0", SHA: "tag-1"},
			{Name: "v1.1.0", SHA: "tag-2"},
		},
		PullRequests: []repository.PullRequestRef{{
			Number: 12, Ref: "refs/pull/12/head", SHA: "pr-2",
			SourceBranch: "bugfix/payment", TargetBranch: "feature/payment",
		}},
	}
	_, _, err = service.SyncApplication(context.Background(), application.ID, "poll")
	if err != nil {
		t.Fatalf("检查自定义分支、PR 和 Tag 变化失败: %v", err)
	}
	var runs []model.PipelineRun
	if err := db.Where("application_id = ?", application.ID).Find(&runs).Error; err != nil {
		t.Fatal(err)
	}
	triggerCounts := make(map[string]int)
	for i := range runs {
		triggerCounts[runs[i].Trigger]++
	}
	if len(runs) != 3 || triggerCounts["poll_push"] != 1 || triggerCounts["poll_pr"] != 1 || triggerCounts["poll_tag"] != 1 {
		t.Fatalf("没有分别执行自定义分支、PR 和 Tag 动作: runs=%+v", runs)
	}
	for i := range runs {
		if runs[i].Ref == "refs/heads/main" {
			t.Fatalf("不匹配 feature/* 的 main 分支不应触发: %+v", runs[i])
		}
	}
}
