package pipeline

import (
	"context"
	"strings"
	"testing"

	"edo/internal/model"
)

func TestExecutionLogInterruptedTailHidesKnownSecretPrefixes(t *testing.T) {
	for _, reason := range []string{"quota", "canceled"} {
		t.Run(reason, func(t *testing.T) {
			node := parallelShell("tail-redaction")
			snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{node}}}}
			s, _, _, run := newBuildExecutorTestRun(t, node, snapshot, nil)
			ctx, cancel := context.WithCancel(withPipelineNodeLog(t.Context(), node.ID, node.Name))
			defer cancel()
			const secret = "private-token-value-that-must-not-appear"
			const partial = "private-token-value"
			writer := s.newLeveledExecutionLogWriter(ctx, run.ID, "build", "终端", "stderr", secret)
			if reason == "quota" {
				// 直接恢复接近配额的 writer 状态，核验本次 Write 确实在秘密中间被裁剪。
				writer.written = maximumBuildLogBytes - len("fatal: ") - len(partial)
				if _, err := writer.Write([]byte("fatal: " + secret)); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := writer.Write([]byte("fatal: " + partial)); err != nil {
					t.Fatal(err)
				}
				cancel()
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			logs, _, err := s.ListNodeRunLogs(t.Context(), run.ID, node.ID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			found, warned := false, false
			for _, entry := range logs {
				if strings.Contains(entry.Message, partial) {
					t.Fatalf("中断输出泄漏了已知秘密前缀: %q", entry.Message)
				}
				found = found || entry.Level == "stderr" && strings.Contains(entry.Message, "fatal: [已脱敏]")
				warned = warned || entry.Level == "warning" && strings.Contains(entry.Message, "已截断")
			}
			if !found || reason == "quota" && !warned {
				t.Fatalf("保护秘密时丢失实际尾部或配额提示: %+v", logs)
			}
		})
	}
}

func TestExecutionLogSuccessfulClosePreservesOrdinaryTailAndSplitSecret(t *testing.T) {
	node := parallelShell("complete-redaction")
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{node}}}}
	s, _, _, run := newBuildExecutorTestRun(t, node, snapshot, nil)
	const secret = "private-complete-token"
	writer := s.newLeveledExecutionLogWriter(withPipelineNodeLog(t.Context(), node.ID, ""), run.ID, "build", "终端", "output", secret)
	prefix := strings.Repeat("x", buildLogChunkBytes-6)
	if _, err := writer.Write([]byte(prefix + secret[:6])); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(secret[6:] + "\nordinary tail p")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	logs, _, err := s.ListNodeRunLogs(t.Context(), run.ID, node.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for _, entry := range logs {
		output.WriteString(entry.Message)
	}
	if got, want := output.String(), prefix+"[已脱敏]\nordinary tail p"; got != want {
		t.Fatalf("完整秘密跨分块未脱敏，或正常尾部被误改: suffix=%q", got[max(0, len(got)-80):])
	}
}

func TestExecutionLogCanceledTailMasksWholeRepeatingSecret(t *testing.T) {
	node := parallelShell("repeating-secret")
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{node}}}}
	s, _, _, run := newBuildExecutorTestRun(t, node, snapshot, nil)
	ctx, cancel := context.WithCancel(t.Context())
	writer := s.newLeveledExecutionLogWriter(ctx, run.ID, "build", "终端", "stderr", "abab")
	if _, err := writer.Write([]byte("fatal: abab")); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	logs, _, err := s.ListRunLogs(t.Context(), run.ID, 0, 100)
	if err != nil || len(logs) != 1 || logs[0].Message != "fatal: [已脱敏]" {
		t.Fatalf("重复前缀秘密被部分替换而留下残片: %+v %v", logs, err)
	}
}

func TestExecutionLogInterruptedTailMasksOverlappingSecrets(t *testing.T) {
	for _, reason := range []string{"quota", "canceled"} {
		for _, test := range []struct {
			name, pending string
			secrets       []string
		}{
			{name: "two-secrets", pending: "abcdefG", secrets: []string{"abcdef", "defGXYZ"}},
			{name: "overlap-chain", pending: "abcdefGHIJ", secrets: []string{"abcdef", "defGHI", "GHIJKLM"}},
		} {
			t.Run(reason+"/"+test.name, func(t *testing.T) {
				node := parallelShell("overlapping-secrets")
				snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{node}}}}
				s, _, _, run := newBuildExecutorTestRun(t, node, snapshot, nil)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				writer := s.newLeveledExecutionLogWriter(ctx, run.ID, "build", "终端", "stderr", test.secrets...)
				value := "fatal: " + test.pending
				if reason == "quota" {
					writer.written = maximumBuildLogBytes - len(value)
					value += "discarded"
				}
				if _, err := writer.Write([]byte(value)); err != nil {
					t.Fatal(err)
				}
				if reason == "canceled" {
					cancel()
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				logs, _, err := s.ListRunLogs(t.Context(), run.ID, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, entry := range logs {
					if entry.Level == "stderr" {
						found = true
						if entry.Message != "fatal: [已脱敏]" {
							t.Fatalf("重叠秘密在裁剪后留下残片: %q", entry.Message)
						}
					}
				}
				if !found {
					t.Fatal("丢失中断前的真实终端尾部")
				}
			})
		}
	}
}
