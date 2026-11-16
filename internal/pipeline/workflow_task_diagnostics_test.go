package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"edo/internal/model"
	"edo/internal/testutil"
	"gorm.io/gorm"
)

type workflowDiagnosticFixtureError struct {
	detail string
}

func (e workflowDiagnosticFixtureError) Error() string      { return "准备步骤失败" }
func (e workflowDiagnosticFixtureError) Diagnostic() string { return e.detail }

func TestWorkflowTaskDiagnosticOnlyFreezesExplicitSafeDetails(t *testing.T) {
	workflow := &model.ReleaseWorkflow{Stages: []model.WorkflowStage{{Tasks: []model.WorkflowNode{{ID: "deploy", Config: model.WorkflowNodeConfig{EnvironmentVariables: map[string]string{"PASSWORD": "known-private-value", "PRIVATE_KEY": "first-key-line\nsecond-key-line"}}}}}}}
	ctx, diagnostics := withWorkflowTaskDiagnostics(context.Background(), workflow)
	s := &Service{}
	failures := map[string]string{}
	const detail = "GET /api/v1/nodes: connection refused; known-private-value; second-key-line"
	s.recordWorkflowTaskFailure(ctx, failures, "deploy", "workflow_task_resources", "无法连接集群", fmt.Errorf("包装：%w", workflowDiagnosticFixtureError{detail: detail}))
	got := diagnostics.entries["deploy"]
	if failures["deploy"] != "无法连接集群" || got.Operation != "workflow_task_resources" || !strings.Contains(got.Detail, "GET /api/v1/nodes: connection refused") {
		t.Fatalf("安全摘要或真实诊断丢失: summary=%v diagnostic=%+v", failures, got)
	}
	encoded, err := json.Marshal(diagnostics.entries)
	if err != nil || strings.Contains(string(encoded), "known-private-value") || strings.Contains(string(encoded), "second-key-line") || !strings.Contains(string(encoded), "已脱敏") {
		t.Fatalf("诊断快照未二次脱敏: %s %v", encoded, err)
	}
	s.recordWorkflowTaskFailure(ctx, failures, "raw", "workflow_task_resources", "未知错误", errors.New("private SQL password=unregistered-secret"))
	if _, exists := diagnostics.entries["raw"]; exists {
		t.Fatal("普通错误正文被无条件写入诊断快照")
	}
	s.recordWorkflowTaskFailure(ctx, failures, "deploy", "later_operation", "后续失败", workflowDiagnosticFixtureError{detail: "迟到错误"})
	if diagnostics.entries["deploy"] != got {
		t.Fatal("首个真实失败诊断被后续错误覆盖")
	}
}

func TestWorkflowTaskDiagnosticsStayWithinSnapshotBudget(t *testing.T) {
	ctx, diagnostics := withWorkflowTaskDiagnostics(context.Background(), &model.ReleaseWorkflow{})
	s := &Service{}
	failures := map[string]string{}
	for index := range 200 {
		s.recordWorkflowTaskFailure(ctx, failures, fmt.Sprint(index), "prepare", "失败", workflowDiagnosticFixtureError{detail: strings.Repeat("界", maximumRunLogMessageBytes)})
	}
	total := 0
	for _, diagnostic := range diagnostics.entries {
		if len(diagnostic.Detail) > maximumRunLogMessageBytes || !strings.HasSuffix(diagnostic.Detail, "已截断") {
			t.Fatal("单条准备诊断没有保持有界 UTF-8 文本")
		}
		total += len(diagnostic.Detail)
	}
	if total > maximumPipelineRunLogBytes || diagnostics.bytes != total {
		t.Fatalf("准备诊断总量超过运行日志配额: %d", total)
	}
}

