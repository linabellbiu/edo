package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"edo/internal/database"
	"edo/internal/model"
)

func TestBuildLogRedactionKeepsSecretAcrossChunkBoundary(t *testing.T) {
	secret := []byte("super-secret-token")
	prefix := bytes.Repeat([]byte("x"), buildLogChunkBytes-6)
	writer := &buildLogWriter{
		pending:    append(prefix, secret[:6]...),
		redactions: [][]byte{secret},
	}
	if cut := writer.safeFlushLength(buildLogChunkBytes); cut != len(prefix) {
		t.Fatalf("日志分块会拆开待脱敏内容: cut=%d want=%d", cut, len(prefix))
	}
	redacted := redactLogBytes([]byte("before "+string(secret)+" after"), writer.redactions)
	if bytes.Contains(redacted, secret) || !bytes.Contains(redacted, []byte("[已脱敏]")) {
		t.Fatalf("敏感构建输出没有脱敏: %q", redacted)
	}
}

func TestPipelineRunLogsSupportCursorAndBuildOutput(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	now := time.Now().UTC()
	application := model.Application{
		ID: uuid.NewString(), Name: "日志测试应用", RepositoryID: repositoryID,
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

	var count int64
	if err := db.Model(&model.PipelineRunLog{}).Where("pipeline_run_id = ?", run.ID).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("流水线审计日志没有完整保留: count=%d err=%v", count, err)
	}
}

func TestPipelineRunLogsShareOneRunLevelBudget(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	now := time.Now().UTC()
	application := model.Application{
		ID: uuid.NewString(), Name: "日志配额应用", RepositoryID: repositoryID,
		PollIntervalSeconds: 60, SyncStatus: model.ApplicationSyncIdle, IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&application).Error; err != nil {
		t.Fatal(err)
	}
	run := model.PipelineRun{
		ID: uuid.NewString(), ApplicationID: application.ID, Trigger: "manual", Ref: "refs/heads/main",
		CommitSHA: strings.Repeat("b", 40), Status: model.PipelineRunRunning, Stage: "build",
		LogBytes:  uint64(maximumPipelineRunLogBytes - len(pipelineRunLogTruncatedMessage) - 5),
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	service.appendRunLog(context.Background(), run.ID, "build", "output", "0123456789")
	var stored model.PipelineRun
	if err := db.Select("log_bytes", "log_truncated").First(&stored, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LogBytes != maximumPipelineRunLogBytes || !stored.LogTruncated {
		t.Fatalf("流水线日志配额没有收敛: bytes=%d truncated=%v", stored.LogBytes, stored.LogTruncated)
	}
	var logs []model.PipelineRunLog
	if err := db.Where("pipeline_run_id = ?", run.ID).Order("id ASC").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || logs[0].Message != "01234" || logs[1].Message != pipelineRunLogTruncatedMessage {
		t.Fatalf("配额边界的截断日志不正确: %+v", logs)
	}
	service.appendRunLog(context.Background(), run.ID, "deploy", "output", "must-not-be-stored")
	var count int64
	if err := db.Model(&model.PipelineRunLog{}).Where("pipeline_run_id = ?", run.ID).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("达到运行级上限后仍写入日志: count=%d err=%v", count, err)
	}
}

func TestExecutionLogListRespectsDepartmentScope(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	now := time.Now().UTC()
	departments := []string{database.DefaultDepartmentID, uuid.NewString()}
	for index, departmentID := range departments {
		application := model.Application{
			ID: uuid.NewString(), DepartmentID: departmentID,
			Name: fmt.Sprintf("部门日志应用-%d", index), RepositoryID: repositoryID,
			PollIntervalSeconds: 60, SyncStatus: model.ApplicationSyncIdle, IsActive: true,
			CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&application).Error; err != nil {
			t.Fatalf("创建部门日志应用失败: %v", err)
		}
		run := model.PipelineRun{
			ID: uuid.NewString(), DepartmentID: departmentID, ApplicationID: application.ID,
			Trigger: "manual", Ref: "refs/heads/main", CommitSHA: strings.Repeat("c", 40),
			Status: model.PipelineRunRunning, Stage: "build", CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("创建部门日志运行失败: %v", err)
		}
		service.appendRunLog(context.Background(), run.ID, "build", "output", fmt.Sprintf("部门-%d", index))
	}

	scoped := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
		UserID: "department-user", DepartmentID: departments[0],
	})
	logs, err := service.ListExecutionLogs(scoped, ExecutionLogFilter{Limit: 20})
	if err != nil || len(logs) != 1 || logs[0].Message != "部门-0" {
		t.Fatalf("流水线日志未按部门隔离: logs=%+v err=%v", logs, err)
	}
	allDepartments := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
		UserID: "superuser", AllDepartments: true,
	})
	logs, err = service.ListExecutionLogs(allDepartments, ExecutionLogFilter{Limit: 20})
	if err != nil || len(logs) != 2 {
		t.Fatalf("超级管理员未看到全部部门日志: logs=%+v err=%v", logs, err)
	}
}
