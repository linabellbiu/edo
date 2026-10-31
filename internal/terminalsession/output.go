package terminalsession

import (
	"sync"

	"edo/internal/terminalwire"
)

// 结束帧必须是最后一帧；关闭时丢弃尚未发送的 PTY 数据，不能在 ACK 后补写。
type controllerOutput struct {
	mu       sync.Mutex
	final    bool
	write    func(terminalwire.Message) error
	failures *controllerFailure
}

// 内核确认收尸后，通过同一输出对象发布最终状态。
func (o *controllerOutput) reaped(started, workerOK bool, tracked int) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.final {
		return 75
	}
	// 与在途输出写入共用这把锁：先封口，再读取已同步发布的最终错误。
	o.final = true
	failureCode := 0
	if o.failures != nil {
		failureCode = int(o.failures.code.Load())
	}
	result, code := reapedResult(started, workerOK, tracked, failureCode)
	if err := o.write(result); err != nil {
		if o.failures != nil {
			o.failures.record(75)
		}
		return 75
	}
	return code
}

func (o *controllerOutput) send(m terminalwire.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.final {
		if m.Type == "data" {
			return nil
		}
		return terminalwire.ErrProtocol
	}
	if m.Type == "closed" {
		o.final = true
	}
	err := o.write(m)
	if err != nil && o.failures != nil {
		o.failures.record(75)
	}
	return err
}
