package terminalsession

import (
	"sync/atomic"

	"edo/internal/terminalwire"
)

const inputFailureCode = 74

type controllerFailure struct {
	code   atomic.Int32
	signal chan struct{}
}

func newControllerFailure() *controllerFailure {
	return &controllerFailure{signal: make(chan struct{}, 1)}
}
func (f *controllerFailure) record(code int) {
	if code != 0 {
		f.code.CompareAndSwap(0, int32(code))
	}
	select {
	case f.signal <- struct{}{}:
	default:
	}
}

// 只能在内核已返回 ECHILD 后使用，异常原因不能被同时到达的正常 EOF 覆盖。
func reapedResult(started, workerOK bool, tracked, failureCode int) (terminalwire.Message, int) {
	clean := started && workerOK && failureCode == 0
	code := failureCode
	if !clean && code == 0 {
		code = 76
	}
	return terminalwire.Message{Type: "closed", Released: true, Clean: clean, Code: code, Tracked: tracked}, code
}
