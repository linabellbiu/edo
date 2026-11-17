package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"edo/internal/model"
)

func TestSuccessfulManualWorkflowRunCanExecuteAgain(t *testing.T) {
	for _, reuseArtifact := range []bool{false, true} {
		name := "rebuild"
		if reuseArtifact {
			name = "original_artifact"
		}
		t.Run(name, func(t *testing.T) {
			service, db, _, repositoryID := newPipelineTestService(t)
			application := createManualRunTestApplication(t, service, db, repositoryID, "rerun_success")
			if err := db.Model(application.Workflow).Update("is_active", true).Error; err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			original := model.PipelineRun{
				ID: "original-success", ApplicationID: application.ID, WorkflowID: application.Workflow.ID,
				WorkflowRevision: application.Workflow.Revision, Trigger: "poll_push",
				Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), CommitMessage: "已成功的固定版本",
				Status: model.PipelineRunSucceeded, Stage: "completed", CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
			}
			if err := db.Create(&original).Error; err != nil {
				t.Fatal(err)
			}
			artifactID := ""
			if reuseArtifact {
				artifact := createPreviousManualArtifact(t, db, application, original.CommitSHA)
				if err := db.Model(&model.BuildRun{}).Where("uuid = ?", artifact.BuildRunID).Update("pipeline_run_id", original.ID).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(artifact).Update("pipeline_run_id", original.ID).Error; err != nil {
					t.Fatal(err)
				}
				artifactID = artifact.ID
			}
			options, err := service.ListRetryRunOptions(context.Background(), original.ID)
			if err != nil || options.Ref != original.Ref || options.CommitSHA != original.CommitSHA || len(options.Artifacts) != map[bool]int{false: 0, true: 1}[reuseArtifact] {
				t.Fatalf("成功运行选项必须保留固定版本和准确原制品: options=%+v err=%v", options, err)
			}
			run, err := service.RetryRunSelection(context.Background(), original.ID, "operator", artifactID)
			if err != nil {
				t.Fatal(err)
			}
			if run.ID == original.ID || run.RetryOfID != original.ID || run.Ref != original.Ref || run.CommitSHA != original.CommitSHA || run.CreatedBy != "operator" {
				t.Fatalf("成功后重新执行必须创建沿用固定版本的新运行: %+v", run)
			}
			if reuseArtifact {
				if run.ArtifactID != artifactID || run.Status != model.PipelineRunAwaitingApproval || run.ExecutionJobID != "" {
					t.Fatalf("复用原制品应跳过构建并保留审核: %+v", run)
				}
			} else if run.Status != model.PipelineRunRunning || run.ExecutionJobID == "" {
				t.Fatalf("重新构建应提交新的真实任务: %+v", run)
			}
			var preserved model.PipelineRun
			if err := db.First(&preserved, "uuid = ?", original.ID).Error; err != nil {
				t.Fatal(err)
			}
			if preserved.Status != original.Status || preserved.Stage != original.Stage || preserved.CommitSHA != original.CommitSHA || preserved.ExecutionJobID != "" {
				t.Fatalf("原成功记录不得被改写: %+v", preserved)
			}
		})
	}
}

