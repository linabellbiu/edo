package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"zrt/internal/model"
)

func TestPipelineRunLogsSupportCursorAndBuildOutput(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	now := time.Now().UTC()
	application := model.Application{
		ID: uuid.NewString(), Name: "日志测试应用", RepositoryID: repositoryID, Branch: "main",
		PollIntervalSeconds: 60, SyncStatus: model.ApplicationSyncIdle, IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&application).Error; err != nil {
		t.Fatalf("创建日志测试应用失败: %v", err)
	}
	run := model.PipelineRun{
		ID: uuid.NewString(), ApplicationID: application.ID, Trigger: "manual", Ref: "refs/heads/main",
		CommitSHA: strings.Repeat("a", 40), Status: model.PipelineRunRunning, Stage: "build",
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建日志测试运行失败: %v", err)
	}

	service.appendRunLog(context.Background(), run.ID, "checkout", "info", "开始检出代码")
	writer := service.newBuildLogWriter(context.Background(), run.ID, "build")
	if _, err := writer.Write([]byte("#1 FROM alpine\n#1 DONE 0.2s\n")); err != nil {
		t.Fatalf("写入构建输出失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭构建输出失败: %v", err)
	}

	first, status, err := service.ListRunLogs(context.Background(), run.ID, 0, 1)
	if err != nil || status != model.PipelineRunRunning || len(first) != 1 || first[0].Message != "开始检出代码" {
		t.Fatalf("首批流水线日志错误: logs=%+v status=%s err=%v", first, status, err)
	}
	rest, _, err := service.ListRunLogs(context.Background(), run.ID, first[0].ID, 20)
	if err != nil || len(rest) != 1 || rest[0].Level != "output" || !strings.Contains(rest[0].Message, "DONE") {
		t.Fatalf("游标后的构建日志错误: logs=%+v err=%v", rest, err)
	}
	listed, err := service.ListExecutionLogs(context.Background(), ExecutionLogFilter{Limit: 20, Query: "日志测试", Level: "output"})
	if err != nil || len(listed) != 1 || listed[0].ApplicationName != application.Name || listed[0].PipelineRunID != run.ID {
		t.Fatalf("日志中心查询结果错误: logs=%+v err=%v", listed, err)
	}

	if err := service.DeleteRun(context.Background(), run.ID); err != nil {
		t.Fatalf("删除流水线运行失败: %v", err)
	}
	var count int64
	if err := db.Model(&model.PipelineRunLog{}).Where("pipeline_run_id = ?", run.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("删除运行后仍残留日志: count=%d err=%v", count, err)
	}
}
