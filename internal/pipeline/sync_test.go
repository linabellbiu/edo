package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"zrt/internal/credential"
	"zrt/internal/model"
	"zrt/internal/repository"
)

type pipelineRefLister struct {
	refs           repository.RefResult
	pullRequestErr error
}

func (l pipelineRefLister) ListRefs(context.Context, model.GitRepository, string) (repository.RefResult, error) {
	return l.refs, nil
}

func (l pipelineRefLister) ListPullRequests(context.Context, model.GitRepository, string, string) ([]repository.PullRequestRef, error) {
	return l.refs.PullRequests, l.pullRequestErr
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
	application := createManualRunTestApplication(t, service, db, repositoryID, "事件触发应用")
	workflow, err := service.GetWorkflow(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	workflow.Workflow.Source.Config.Branch = "test"
	workflow.Workflow.Source.Config.Events = []string{"push", "pr"}
	if _, err := service.SaveWorkflow(context.Background(), application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name,
		Revision: workflow.Workflow.Revision, Activate: true,
		Source: workflow.Workflow.Source, Stages: workflow.Workflow.Stages,
	}); err != nil {
		t.Fatal(err)
	}

	application, run, err := service.SyncApplication(context.Background(), application.ID, "manual_sync")
	if err != nil {
		t.Fatalf("仓库可读取时不应检查失败: %v", err)
	}
	if run != nil {
		t.Fatalf("首次读取 Push 分支只应建立基线: %+v", run)
	}
	var link model.ApplicationRepository
	if err := db.First(&link, "application_id = ?", application.ID).Error; err != nil {
		t.Fatal(err)
	}
	if application.SyncStatus != model.ApplicationSyncSynced || link.LastObservedCommit != "test-commit" {
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
	var link model.ApplicationRepository
	if loadErr := db.First(&link, "application_id = ?", application.ID).Error; loadErr != nil {
		t.Fatal(loadErr)
	}
	if err != nil || run != nil || link.LastObservedCommit != "first-commit" {
		t.Fatalf("首次检查没有正确建立基线: application=%+v run=%+v err=%v", application, run, err)
	}
	lister.refs = repository.RefResult{Branches: []repository.GitRef{{Name: "main", SHA: "second-commit"}}}

	application, run, err = service.SyncApplication(context.Background(), application.ID, "poll")
	if err != nil {
		t.Fatalf("定时检查 Push 变化失败: %v", err)
	}
	if application.SyncStatus != model.ApplicationSyncChanged || run == nil || run.Trigger != "poll_push" ||
		run.Ref != "refs/heads/main" || run.CommitSHA != "second-commit" || run.Status != model.PipelineRunRunning ||
		run.CurrentNodeID != "build" || run.ExecutionJobID == "" {
		t.Fatalf("Push 变化没有自动提交构建任务: application=%+v run=%+v", application, run)
	}
	assertPipelineBuildJob(t, db, run.ExecutionJobID)
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
	application := createManualRunTestApplication(t, service, db, repositoryID, "分支尚未创建")
	workflow, err := service.GetWorkflow(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	workflow.Workflow.Source.Config.Branch = "test"
	workflow.Workflow.Source.Config.Events = []string{"push"}
	if _, err := service.SaveWorkflow(context.Background(), application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name,
		Revision: workflow.Workflow.Revision, Activate: true,
		Source: workflow.Workflow.Source, Stages: workflow.Workflow.Stages,
	}); err != nil {
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
	workflow.Source.Config.Branch = "feature/*"
	workflow.Source.Config.Events = []string{"push", "pr", "tag"}
	workflow.Source.Config.TagPattern = "v*"
	workflow.Source.Config.PRTargetPattern = "feature/*"
	workflow.Source.Config.PRSourcePattern = "*"
	workflow.Source.Config.PRActions = []string{"opened", "updated", "merged"}
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
		if runs[i].CurrentNodeID != "build" || runs[i].ExecutionJobID == "" {
			t.Fatalf("代码变化没有从阶段式流水线的构建任务开始: %+v", runs[i])
		}
		assertPipelineBuildJob(t, db, runs[i].ExecutionJobID)
	}
}

func TestSyncApplicationDetectsMergedPullRequestWithoutHistoricalReplay(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	lister := &pipelineRefLister{refs: repository.RefResult{
		Branches: []repository.GitRef{{Name: "main", SHA: "base-1"}},
		PullRequests: []repository.PullRequestRef{
			{Number: 9, Ref: "refs/pull/9/head", SHA: "historical-merge", SourceBranch: "feature/old", TargetBranch: "main", State: "merged", Action: "merged"},
			{Number: 12, Ref: "refs/pull/12/head", SHA: "merge-12", SourceBranch: "feature/payment", TargetBranch: "main", State: "open", Action: "opened"},
		},
	}}
	service.repositories = repository.NewService(db, secrets, credential.NewService(db, secrets), lister, 4)
	application := createManualRunTestApplication(t, service, db, repositoryID, "PR 合并轮询")
	var workflow model.ReleaseWorkflow
	if err := db.First(&workflow, "application_id = ?", application.ID).Error; err != nil {
		t.Fatal(err)
	}
	workflow.Source.Config.Events = []string{"pr"}
	workflow.Source.Config.PRSourcePattern = "*"
	workflow.Source.Config.PRTargetPattern = "main"
	workflow.Source.Config.PRActions = []string{"merged"}
	workflow.IsActive = true
	if err := db.Save(&workflow).Error; err != nil {
		t.Fatal(err)
	}

	_, run, err := service.SyncApplication(context.Background(), application.ID, "manual_sync")
	if err != nil || run != nil {
		t.Fatalf("首次基线不得重放历史合并: run=%+v err=%v", run, err)
	}
	var baseline []model.ApplicationRepositoryObservation
	if err := db.Order("ref ASC").Find(&baseline).Error; err != nil {
		t.Fatal(err)
	}
	if len(baseline) != 2 || baseline[0].Action != "opened" || baseline[1].Action != "merged" {
		t.Fatalf("PR 动作基线不完整: %+v", baseline)
	}

	lister.refs = repository.RefResult{
		Branches: []repository.GitRef{{Name: "main", SHA: "merge-12"}},
		PullRequests: []repository.PullRequestRef{
			{Number: 12, Ref: "refs/pull/12/head", SHA: "merge-12", SourceBranch: "feature/payment", TargetBranch: "main", State: "merged", Action: "merged"},
		},
	}
	_, run, err = service.SyncApplication(context.Background(), application.ID, "poll")
	if err != nil {
		t.Fatalf("PR 合并状态转换检测失败: %v", err)
	}
	if run == nil || run.Trigger != "poll_pr" || run.TriggerAction != "merged" ||
		run.Ref != "refs/pull/12/head" || run.CommitSHA != "merge-12" || run.TargetBranch != "main" {
		t.Fatalf("PR 合并运行快照错误: %+v", run)
	}
	var observation model.ApplicationRepositoryObservation
	if err := db.First(&observation, "ref = ?", "refs/pull/12/head").Error; err != nil || observation.Action != "merged" {
		t.Fatalf("PR 合并动作游标没有保存: observation=%+v err=%v", observation, err)
	}
	lister.refs.PullRequests = append(lister.refs.PullRequests, repository.PullRequestRef{
		Number: 9, Ref: "refs/pull/9/head", SHA: "historical-merge",
		SourceBranch: "feature/old", TargetBranch: "main", State: "merged", Action: "merged",
	})
	_, duplicate, err := service.SyncApplication(context.Background(), application.ID, "poll")
	if err != nil || duplicate != nil {
		t.Fatalf("相同或重新进入最近窗口的历史合并不应重复执行: run=%+v err=%v", duplicate, err)
	}
}

func TestSyncApplicationDeduplicatesMergedPullRequestAndTargetPush(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	lister := &pipelineRefLister{refs: repository.RefResult{
		Branches: []repository.GitRef{{Name: "main", SHA: "base-1"}},
		PullRequests: []repository.PullRequestRef{{
			Number: 12, Ref: "refs/pull/12/head", SHA: "head-12",
			SourceBranch: "feature/payment", TargetBranch: "main", State: "open", Action: "opened",
		}},
	}}
	service.repositories = repository.NewService(db, secrets, credential.NewService(db, secrets), lister, 4)
	application := createManualRunTestApplication(t, service, db, repositoryID, "PR Push 去重")
	var workflow model.ReleaseWorkflow
	if err := db.First(&workflow, "application_id = ?", application.ID).Error; err != nil {
		t.Fatal(err)
	}
	workflow.Source.Config.Events = []string{"push", "pr"}
	workflow.Source.Config.Branch = "main"
	workflow.Source.Config.PRSourcePattern = "*"
	workflow.Source.Config.PRTargetPattern = "main"
	workflow.Source.Config.PRActions = []string{"merged"}
	workflow.IsActive = true
	if err := db.Save(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	if _, run, err := service.SyncApplication(context.Background(), application.ID, "manual_sync"); err != nil || run != nil {
		t.Fatalf("首次检查只应建立基线: run=%+v err=%v", run, err)
	}

	lister.refs = repository.RefResult{
		Branches: []repository.GitRef{{Name: "main", SHA: "merge-12"}},
		PullRequests: []repository.PullRequestRef{{
			Number: 12, Ref: "refs/pull/12/head", SHA: "merge-12",
			SourceBranch: "feature/payment", TargetBranch: "main", State: "merged", Action: "merged",
		}},
	}
	if _, _, err := service.SyncApplication(context.Background(), application.ID, "poll"); err != nil {
		t.Fatalf("合并与目标分支 Push 去重失败: %v", err)
	}
	var runs []model.PipelineRun
	if err := db.Where("application_id = ?", application.ID).Find(&runs).Error; err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].CommitSHA != "merge-12" {
		t.Fatalf("同一合并提交创建了重复流水线: %+v", runs)
	}
	var observation model.ApplicationRepositoryObservation
	if err := db.First(&observation, "ref = ?", "refs/pull/12/head").Error; err != nil || observation.Action != "merged" {
		t.Fatalf("去重后仍应推进 PR 动作游标: observation=%+v err=%v", observation, err)
	}
}

func TestSyncApplicationContinuesPushAndTagWhenPullRequestAPIFails(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	lister := &pipelineRefLister{refs: repository.RefResult{
		Branches: []repository.GitRef{{Name: "main", SHA: "main-1"}},
		Tags:     []repository.GitRef{{Name: "v1.0.0", SHA: "tag-1"}},
		PullRequests: []repository.PullRequestRef{{
			Number: 12, Ref: "refs/pull/12/head", SHA: "pr-1",
			SourceBranch: "feature/private", TargetBranch: "main", State: "open", Action: "opened",
		}},
	}}
	service.repositories = repository.NewService(db, secrets, credential.NewService(db, secrets), lister, 4)
	application := createManualRunTestApplication(t, service, db, repositoryID, "PR API 部分失败")
	var workflow model.ReleaseWorkflow
	if err := db.First(&workflow, "application_id = ?", application.ID).Error; err != nil {
		t.Fatal(err)
	}
	workflow.Source.Config.Events = []string{"push", "pr", "tag"}
	workflow.Source.Config.Branch = "main"
	workflow.Source.Config.TagPattern = "v*"
	workflow.Source.Config.PRSourcePattern = "*"
	workflow.Source.Config.PRTargetPattern = "main"
	workflow.Source.Config.PRActions = []string{"opened", "updated"}
	workflow.IsActive = true
	if err := db.Save(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	if _, run, err := service.SyncApplication(context.Background(), application.ID, "manual_sync"); err != nil || run != nil {
		t.Fatalf("首次检查只应建立基线: run=%+v err=%v", run, err)
	}

	lister.pullRequestErr = errors.New("provider API returned 401")
	lister.refs = repository.RefResult{
		Branches: []repository.GitRef{{Name: "main", SHA: "main-2"}},
		Tags: []repository.GitRef{
			{Name: "v1.0.0", SHA: "tag-1"},
			{Name: "v1.1.0", SHA: "tag-2"},
		},
		// 该 Ref 的 Commit 恰好与 main 相同，也不能据此猜测 PR 源分支或目标分支。
		PullRequests: []repository.PullRequestRef{{Number: 13, Ref: "refs/pull/13/head", SHA: "main-2"}},
	}
	updated, _, err := service.SyncApplication(context.Background(), application.ID, "poll")
	if err != nil {
		t.Fatalf("PR API 失败不应阻断 Push/Tag 轮询: %v", err)
	}
	if updated.SyncStatus != model.ApplicationSyncChanged || !strings.Contains(updated.SyncMessage, "PR/MR 同步失败") {
		t.Fatalf("部分失败状态不可诊断: %+v", updated)
	}
	var runs []model.PipelineRun
	if err := db.Where("application_id = ?", application.ID).Find(&runs).Error; err != nil {
		t.Fatal(err)
	}
	triggers := map[string]int{}
	for i := range runs {
		triggers[runs[i].Trigger]++
	}
	if len(runs) != 2 || triggers["poll_push"] != 1 || triggers["poll_tag"] != 1 || triggers["poll_pr"] != 0 {
		t.Fatalf("PR API 失败时流水线触发错误: %+v", runs)
	}
	var preserved int64
	if err := db.Model(&model.ApplicationRepositoryObservation{}).
		Where("event = ? AND ref = ?", "pr", "refs/pull/12/head").Count(&preserved).Error; err != nil || preserved != 1 {
		t.Fatalf("PR API 失败时丢失已有监听游标: count=%d err=%v", preserved, err)
	}
}

func TestSyncApplicationMarksPullRequestOnlyFailureWithoutGuessingBranches(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	lister := &pipelineRefLister{
		refs: repository.RefResult{
			Branches:     []repository.GitRef{{Name: "main", SHA: "same-commit"}},
			PullRequests: []repository.PullRequestRef{{Number: 7, Ref: "refs/pull/7/head", SHA: "same-commit"}},
		},
		pullRequestErr: errors.New("provider API unavailable"),
	}
	service.repositories = repository.NewService(db, secrets, credential.NewService(db, secrets), lister, 4)
	application := createManualRunTestApplication(t, service, db, repositoryID, "PR API 完全失败")
	var workflow model.ReleaseWorkflow
	if err := db.First(&workflow, "application_id = ?", application.ID).Error; err != nil {
		t.Fatal(err)
	}
	workflow.Source.Config.Events = []string{"pr"}
	workflow.Source.Config.PRSourcePattern = "*"
	workflow.Source.Config.PRTargetPattern = "main"
	workflow.Source.Config.PRActions = []string{"opened", "updated"}
	workflow.IsActive = true
	if err := db.Save(&workflow).Error; err != nil {
		t.Fatal(err)
	}

	updated, run, err := service.SyncApplication(context.Background(), application.ID, "poll")
	if err != nil || run != nil {
		t.Fatalf("PR API 失败不应触发缺少元数据的 PR Ref: run=%+v err=%v", run, err)
	}
	if updated.SyncStatus != model.ApplicationSyncFailed || !strings.Contains(updated.SyncMessage, "API 令牌") {
		t.Fatalf("PR API 失败状态不可诊断: %+v", updated)
	}
	var observations int64
	if err := db.Model(&model.ApplicationRepositoryObservation{}).Where("event = ?", "pr").Count(&observations).Error; err != nil || observations != 0 {
		t.Fatalf("缺少可靠分支元数据的 PR 被建立监听游标: count=%d err=%v", observations, err)
	}
}
