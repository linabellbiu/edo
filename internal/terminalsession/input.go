package terminalsession

import (
	"bytes"
	"io"
	"sync"

	"edo/internal/terminalwire"
)

const controllerInputLimit = 256 * 1024

// PTY 不读输入时，不能阻塞同一连接上的关闭、租约和尺寸控制。
type controllerInput struct {
	mu             sync.Mutex
	closed         bool
	queue          bytes.Buffer
	pending        int
	ready          chan struct{}
	stopping, done chan struct{}
	writer         io.Writer
	fail           func()
}

func newControllerInput(writer io.Writer, fail func()) *controllerInput {
	i := &controllerInput{ready: make(chan struct{}, 1), stopping: make(chan struct{}), done: make(chan struct{}), writer: writer, fail: fail}
	go i.run()
	return i
}

func (i *controllerInput) offer(b []byte) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return false
	}
	if len(b) == 0 || len(b) > terminalwire.MaxData || len(b) > controllerInputLimit-i.pending {
		i.fail()
		return false
	}
	// 接管已接收的输入，合并细碎帧；额度包括正在写入 PTY 的部分。
	i.pending += len(b)
	_, _ = i.queue.Write(b)
	clear(b)
	select {
	case i.ready <- struct{}{}:
	default:
	}
	return true
}

func (i *controllerInput) stop() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return
	}
	i.closed = true
	close(i.stopping)
	clear(i.queue.Bytes())
	i.pending -= i.queue.Len()
	i.queue = bytes.Buffer{}
}

func (i *controllerInput) take() ([]byte, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil, false
	}
	n := min(i.queue.Len(), terminalwire.MaxData)
	if n == 0 {
		return nil, true
	}
	queued := i.queue.Bytes()
	b := append([]byte(nil), queued[:n]...)
	clear(queued[:n])
	i.queue.Next(n)
	if i.queue.Len() == 0 {
		i.queue = bytes.Buffer{}
	}
	return b, true
}

func (i *controllerInput) run() {
	defer close(i.done)
	defer i.stop()
	for {
		payload, open := i.take()
		if !open {
			return
		}
		if len(payload) == 0 {
			select {
			case <-i.stopping:
				return
			case <-i.ready:
			}
			continue
		}
		b := payload
		for len(b) > 0 {
			i.mu.Lock()
			closed := i.closed
			if closed {
				i.pending -= len(b)
			}
			i.mu.Unlock()
			if closed {
				clear(payload)
				return
			}
			n, err := i.writer.Write(b)
			if err != nil || n <= 0 || n > len(b) {
				clear(payload)
				i.mu.Lock()
				i.pending -= len(b)
				if !i.closed {
					i.fail()
				}
				i.mu.Unlock()
				return
			}
			clear(b[:n])
			i.mu.Lock()
			i.pending -= n
			i.mu.Unlock()
			b = b[n:]
		}
		clear(payload)
	}
}
