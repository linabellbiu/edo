package terminalsession

import (
	"io"
	"sync"

	"edo/internal/terminalwire"
)

// PTY 不读输入时，不能阻塞同一连接上的关闭、租约和尺寸控制。
type controllerInput struct {
	mu             sync.Mutex
	closed         bool
	queue          chan []byte
	stopping, done chan struct{}
	writer         io.Writer
	fail           func()
}

func newControllerInput(writer io.Writer, fail func()) *controllerInput {
	i := &controllerInput{queue: make(chan []byte, 8), stopping: make(chan struct{}), done: make(chan struct{}), writer: writer, fail: fail}
	go i.run()
	return i
}

func (i *controllerInput) offer(b []byte) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return false
	}
	if len(b) == 0 || len(b) > terminalwire.MaxData {
		i.fail()
		return false
	}
	select {
	case i.queue <- b:
		return true
	default:
		i.fail()
		return false
	}
}

func (i *controllerInput) stop() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return
	}
	i.closed = true
	close(i.stopping)
	for {
		select {
		case b := <-i.queue:
			clear(b)
		default:
			return
		}
	}
}

func (i *controllerInput) run() {
	defer close(i.done)
	defer i.stop()
	for {
		var b []byte
		select {
		case <-i.stopping:
			return
		case b = <-i.queue:
		}
		payload := b
		i.mu.Lock()
		closed := i.closed
		i.mu.Unlock()
		if closed {
			clear(payload)
			return
		}
		for len(b) > 0 {
			n, err := i.writer.Write(b)
			if err != nil || n <= 0 || n > len(b) {
				clear(payload)
				i.mu.Lock()
				if !i.closed {
					i.fail()
				}
				i.mu.Unlock()
				return
			}
			b = b[n:]
		}
		clear(payload)
	}
}
