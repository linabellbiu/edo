package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/dockerengine"
	"edo/internal/model"
)

func TestExecutionLogsPreserveRealStderrTailAfterProcessTimeout(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	now := time.Now().UTC()
	application := model.Application{ID: uuid.NewString(), Name: "超时日志测试", RepositoryID: repositoryID, PollIntervalSeconds: 60, SyncStatus: model.ApplicationSyncIdle, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&application).Error; err != nil {
		t.Fatal(err)
	}
	run := model.PipelineRun{ID: uuid.NewString(), ApplicationID: application.ID, Trigger: "manual", Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), Status: model.PipelineRunRunning, Stage: "build", CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(withPipelineNodeLog(t.Context(), "timeout-node", "实际脚本"), 200*time.Millisecond)
	defer cancel()
	output := service.newExecutionLogStreams(ctx, run.ID, "build", "脚本", "private-output-secret")
	_, err := runWorkspaceBuild(ctx, dockerengine.ScriptContainerInput{SourceDirectory: t.TempDir(), WorkingDirectory: ".", Timeout: 30 * time.Second, Script: "printf 'started\\n'; printf 'fatal: private-output-secret at last byte' >&2; sleep 10", Stdout: output, Stderr: output.Stderr()})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("脚本没有实际超时: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	var logs []model.PipelineRunLog
	if err := db.Where("pipeline_run_id = ?", run.ID).Order("id ASC").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	foundTail, foundOutput := false, false
	for _, log := range logs {
		if log.NodeID != "timeout-node" {
			t.Fatalf("日志节点归属丢失: %+v", log)
		}
		if strings.Contains(log.Message, "private-output-secret") {
			t.Fatal("尾部归档未脱敏")
		}
		foundTail = foundTail || log.Level == "stderr" && strings.Contains(log.Message, "fatal: [已脱敏] at last byte")
		foundOutput = foundOutput || log.Level == "output" && strings.Contains(log.Message, "started")
	}
	if !foundTail || !foundOutput {
		t.Fatalf("执行超时后丢失真实终端输出: %+v", logs)
	}
}

func TestExecutionLogFinalFlushSharesOnePersistenceDeadline(t *testing.T) {
	service, db, _, _ := newPipelineTestService(t)
	var queries atomic.Int32
	if err := db.Callback().Query().Before("gorm:query").Register("block_final_log_query", func(tx *gorm.DB) {
		if tx.Statement.Table != "pipeline_runs" {
			return
		}
		queries.Add(1)
		<-tx.Statement.Context.Done()
		tx.AddError(tx.Statement.Context.Err())
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("block_final_log_query") })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	writer := service.newLeveledExecutionLogWriter(ctx, "fixture-run", "build", "脚本", "stderr")
	writer.pending = []byte(strings.Repeat("terminal tail\n", buildLogChunkBytes))
	start := time.Now()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 4*time.Second || elapsed > 7*time.Second {
		t.Fatalf("最终归档没有使用单次有界预算: %s", elapsed)
	}
	if queries.Load() != 1 {
		t.Fatalf("数据库超时后为后续chunk重新续期: queries=%d", queries.Load())
	}
}
