package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"edo/internal/model"
)

func TestApprovedRetryUsesResolvedSnapshot(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	app := createManualRunTestApplication(t, service, db, repositoryID, "approved_retry")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", app.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	failed := model.PipelineRun{ID: "approved-failed", ApplicationID: app.ID, WorkflowID: app.Workflow.ID,
		Ref: "refs/heads/main", CommitSHA: strings.Repeat("d", 40), Status: model.PipelineRunFailed,
		Stage: "failed", CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&failed).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fingerprint, err := service.PreviewRetryRun(ctx, failed.ID, "operator", "")
	if err != nil || fingerprint == "" {
		t.Fatalf("生成确认指纹失败: %v", err)
	}
	again, err := service.PreviewRetryRun(ctx, failed.ID, "operator", "")
	if err != nil || again != fingerprint {
		t.Fatalf("随机运行ID或时间污染确认指纹: %s %s %v", fingerprint, again, err)
	}
	var count int64
	db.Model(&model.PipelineRun{}).Count(&count)
	if count != 1 {
		t.Fatal("预览不应创建运行")
	}
	var original model.BuildPlan
	if err := db.Where("uuid = ?", workflowTasks(app.Workflow.Stages)[0].Config.BuildPlanID).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&original).Update("dockerfile_path", "Dockerfile.changed").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.RetryRunSelectionExpected(ctx, failed.ID, "operator", "", fingerprint); !errors.Is(err, ErrRetrySnapshotChanged) {
		t.Fatalf("独立构建方案变化应拒绝旧确认: %v", err)
	}
	db.Model(&model.PipelineRun{}).Count(&count)
	if count != 1 {
		t.Fatal("拒绝旧确认后不应产生运行")
	}
	if err := db.Model(&original).Update("dockerfile_path", original.DockerfilePath).Error; err != nil {
		t.Fatal(err)
	}
	refreshed, err := service.PreviewRetryRun(ctx, failed.ID, "operator", "")
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.RetryRunSelectionExpected(ctx, failed.ID, "operator", "", refreshed)
	if err != nil || run == nil || run.ID == failed.ID || run.RetryOfID != failed.ID || run.CommitSHA != failed.CommitSHA {
		t.Fatalf("新确认未创建固定代码版本的重试: %+v %v", run, err)
	}
}