func TestPreparedTaskDiagnosticPersistsWithoutJobOrFakeProgramOutput(t *testing.T) {
	ctx := context.Background()
	bad, healthy := parallelShell("failed-preparation"), parallelShell("healthy")
	snapshot := workflowSnapshot{
		SchemaVersion: 1, ExecutionVersion: 2,
		Source:          model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger},
		Stages:          []model.WorkflowStage{{ID: "parallel", Tasks: []model.WorkflowNode{bad, healthy}}},
		TaskFailures:    map[string]string{bad.ID: "无法连接集群"},
		TaskDiagnostics: map[string]workflowTaskDiagnostic{bad.ID: {Operation: "workflow_task_resources", Detail: "GET /api/v1/nodes: dial tcp 127.0.0.1:1: connect: connection refused"}},
	}
	s, db, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"code.txt": "source"})
	started, err := s.AdvanceRun(ctx, run.ID, "admin", "")
	if err != nil || started.Status != model.PipelineRunRunning {
		t.Fatalf("准备失败阻塞独立任务: %+v %v", started, err)
	}
	nodes, err := s.ListNodeRuns(ctx, run.ID)
	if err != nil || len(nodes) != 2 {
		t.Fatal(nodes, err)
	}
	for _, node := range nodes {
		if node.NodeID == bad.ID && (node.JobID != "" || node.Status != model.PipelineRunFailed) {
			t.Fatal("执行前失败生成了 Job 或未记失败", node)
		}
		if node.NodeID == healthy.ID && node.JobID == "" {
			t.Fatal("独立任务未正常入队")
		}
	}
	logs, status, err := s.ListNodeRunLogs(ctx, run.ID, bad.ID, 0, 100)
	if err != nil || status != model.PipelineRunFailed || len(logs) != 2 {
		t.Fatalf("任务详情日志缺失: %+v %s %v", logs, status, err)
	}
	if logs[0].Stage != "prepare" || logs[0].Level != "error" || logs[0].NodeID != bad.ID || !strings.Contains(logs[0].Message, snapshot.TaskDiagnostics[bad.ID].Detail) {
		t.Fatalf("准备诊断的步骤、归属或底层原因丢失: %+v", logs[0])
	}
	for _, entry := range logs {
		if entry.Level == "output" || entry.Level == "stderr" || strings.Contains(entry.Message, "退出码") {
			t.Fatal("未运行程序却伪造了终端输出或退出码", entry)
		}
	}
	// 再次推进和重新读取只恢复已经持久化的诊断，不重复写入、不借用其他任务日志。
	if _, err := s.AdvanceRun(ctx, run.ID, "admin", ""); err != nil {
		t.Fatal(err)
	}
	again, _, err := s.ListNodeRunLogs(ctx, run.ID, bad.ID, 0, 100)
	if err != nil || len(again) != len(logs) || again[0].ID != logs[0].ID {
		t.Fatalf("诊断重复写入或刷新丢失: %+v %v", again, err)
	}
	var stored model.PipelineRun
	if err := db.First(&stored, "uuid = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	frozen, err := parseWorkflowSnapshot(&stored)
	if err != nil || frozen.TaskDiagnostics[bad.ID] != snapshot.TaskDiagnostics[bad.ID] {
		t.Fatal("持久化运行未保留准备阶段诊断", err)
	}
	public, err := json.Marshal(stored)
	if err != nil || strings.Contains(string(public), "connection refused") {
		t.Fatal("普通运行响应暴露了任务诊断", err)
	}
}

func preparedFailureLogFixture(t *testing.T) (*Service, *gorm.DB, model.PipelineRun, string) {
	t.Helper()
	node := parallelShell("failed-preparation")
	snapshot := workflowSnapshot{
		SchemaVersion: 1, ExecutionVersion: 2,
		Source:          model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger},
		Stages:          []model.WorkflowStage{{ID: "prepare", Tasks: []model.WorkflowNode{node}}},
		TaskFailures:    map[string]string{node.ID: "无法连接集群"},
		TaskDiagnostics: map[string]workflowTaskDiagnostic{node.ID: {Operation: "workflow_task_resources", Detail: "GET /api/v1/nodes: connection refused"}},
	}
	s, db, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"code.txt": "source"})
	return s, db, run, node.ID
}

func TestPreparedFailureTerminalStateWaitsForDiagnosticCommit(t *testing.T) {
	s, db, run, nodeID := preparedFailureLogFixture(t)
	// 独立连接模拟日志订阅者；暂停在首条日志写入前，准确覆盖旧实现的终态已提交窗口。
	reader := NewService(testutil.ReopenDatabase(t, db), s.repositories, s.secrets)
	entered, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	const callback = "test:pause_prepared_failure_log"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "pipeline_run_logs" {
			once.Do(func() {
				close(entered)
				<-release
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	done := make(chan error, 1)
	go func() {
		_, err := s.AdvanceRun(context.Background(), run.ID, "admin", "")
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("未进入准备日志写入点: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("准备日志写入没有开始")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	logs, status, err := reader.ListNodeRunLogs(ctx, run.ID, nodeID, 0, 200)
	if err != nil || nodeFinished(status) || len(logs) != 0 {
		t.Fatalf("日志未持久化时订阅者已经读到终态，会提前 complete: logs=%+v status=%s err=%v", logs, status, err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("释放日志写入后运行未收敛")
	}
	logs, status, err = reader.ListNodeRunLogs(context.Background(), run.ID, nodeID, 0, 200)
	if err != nil || status != model.PipelineRunFailed || len(logs) != 2 || logs[0].Stage != "prepare" || logs[1].Stage != "failed" {
		t.Fatalf("终态与两条日志未原子可见: logs=%+v status=%s err=%v", logs, status, err)
	}
}

func TestPreparedFailureRollsBackStateWhenLogWriteFails(t *testing.T) {
	s, db, run, _ := preparedFailureLogFixture(t)
	cause := errors.New("isolated diagnostic write failure")
	const callback = "test:fail_prepared_failure_log"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "pipeline_run_logs" {
			_ = tx.AddError(cause)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	if _, err := s.AdvanceRun(context.Background(), run.ID, "admin", ""); !errors.Is(err, cause) {
		t.Fatalf("准备日志失败没有阻止终态提交: %v", err)
	}
	var stored model.PipelineRun
	if err := db.First(&stored, "uuid = ?", run.ID).Error; err != nil || nodeFinished(stored.Status) || stored.LogBytes != 0 {
		t.Fatalf("日志失败后仍保留终态或日志预算: %+v %v", stored, err)
	}
	for _, value := range []any{&model.PipelineNodeRun{}, &model.PipelineRunLog{}} {
		var count int64
		if err := db.Model(value).Where("pipeline_run_id = ?", run.ID).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("准备日志失败未回滚关联记录: %T count=%d err=%v", value, count, err)
		}
	}
}
