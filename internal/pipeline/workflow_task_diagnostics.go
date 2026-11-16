package pipeline

import (
	"context"
	"errors"
	"strings"

	"edo/internal/model"
	"gorm.io/gorm"
)

type workflowTaskDiagnostic struct {
	Operation string `json:"operation"`
	Detail    string `json:"detail"`
}

type workflowTaskDiagnosticsKey struct{}

// 准备阶段尚无任务 Job；先将源头明确提供的安全诊断冻结，失败任务落地后再写入任务日志。
type workflowTaskDiagnostics struct {
	entries    map[string]workflowTaskDiagnostic
	redactions [][]byte
	bytes      int
}

func withWorkflowTaskDiagnostics(ctx context.Context, workflow *model.ReleaseWorkflow) (context.Context, *workflowTaskDiagnostics) {
	var values []string
	for _, task := range workflowTasks(workflow.Stages) {
		values = append(values, sensitiveVariableValues(task.Config.EnvironmentVariables)...)
		if task.Config.InlineBuild != nil {
			values = append(values, sensitiveVariableValues(task.Config.InlineBuild.BuildArgs)...)
		}
	}
	for _, value := range append([]string{}, values...) {
		if strings.ContainsRune(value, '\n') {
			for _, line := range strings.Split(value, "\n") {
				if line = strings.TrimSuffix(line, "\r"); line != "" {
					values = append(values, line)
				}
			}
		}
	}
	diagnostics := &workflowTaskDiagnostics{entries: map[string]workflowTaskDiagnostic{}, redactions: normalizeLogRedactions(values)}
	return context.WithValue(ctx, workflowTaskDiagnosticsKey{}, diagnostics), diagnostics
}

func (d *workflowTaskDiagnostics) record(nodeID, operation string, err error) {
	// 普通 Error() 可能带原始 SQL、认证配置或未经清理的上游正文，不能直接成为任务日志。
	var diagnostic interface{ Diagnostic() string }
	if !errors.As(err, &diagnostic) {
		return
	}
	detail := strings.TrimSpace(string(redactLogBytes([]byte(diagnostic.Diagnostic()), d.redactions)))
	detail = strings.ReplaceAll(strings.ToValidUTF8(detail, "�"), "\x00", "")
	if detail == "" {
		return
	}
	const truncated = "\n…准备诊断过长，已截断"
	remaining := maximumPipelineRunLogBytes - d.bytes
	limit := min(maximumRunLogMessageBytes, remaining)
	if limit <= len(truncated) {
		return
	}
	if len(detail) > limit {
		detail = truncateUTF8Bytes(detail, limit-len(truncated)) + truncated
	}
	operation = truncateUTF8Bytes(operation, 128)
	d.entries[nodeID] = workflowTaskDiagnostic{Operation: operation, Detail: detail}
	d.bytes += len(detail)
}

func (s *Service) appendWorkflowTaskDiagnostic(ctx context.Context, tx *gorm.DB, runID string, diagnostic workflowTaskDiagnostic) error {
	if strings.TrimSpace(diagnostic.Detail) == "" {
		return nil
	}
	message := "任务准备失败（" + diagnostic.Operation + "）：\n" + diagnostic.Detail
	return s.appendRunLogInTransaction(ctx, tx, runID, "prepare", "error", message)
}