func TestSuccessfulRetryRechecksCurrentManualSetting(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "manual_rerun_guard")
	workflow := application.Workflow
	workflow.IsActive = true
	if err := db.Save(workflow).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	original := model.PipelineRun{
		ID: "completed-run", ApplicationID: application.ID, WorkflowID: workflow.ID,
		Status: model.PipelineRunSucceeded, Stage: "completed", Trigger: "manual",
		Ref: "refs/heads/main", CommitSHA: strings.Repeat("b", 40), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	view, err := service.FindRun(context.Background(), original.ID)
	if err != nil || !view.RetryAllowed {
		t.Fatalf("手动流程的成功运行应展示重新执行: view=%+v err=%v", view, err)
	}
	fingerprint, err := service.PreviewRetryRun(context.Background(), original.ID, "operator", "")
	if err != nil {
		t.Fatal(err)
	}
	workflow.Source.Config.Events = []string{"push"}
	if err := db.Save(workflow).Error; err != nil {
		t.Fatal(err)
	}
	page, err := service.QueryRuns(context.Background(), RunQueryOptions{})
	if err != nil || len(page.Runs) != 1 || page.Runs[0].RetryAllowed {
		t.Fatalf("关闭手动执行后列表应立即取消成功记录入口: page=%+v err=%v", page, err)
	}
	if _, err := service.ListRetryRunOptions(context.Background(), original.ID); !errors.Is(err, ErrPipelineRunNotRetryable) {
		t.Fatalf("关闭手动执行后不得读取成功运行的重新执行选项: %v", err)
	}
	if _, err := service.PreviewRetryRun(context.Background(), original.ID, "operator", ""); !errors.Is(err, ErrPipelineRunNotRetryable) {
		t.Fatalf("关闭手动执行后不得预览成功运行重新执行: %v", err)
	}
	if _, err := service.RetryRunSelectionExpected(context.Background(), original.ID, "operator", "", fingerprint); !errors.Is(err, ErrPipelineRunNotRetryable) {
		t.Fatalf("确认后关闭手动执行必须阻止实际重新执行: %v", err)
	}
	var count int64
	if err := db.Model(&model.PipelineRun{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("被拒绝的重新执行不得创建运行: count=%d err=%v", count, err)
	}
	if err := db.Model(&model.Job{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("被拒绝的重新执行不得创建任务: count=%d err=%v", count, err)
	}
}

func TestChainedRetryKeepsArtifactPinnedByPreviousRetry(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "retry_chain_artifact")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	commitSHA := strings.Repeat("6", 40)
	original := model.PipelineRun{
		ID: "original-build-run", ApplicationID: application.ID, WorkflowID: application.Workflow.ID,
		WorkflowRevision: application.Workflow.Revision, Trigger: "manual",
		Ref: "refs/tags/v0.0.8", CommitSHA: commitSHA, CommitMessage: "固定版本",
		Status: model.PipelineRunFailed, Stage: "deploy_failed", Message: "部署失败",
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	stored := createPreviousManualArtifact(t, db, application, commitSHA)
	if err := db.Model(&model.BuildRun{}).Where("uuid = ?", stored.BuildRunID).
		Updates(map[string]any{"pipeline_run_id": original.ID, "ref": original.Ref, "workflow_node_id": "build"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Artifact{}).Where("uuid = ?", stored.ID).Update("pipeline_run_id", original.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PipelineNodeRun{
		ID: uuid.NewString(), PipelineRunID: original.ID, NodeID: "build", StageID: "build", Name: "构建制品",
		Type: model.WorkflowNodeBuild, Status: model.PipelineRunSucceeded, Stage: "completed", Message: "任务已完成",
		ArtifactID: stored.ID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	firstRetry, err := service.RetryRunSelection(context.Background(), original.ID, "operator", stored.ID)
	if err != nil || firstRetry.ArtifactID != stored.ID || firstRetry.RetryOfID != original.ID {
		t.Fatalf("第一次重新执行没有沿用原运行制品: run=%+v err=%v", firstRetry, err)
	}
	// 第一次重新执行同样跳过构建，只登记沿用的制品；它部署失败后再次重新执行。
	if err := db.Where("pipeline_run_id = ?", firstRetry.ID).Delete(&model.PipelineNodeRun{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PipelineNodeRun{
		ID: uuid.NewString(), PipelineRunID: firstRetry.ID, NodeID: "build", StageID: "build", Name: "构建制品",
		Type: model.WorkflowNodeBuild, Status: model.PipelineRunSkipped, Stage: "skipped", Message: "本次使用已选择的制品，跳过重新构建",
		ArtifactID: stored.ID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.PipelineRun{}).Where("uuid = ?", firstRetry.ID).
		Updates(map[string]any{"status": model.PipelineRunFailed, "stage": "deploy_failed", "message": "部署失败"}).Error; err != nil {
		t.Fatal(err)
	}
	options, err := service.ListRetryRunOptions(context.Background(), firstRetry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if options.Ref != original.Ref || options.CommitSHA != original.CommitSHA || len(options.Artifacts) != 1 || options.Artifacts[0].ID != stored.ID {
		t.Fatalf("沿用原制品的运行再次重新执行时必须提供同一个制品: %+v", options)
	}
	secondRetry, err := service.RetryRunSelection(context.Background(), firstRetry.ID, "operator", stored.ID)
	if err != nil || secondRetry.RetryOfID != firstRetry.ID || secondRetry.ArtifactID != stored.ID ||
		secondRetry.Ref != original.Ref || secondRetry.CommitSHA != commitSHA ||
		secondRetry.CurrentNodeID != "approval" || secondRetry.Status != model.PipelineRunAwaitingApproval || secondRetry.ExecutionJobID != "" {
		t.Fatalf("重试链再次沿用原制品时必须跳过构建并保留固定版本: run=%+v err=%v", secondRetry, err)
	}
	var jobs int64
	if err := db.Model(&model.Job{}).Where("payload LIKE ?", "%"+secondRetry.ID+"%").Count(&jobs).Error; err != nil || jobs != 0 {
		t.Fatalf("重试链沿用原制品后仍创建了构建任务: count=%d err=%v", jobs, err)
	}
	foreign := createPreviousManualArtifact(t, db, application, strings.Repeat("7", 40))
	if _, err := service.RetryRunSelection(context.Background(), firstRetry.ID, "operator", foreign.ID); !errors.Is(err, ErrRetryArtifactInvalid) {
		t.Fatalf("重试链不得接受该运行没有固定使用的其他制品: %v", err)
	}
	var preserved model.PipelineRun
	if err := db.First(&preserved, "uuid = ?", firstRetry.ID).Error; err != nil {
		t.Fatal(err)
	}
	if preserved.Status != model.PipelineRunFailed {
		t.Fatalf("重新执行不得覆盖原记录: %+v", preserved)
	}
}
