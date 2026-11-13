package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"edo/internal/model"
)

type safeDiagnosticTestError struct{ cause error }

func (e safeDiagnosticTestError) Error() string { return "文件准备失败，请检查配置" }
func (e safeDiagnosticTestError) Unwrap() error { return e.cause }

func TestExecutionFailureLogsUnderlyingCauseAndKeepsPublicMessage(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(fmt.Sprint(parallel), func(t *testing.T) {
			node := parallelShell("diagnostic-task")
			snapshot := workflowSnapshot{SchemaVersion: 1, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{node}}}}
			s, db, _, run := newBuildExecutorTestRun(t, node, snapshot, nil)
			if parallel {
				if err := db.Create(&model.PipelineNodeRun{ID: "diagnostic-node-run", PipelineRunID: run.ID, NodeID: node.ID, JobID: run.ExecutionJobID, Status: model.PipelineRunRunning}).Error; err != nil {
					t.Fatal(err)
				}
			}
			ctx := withPipelineNodeLog(context.Background(), node.ID, node.Name)
			ctx = withExecutionDiagnosticRedactions(ctx, func() []string { return []string{"private-file-canary", "first-secret-line\nsecond-secret-line"} })
			cause := safeDiagnosticTestError{cause: &os.PathError{Op: "open", Path: "/workspace/config.env", Err: errors.New("permission denied private-file-canary second-secret-line")}}
			message := "部署配置无法读取，请检查配置及使用权限"
			if err := s.failDeploymentExecution(ctx, failureStateForRun(run), message, cause); err == nil {
				t.Fatal("失败被忽略")
			}
			var stored model.PipelineRun
			if err := db.First(&stored, "uuid = ?", run.ID).Error; err != nil || stored.Message != message || stored.Status != model.PipelineRunFailed {
				t.Fatal("公开提示被底层错误替换", err)
			}
			logs, _, err := s.ListNodeRunLogs(context.Background(), run.ID, node.ID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			for _, line := range logs {
				if line.Level != "error" || line.NodeID != node.ID {
					t.Fatal("错误级别或任务身份丢失")
				}
				text.WriteString(line.Message)
			}
			for _, want := range []string{message, "错误详情", "open /workspace/config.env", "permission denied", "[已脱敏]"} {
				if !strings.Contains(text.String(), want) {
					t.Fatal("日志缺少业务提示或底层诊断", want)
				}
			}
			for _, secret := range []string{"private-file-canary", "second-secret-line"} {
				if strings.Contains(text.String(), secret) {
					t.Fatal("底层诊断泄漏密钥")
				}
			}
		})
	}
}

func TestExecutionFailureDiagnosticsHandleJoinedErrorsAndBoundedLogs(t *testing.T) {
	node := parallelShell("bounded-diagnostic")
	snapshot := workflowSnapshot{SchemaVersion: 1, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{node}}}}
	s, db, _, run := newBuildExecutorTestRun(t, node, snapshot, nil)
	cause := safeDiagnosticTestError{cause: errors.Join(errors.New("dial tcp: connection refused"), errors.New("exit status 23\n"+strings.Repeat("诊断", 20_000)))}
	s.appendExecutionFailureDiagnostic(context.Background(), failureStateForRun(run), "执行失败", cause)
	var logs []model.PipelineRunLog
	if err := db.Where("pipeline_run_id = ?", run.ID).Find(&logs).Error; err != nil || len(logs) != 1 {
		t.Fatal("未记录诊断", err)
	}
	line := logs[0]
	if len(line.Message) > maximumRunLogMessageBytes || !utf8.ValidString(line.Message) || line.NodeID != node.ID || !strings.Contains(line.Message, "connection refused") || !strings.Contains(line.Message, "exit status 23") {
		t.Fatal("诊断丢失错误链、任务身份或超出单条日志上限")
	}
}
