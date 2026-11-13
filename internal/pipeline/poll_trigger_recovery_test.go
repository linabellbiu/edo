package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	"edo/internal/model"
	"edo/internal/repository"
)

func TestPollTriggerCreationFailurePreservesEventForNextCheck(t *testing.T) {
	for _, scenario := range []string{"新增标签", "移动标签", "新增分支", "更新分支", "合并请求"} {
		t.Run(scenario, func(t *testing.T) {
			service, db, application, client := tagNormalizationFixture(t)
			ctx := context.Background()
			workflow, err := service.GetWorkflow(ctx, application.ID)
			if err != nil {
				t.Fatal(err)
			}
			source := workflow.Workflow.Source
			source.Config.Events = []string{"tag", "push", "pr"}
			source.Config.Branch = "*"
			source.Config.PRActions = []string{"opened", "updated", "merged"}
			if _, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
				SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name,
				Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: workflow.Workflow.Stages,
			}); err != nil {
				t.Fatal(err)
			}
			first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
			client.refs = repository.RefResult{
				Tags:     []repository.GitRef{{Name: "v1", SHA: first}},
				Branches: []repository.GitRef{{Name: "main", SHA: first}},
				PullRequests: []repository.PullRequestRef{{
					Ref: "refs/pull/1/head", SHA: first, SourceBranch: "feature", TargetBranch: "main", Action: "opened",
				}},
			}
			if _, run, err := service.SyncApplication(ctx, application.ID, "poll"); err != nil || run != nil {
				t.Fatalf("首次检查应只建立基线: run=%v err=%v", run, err)
			}
			event, ref := "tag", "refs/tags/v1"
			switch scenario {
			case "新增标签":
				ref = "refs/tags/v2"
				client.refs.Tags = append(client.refs.Tags, repository.GitRef{Name: "v2", SHA: second})
			case "移动标签":
				client.refs.Tags[0].SHA = second
			case "新增分支":
				event, ref = "push", "refs/heads/feature"
				client.refs.Branches = append(client.refs.Branches, repository.GitRef{Name: "feature", SHA: second})
			case "更新分支":
				event, ref = "push", "refs/heads/main"
				client.refs.Branches[0].SHA = second
			case "合并请求":
				event, ref = "pr", "refs/pull/1/head"
				client.refs.PullRequests[0].Action = "merged"
				second = first
			}
			var before model.ApplicationRepositoryObservation
			queryErr := db.First(&before, "workflow_id = ? AND event = ? AND ref = ?", workflow.Workflow.ID, event, ref).Error
			if queryErr != nil && !errors.Is(queryErr, gorm.ErrRecordNotFound) {
				t.Fatal(queryErr)
			}
			callback := "test:poll_trigger_creation_failure"
			blocked := true
			if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if blocked && tx.Statement.Table == "build_plans" {
					tx.AddError(errors.New("仅限系统日志的内部诊断"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
			if _, run, err := service.SyncApplication(ctx, application.ID, "poll"); !errors.Is(err, ErrRepositoryTriggerFailed) || run != nil {
				t.Fatalf("创建运行失败不得当作检查成功: run=%v err=%v", run, err)
			}
			var after model.ApplicationRepositoryObservation
			err = db.First(&after, "workflow_id = ? AND event = ? AND ref = ?", workflow.Workflow.ID, event, ref).Error
			if errors.Is(queryErr, gorm.ErrRecordNotFound) {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					t.Fatalf("新事件尚未登记运行就推进了游标: observation=%+v err=%v", after, err)
				}
			} else if err != nil || after.CommitSHA != before.CommitSHA || after.Action != before.Action {
				t.Fatalf("创建失败覆盖了已有事件游标: before=%+v after=%+v err=%v", before, after, err)
			}
			var stored model.Application
			if err := db.First(&stored, "uuid = ?", application.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.SyncStatus != model.ApplicationSyncFailed || !strings.Contains(stored.SyncMessage, "流水线") || strings.Contains(stored.SyncMessage, "内部诊断") {
				t.Fatalf("没有展示安全的触发失败状态: status=%s message=%s", stored.SyncStatus, stored.SyncMessage)
			}
			assertTagCounts(t, db, 0, 0, 0)
			blocked = false
			_, run, err := service.SyncApplication(ctx, application.ID, "poll")
			if err != nil || run == nil || run.CommitSHA != second || run.Ref != ref {
				t.Fatalf("恢复后未自动触发原事件: run=%+v err=%v", run, err)
			}
			if run.Status != model.PipelineRunRunning || run.ExecutionJobID == "" {
				t.Fatalf("运行没有真实投递执行: %+v", run)
			}
			runs, jobs, outboxes := tagCounts(t, db)
			for range 2 {
				if _, duplicate, err := service.SyncApplication(ctx, application.ID, "poll"); err != nil || duplicate != nil {
					t.Fatalf("同一事件被重复触发: run=%v err=%v", duplicate, err)
				}
			}
			assertTagCounts(t, db, runs, jobs, outboxes)
		})
	}
}

func TestPollRunRegistrationRollbackDoesNotConsumeTag(t *testing.T) {
	service, db, application, client := tagNormalizationFixture(t)
	ctx := context.Background()
	if _, run, err := service.SyncApplication(ctx, application.ID, "poll"); err != nil || run != nil {
		t.Fatalf("建立空仓库基线失败: run=%v err=%v", run, err)
	}
	client.refs.Tags = []repository.GitRef{{Name: "v1", SHA: strings.Repeat("a", 40)}}
	callback := "test:poll_repository_registration_failure"
	blocked := true
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if blocked && tx.Statement.Table == "pipeline_run_repositories" {
			tx.AddError(errors.New("模拟运行来源登记失败"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	if _, run, err := service.SyncApplication(ctx, application.ID, "poll"); !errors.Is(err, ErrRepositoryTriggerFailed) || run != nil {
		t.Fatalf("登记失败没有返回安全提示: run=%v err=%v", run, err)
	}
	assertTagCounts(t, db, 0, 0, 0)
	var observations int64
	if err := db.Model(&model.ApplicationRepositoryObservation{}).Count(&observations).Error; err != nil || observations != 0 {
		t.Fatalf("事务回滚后仍消费了标签事件: count=%d err=%v", observations, err)
	}
	blocked = false
	if _, run, err := service.SyncApplication(ctx, application.ID, "poll"); err != nil || run == nil {
		t.Fatalf("事务恢复后没有重新投递标签: run=%v err=%v", run, err)
	}
	runs, jobs, outboxes := tagCounts(t, db)
	if _, duplicate, err := service.SyncApplication(ctx, application.ID, "poll"); err != nil || duplicate != nil {
		t.Fatalf("成功投递后发生重复运行: run=%v err=%v", duplicate, err)
	}
	assertTagCounts(t, db, runs, jobs, outboxes)
}

func TestPollTriggerCanceledPreparationKeepsNewTagRetryable(t *testing.T) {
	service, db, application, client := tagNormalizationFixture(t)
	ctx := context.Background()
	if _, _, err := service.SyncApplication(ctx, application.ID, "poll"); err != nil {
		t.Fatal(err)
	}
	client.refs.Tags = []repository.GitRef{{Name: "v1", SHA: strings.Repeat("a", 40)}}
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	callback := "test:poll_preparation_canceled"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "build_plans" {
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	if _, run, err := service.SyncApplication(canceled, application.ID, "poll"); !errors.Is(err, ErrRepositoryTriggerFailed) || run != nil {
		t.Fatalf("准备阶段取消没有安全返回: run=%v err=%v", run, err)
	}
	var stored model.Application
	if err := db.First(&stored, "uuid = ?", application.ID).Error; err != nil || stored.SyncStatus != model.ApplicationSyncFailed {
		t.Fatalf("请求取消后没有独立保存失败状态: status=%s err=%v", stored.SyncStatus, err)
	}
	if _, run, err := service.SyncApplication(ctx, application.ID, "poll"); err != nil || run == nil {
		t.Fatalf("请求取消导致下一轮把新 Tag 当作基线: run=%v err=%v", run, err)
	}
}
