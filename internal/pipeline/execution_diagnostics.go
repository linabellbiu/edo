package pipeline

import (
	"context"
	"strings"

	"edo/internal/logredact"
)

type executionDiagnosticKey struct{}

// 脱敏信息随当前任务积累，失败日志仍使用该任务的范围，不读取其他任务或系统日志。
func withExecutionDiagnosticRedactions(ctx context.Context, values func() []string) context.Context {
	return context.WithValue(ctx, executionDiagnosticKey{}, values)
}

// 同时保留业务提示和底层诊断；Error() 的安全提示不因显示日志而改变。
func (s *Service) appendExecutionFailureDiagnostic(ctx context.Context, expected executionFailureState, message string, cause error) {
	if cause == nil {
		return
	}
	var details []string
	remaining := 32
	var visit func(error)
	visit = func(err error) {
		if err == nil || remaining == 0 {
			return
		}
		remaining--
		text := strings.TrimSpace(err.Error())
		if diagnostic, ok := err.(interface{ Diagnostic() string }); ok {
			text = strings.TrimSpace(diagnostic.Diagnostic())
		}
		if text != "" && text != message {
			included := false
			for _, previous := range details {
				included = included || strings.Contains(previous, text)
			}
			if !included {
				details = append(details, text)
			}
		}
		switch wrapped := err.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range wrapped.Unwrap() {
				visit(child)
			}
		case interface{ Unwrap() error }:
			visit(wrapped.Unwrap())
		}
	}
	visit(cause)
	if len(details) == 0 {
		return
	}
	var redactions []string
	if values, ok := ctx.Value(executionDiagnosticKey{}).(func() []string); ok {
		redactions = values()
	}
	for _, value := range append([]string{}, redactions...) {
		if strings.ContainsRune(value, '\n') {
			for _, line := range strings.Split(value, "\n") {
				if line = strings.TrimSuffix(line, "\r"); line != "" {
					redactions = append(redactions, line)
				}
			}
		}
	}
	diagnostic := logredact.Text(string(redactLogBytes([]byte(strings.Join(details, "\n")), normalizeLogRedactions(redactions))))
	identity, _ := ctx.Value(pipelineNodeLogKey{}).(pipelineNodeLog)
	prefixBytes := 0
	if identity.name != "" {
		prefixBytes = len(identity.name) + len("[] ")
	}
	diagnostic = truncateUTF8Bytes(diagnostic, max(0, maximumRunLogMessageBytes-len("错误详情：")-prefixBytes))
	if identity.id == "" {
		ctx = withPipelineNodeLog(ctx, expected.nodeID, "")
	}
	s.appendRunLog(ctx, expected.runID, "failed", "error", "错误详情："+diagnostic)
}
