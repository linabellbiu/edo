package pipeline

import (
	"strings"
	"testing"
	"time"

	"edo/internal/dockerengine"
	"edo/internal/model"
)

func TestExecutionLogStreamsKeepOriginIndependentOfMessageAndExitStatus(t *testing.T) {
	node := model.WorkflowNode{ID: "generic-logs", Name: "任意工具", Type: model.WorkflowNodeShell}
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{node}}}}
	s, _, _, run := newBuildExecutorTestRun(t, node, snapshot, nil)
	ctx := withPipelineNodeLog(t.Context(), node.ID, node.Name)
	output := s.newExecutionLogStreams(ctx, run.ID, "build", "任意工具", "private-value")
	result, err := runWorkspaceBuild(ctx, dockerengine.ScriptContainerInput{SourceDirectory: t.TempDir(), Timeout: 30 * time.Second, Script: "printf '%s\\n' 'Error dependency is unhealthy'\nprintf '%s\\n' 'arbitrary diagnostic private-value' >&2\nexit 7\n", Stdout: output, Stderr: output.Stderr()})
	if closeErr := output.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || result.ExitCode != 7 {
		t.Fatal("未保留实际退出结果", result, err)
	}
	logs, _, logErr := s.ListNodeRunLogs(ctx, run.ID, node.ID, 0, 100)
	if logErr != nil || len(logs) != 2 {
		t.Fatal(logs, logErr)
	}
	sources := map[string]string{}
	for _, line := range logs {
		sources[line.Level] = line.Message
	}
	if !strings.Contains(sources["output"], "Error dependency is unhealthy") || !strings.Contains(sources["stderr"], "arbitrary diagnostic") {
		t.Fatal("按文本猜测了输出来源", logs)
	}
	if strings.Contains(sources["stderr"], "private-value") || !strings.Contains(sources["stderr"], "[已脱敏]") {
		t.Fatal("标准错误流未沿用脱敏", logs)
	}

	var stored model.PipelineRun
	if err := s.db.First(&stored, "uuid = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != run.Status {
		t.Fatal("日志流擅自改变了任务状态")
	}
}

func TestAnsiFrameIsNotSplitAcrossLogRecords(t *testing.T) {
	value := []byte("prefix\x1b[31m任意输出\x1b[0m")
	for _, limit := range []int{7, 8, 9} {
		if cut := ansiFrameBoundary(value, limit); cut != 6 {
			t.Fatalf("切开了 ANSI CSI 帧: limit=%d cut=%d", limit, cut)
		}
	}
	if cut := ansiFrameBoundary(value, len(value)); cut != len(value) {
		t.Fatal("完整 ANSI 帧被截断", cut)
	}
	writer := &buildLogWriter{pending: value}
	if cut := writer.safeFlushLength(9); cut != 6 {
		t.Fatal("未配置密钥时仍应保留完整帧", cut)
	}
}
