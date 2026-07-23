package worker

import (
	"context"
	"errors"
	"fmt"
)

type ExecutionError struct {
	Code       string
	Message    string
	Retryable  bool
	underlying error
}

func (e *ExecutionError) Error() string {
	if e.underlying == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.underlying)
}

func (e *ExecutionError) Unwrap() error { return e.underlying }

func NewRetryableError(code, message string, err error) error {
	return &ExecutionError{Code: code, Message: message, Retryable: true, underlying: err}
}

func NewPermanentError(code, message string, err error) error {
	return &ExecutionError{Code: code, Message: message, Retryable: false, underlying: err}
}

func classifyError(err error) (code, message string, retryable bool) {
	var executionError *ExecutionError
	if errors.As(err, &executionError) {
		return executionError.Code, executionError.Message, executionError.Retryable
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "task_interrupted", "任务执行超时或被 Worker 中断", true
	}
	return "task_failed", "任务执行失败，请检查任务配置", false
}
