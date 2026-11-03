package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/credential"
	"edo/internal/model"
	"edo/internal/repository"
)

type pipelineRefLister struct {
	refs           repository.RefResult
	refErr         error
	pullRequestErr error
}

func (l pipelineRefLister) ListRefs(context.Context, model.GitRepository, string) (repository.RefResult, error) {
	return l.refs, l.refErr
}

func TestSyncFailureKeepsSafeMessageAndPreservesPollingCursor(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	lister := &pipelineRefLister{refs: repository.RefResult{Branches: []repository.GitRef{{Name: "main", SHA: "first-commit"}}}}
	service.repositories = repository.NewService(db, secrets, credential.NewService(db, secrets), lister, 4)
	application := createManualRunTestApplication(t, service, db, repositoryID, "poll_recovery")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	if _, run, err := service.SyncApplication(context.Background(), application.ID, "poll"); err != nil || run != nil {
		t.Fatalf("建立基线失败: run=%+v err=%v", run, err)
	}
	lister.refErr = &url.Error{Op: "Get", URL: "https://private.invalid/repo.git/info/refs", Err: io.EOF}
	if _, run, err := service.SyncApplication(context.Background(), application.ID, "poll"); !errors.Is(err, io.EOF) || run != nil {
		t.Fatalf("引用不可读时不能创建运行，内部仍应保留诊断: run=%+v err=%v", run, err)
	}
	var stored model.Application
	if err := db.First(&stored, "uuid = ?", application.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.SyncMessage != "代码仓库暂时不可用，将在下次检查时重试" || stored.LastCheckedAt == nil || stored.SyncStatus != model.ApplicationSyncFailed {
		t.Fatalf("没有保存安全的同步失败状态: %+v", stored)
	}
	var observations []model.ApplicationRepositoryObservation
	if err := db.Find(&observations).Error; err != nil || len(observations) != 1 || observations[0].CommitSHA != "first-commit" {
		t.Fatalf("读取失败不应改变原监听游标: observations=%+v err=%v", observations, err)
	}
	lister.refErr = nil
	lister.refs.Branches[0].SHA = "second-commit"
	if _, run, err := service.SyncApplication(context.Background(), application.ID, "poll"); err != nil || run == nil || run.CommitSHA != "second-commit" {
		t.Fatalf("恢复后未正常触发真实变更: run=%+v err=%v", run, err)
	}
	if _, run, err := service.SyncApplication(context.Background(), application.ID, "poll"); err != nil || run != nil {
		t.Fatalf("恢复后重复触发了相同变更: run=%+v err=%v", run, err)
	}
	var count int64
	if err := db.Model(&model.PipelineRun{}).Where("application_id = ?", application.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("应只产生一条真实变更运行: count=%d err=%v", count, err)
	}
	service.markSyncFailure(context.Background(), application.ID, errors.New("credential failure: private diagnostic"))
	if err := db.First(&stored, "uuid = ?", application.ID).Error; err != nil || stored.SyncMessage != "代码仓库检查失败，请检查仓库地址或凭据" {
		t.Fatalf("永久错误不应暴露内部诊断: message=%q err=%v", stored.SyncMessage, err)
	}
}

func (l pipelineRefLister) ListPullRequests(context.Context, model.GitRepository, string, string) ([]repository.PullRequestRef, error) {
	return l.refs.PullRequests, l.pullRequestErr
}

type pipelineAPIPullRequestLister struct {
	pipelineRefLister
	client *repository.GitClient
}

func (l pipelineAPIPullRequestLister) ListPullRequests(ctx context.Context, item model.GitRepository, cloneCredential, apiCredential string) ([]repository.PullRequestRef, error) {
	return l.client.ListPullRequests(ctx, item, cloneCredential, apiCredential)
}

func TestSyncApplicationPreservesReliablePullRequestsDuringPartialAPIResults(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	var partial atomic.Bool
	var openRequests, mergedRequests atomic.Int32
	sha := func(value string) string { return strings.Repeat(value, 40) }
	apiPull := func(number int, commit string) map[string]any {
		return map[string]any{
			"number": number, "state": "open",
			"head": map[string]string{"ref": "feature/test", "sha": commit},
			"base": map[string]string{"ref": "main"},
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v5/repos/team/app/pulls" ||
			request.URL.Query().Get("page") != "1" || request.URL.Query().Get("per_page") != "100" {
			t.Errorf("PR 测试 API 收到非预期请求: method=%s path=%s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		rows := make([]map[string]any, 0)
		switch request.URL.Query().Get("state") {
		case "open":
			openRequests.Add(1)
			rows = []map[string]any{apiPull(7, sha("7")), apiPull(8, sha("8")), apiPull(12, sha("a"))}
			if partial.Load() {
				rows = []map[string]any{apiPull(7, sha("e")), apiPull(12, sha("b"))}
			}
		case "merged":
			mergedRequests.Add(1)
			if partial.Load() {
				// 平台确认同编号已合并，但没有给出合并提交；旧 open 的 head 不能继续触发。
				rows = []map[string]any{{
					"number": 7, "state": "merged", "merged_at": "2026-11-03T23:27:29+08:00",
					"head": map[string]string{"ref": "feature/test", "sha": sha("e")},
					"base": map[string]string{"ref": "main", "sha": sha("c")},
				}}
			}
		default:
			t.Errorf("PR 测试 API 收到错误状态: %q", request.URL.Query().Get("state"))
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(rows); err != nil {
			t.Errorf("写入 PR 测试响应失败: %v", err)
		}
	}))
	defer server.Close()
	lister := pipelineAPIPullRequestLister{
		pipelineRefLister: pipelineRefLister{refs: repository.RefResult{Branches: []repository.GitRef{{Name: "main", SHA: sha("c")}}}},
		client:            repository.NewGitClient(config.Git{Timeout: time.Second}),
	}
	repositories := repository.NewService(db, secrets, credential.NewService(db, secrets), lister, 4)
	if _, _, err := repositories.Update(context.Background(), "admin", repositoryID, repository.Input{
		Name: "partial-pr-api", Provider: model.GitProviderGitee, CloneURL: server.URL + "/team/app.git",
		DefaultBranch: "main", AuthType: model.GitAuthNone, AllowInsecureHTTP: true,
	}); err != nil {
		t.Fatalf("设置本地 PR API 仓库失败: %v", err)
	}
	service.repositories = repositories
	application := createManualRunTestApplication(t, service, db, repositoryID, "partial_pr_api_poll")
	workflow, err := service.GetWorkflow(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := workflow.Workflow.Source
	source.Config.Events, source.Config.PRActions = []string{"pr"}, []string{"opened", "updated", "merged"}
	source.Config.PRSourcePattern, source.Config.PRTargetPattern = "*", "main"
	if _, err := service.SaveWorkflow(context.Background(), application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name, Revision: workflow.Workflow.Revision,
		Activate: true, Source: source, Stages: workflow.Workflow.Stages,
	}); err != nil {
		t.Fatalf("启用 PR 测试流水线失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if updated, run, err := service.SyncApplication(ctx, application.ID, "manual_sync"); err != nil || run != nil || updated.SyncStatus != model.ApplicationSyncSynced {
		t.Fatalf("首次完整 API 结果应只建立基线: application=%+v run=%+v err=%v", updated, run, err)
	}
	var link model.ApplicationRepository
	if err := db.First(&link, "application_id = ?", application.ID).Error; err != nil {
		t.Fatal(err)
	}
	observation := func(ref string) model.ApplicationRepositoryObservation {
		t.Helper()
		var stored model.ApplicationRepositoryObservation
		if err := db.Where("application_repository_id = ? AND event = ? AND ref = ?", link.ID, "pr", ref).First(&stored).Error; err != nil {
			t.Fatalf("读取 PR 游标失败: ref=%s err=%v", ref, err)
		}
		return stored
	}
	conflicted, omitted := observation("refs/pull/7/head"), observation("refs/pull/8/head")
	partial.Store(true)
	updated, run, err := service.SyncApplication(ctx, application.ID, "poll")
	if err != nil || run == nil || run.Trigger != "poll_pr" || run.TriggerAction != "updated" ||
		run.Ref != "refs/pull/12/head" || run.CommitSHA != sha("b") || run.TargetBranch != "main" ||
		run.Status != model.PipelineRunRunning || run.CurrentNodeID != "build" || run.ExecutionJobID == "" {
		t.Fatalf("部分 API 结果未仅执行可靠 open 变化: run=%+v err=%v", run, err)
	}
	if updated.SyncStatus != model.ApplicationSyncChanged || !strings.Contains(updated.SyncMessage, "PR/MR 同步失败") {
		t.Fatalf("可靠事件执行后仍须保留部分失败诊断: %+v", updated)
	}
	assertPipelineBuildJob(t, db, run.ExecutionJobID)
	assertPreserved := func() {
		t.Helper()
		if got := observation(conflicted.Ref); !reflect.DeepEqual(got, conflicted) {
			t.Fatalf("缺少 merge SHA 的同编号 PR 不得沿用旧 open 或推进游标: before=%+v after=%+v", conflicted, got)
		}
		if got := observation(omitted.Ref); !reflect.DeepEqual(got, omitted) {
			t.Fatalf("部分结果中遗漏的 PR 游标必须完整保留: before=%+v after=%+v", omitted, got)
		}
	}
	assertPreserved()
	updated, duplicate, err := service.SyncApplication(ctx, application.ID, "poll")
	if err != nil || duplicate != nil || updated.SyncStatus != model.ApplicationSyncFailed || !strings.Contains(updated.SyncMessage, "PR/MR 同步失败") {
		t.Fatalf("相同部分结果不得重放，失败诊断也不得被清除: application=%+v run=%+v err=%v", updated, duplicate, err)
	}
	assertPreserved()
	var runs []model.PipelineRun
	if err := db.Where("application_id = ?", application.ID).Find(&runs).Error; err != nil || len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("可靠事件只能建立一个运行，冲突及遗漏记录均不得执行: runs=%+v err=%v", runs, err)
	}
	if openRequests.Load() != 3 || mergedRequests.Load() != 3 {
		t.Fatalf("每次同步必须经过真实平台 API 两种查询: open=%d merged=%d", openRequests.Load(), mergedRequests.Load())
	}
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
	application := createManualRunTestApplication(t, service, db, repositoryID, "event_trigger_app")
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
	application := createManualRunTestApplication(t, service, db, repositoryID, "scheduled_branch_check")
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

func TestSyncApplicationPollsEachMatchingWorkflowIndependently(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	lister := &pipelineRefLister{refs: repository.RefResult{
		Branches: []repository.GitRef{{Name: "main", SHA: "baseline-commit"}},
	}}
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets), lister, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "multi_workflow_poll")
	first := application.Workflow
	if first == nil {
		t.Fatal("新应用缺少默认流水线")
	}
	firstSource, firstStages := first.Source, cloneWorkflowStages(first.Stages)
	firstSource.Config.Events = []string{"push"}
	if _, err := service.SaveApplicationWorkflow(context.Background(), application.ID, first.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: "主分支构建",
		Revision: first.Revision, Activate: true, Source: firstSource, Stages: firstStages,
	}); err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateApplicationWorkflow(context.Background(), application.ID, "admin", WorkflowCreateInput{Name: "主分支检查"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveApplicationWorkflow(context.Background(), application.ID, second.Workflow.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: second.Workflow.Name,
		Revision: second.Workflow.Revision, Activate: true, Source: firstSource, Stages: cloneWorkflowStages(firstStages),
	}); err != nil {
		t.Fatal(err)
	}

	if _, run, err := service.SyncApplication(context.Background(), application.ID, "manual_sync"); err != nil || run != nil {
		t.Fatalf("首次检查应只为两条流水线分别建立基线: run=%+v err=%v", run, err)
	}
	var baselines []model.ApplicationRepositoryObservation
	if err := db.Find(&baselines).Error; err != nil || len(baselines) != 2 || baselines[0].WorkflowID == baselines[1].WorkflowID {
		t.Fatalf("两条流水线的监听基线没有隔离: observations=%+v err=%v", baselines, err)
	}

	lister.refs = repository.RefResult{Branches: []repository.GitRef{{Name: "main", SHA: "changed-commit"}}}
	if _, _, err := service.SyncApplication(context.Background(), application.ID, "poll"); err != nil {
		t.Fatalf("轮询同一应用的多条流水线失败: %v", err)
	}
	var runs []model.PipelineRun
	if err := db.Where("application_id = ? AND commit_sha = ?", application.ID, "changed-commit").Find(&runs).Error; err != nil {
		t.Fatal(err)
	}
	workflowIDs := map[string]bool{}
	for i := range runs {
		workflowIDs[runs[i].WorkflowID] = true
	}
	if len(runs) != 2 || len(workflowIDs) != 2 {
		t.Fatalf("轮询变化没有分别触发两条流水线: %+v", runs)
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
	application := createManualRunTestApplication(t, service, db, repositoryID, "branch_not_created")
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
	application := createManualRunTestApplication(t, service, db, repositoryID, "custom_branch_poll")
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
	application := createManualRunTestApplication(t, service, db, repositoryID, "pr_merge_poll")
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
	application := createManualRunTestApplication(t, service, db, repositoryID, "pr_push_dedup")
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
	application := createManualRunTestApplication(t, service, db, repositoryID, "pr_api_partial_failure")
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
	application := createManualRunTestApplication(t, service, db, repositoryID, "pr_api_total_failure")
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
