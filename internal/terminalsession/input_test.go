package terminalsession

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/terminalwire"
)

type heldInputWriter struct {
	entered, release chan struct{}
	once             sync.Once
	calls            atomic.Int32
}

func (w *heldInputWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	w.calls.Add(1)
	return len(b), nil
}

func TestControllerInputTinyFramesDoNotExhaustByteBudget(t *testing.T) {
	w := &heldInputWriter{entered: make(chan struct{}), release: make(chan struct{})}
	var failures atomic.Int32
	i := newControllerInput(w, func() { failures.Add(1) })
	t.Cleanup(func() {
		i.stop()
		close(w.release)
		select {
		case <-i.done:
		case <-time.After(time.Second):
			t.Error("输入协程未结束")
		}
	})
	if !i.offer([]byte{'a'}) {
		t.Fatal("首次输入被拒绝")
	}
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("写入未开始")
	}
	for range 256 {
		if !i.offer([]byte{'b'}) {
			t.Fatal("少量细碎输入被误判为队列满")
		}
	}
	if failures.Load() != 0 {
		t.Fatal("未达到字节预算的输入触发失败")
	}
}

func TestControllerInputBlockedPTYDoesNotBlockControlAndOverflowFails(t *testing.T) {
	w := &heldInputWriter{entered: make(chan struct{}), release: make(chan struct{})}
	failed := make(chan struct{}, 1)
	var failures atomic.Int32
	i := newControllerInput(w, func() {
		failures.Add(1)
		select {
		case failed <- struct{}{}:
		default:
		}
	})
	if !i.offer([]byte{'a'}) {
		t.Fatal("首次输入被拒绝")
	}
	<-w.entered
	for range controllerInputLimit/terminalwire.MaxData - 1 {
		if !i.offer(bytes.Repeat([]byte{'b'}, terminalwire.MaxData)) {
			t.Fatal("有界队列过早溢出")
		}
	}
	if !i.offer(bytes.Repeat([]byte{'b'}, terminalwire.MaxData-1)) {
		t.Fatal("字节预算未满时拒绝输入")
	}
	if i.offer([]byte{'c'}) {
		t.Fatal("队列未限制输入")
	}
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("输入阻塞连带阻塞控制失败通知")
	}
	i.stop()
	i.stop()
	if i.offer([]byte("after-close")) {
		t.Fatal("关闭后继续投递")
	}
	close(w.release)
	select {
	case <-i.done:
	case <-time.After(time.Second):
		t.Fatal("写入协程未结束")
	}
	if failures.Load() != 1 || w.calls.Load() != 1 || i.queue.Len() != 0 || i.pending != 0 {
		t.Fatal("排队输入被继续执行或未清理")
	}
}

type inputWriteResult struct {
	n   int
	err error
}

type steppedInputWriter struct {
	entered chan []byte
	results chan inputWriteResult
	release chan struct{}
	calls   atomic.Int32
}

func (w *steppedInputWriter) Write(b []byte) (int, error) {
	w.calls.Add(1)
	select {
	case w.entered <- b:
	case <-w.release:
		return len(b), nil
	}
	select {
	case result := <-w.results:
		return result.n, result.err
	case <-w.release:
		return len(b), nil
	}
}

func TestControllerInputPartialWriteReturnsOnlyConsumedBudget(t *testing.T) {
	w := &steppedInputWriter{entered: make(chan []byte, 2), results: make(chan inputWriteResult), release: make(chan struct{})}
	var failures atomic.Int32
	i := newControllerInput(w, func() { failures.Add(1) })
	var inflight []byte
	t.Cleanup(func() {
		i.stop()
		close(w.release)
		select {
		case <-i.done:
		case <-time.After(time.Second):
			t.Error("输入协程未结束")
			return
		}
		if i.pending != 0 || i.queue.Cap() != 0 || w.calls.Load() != 2 {
			t.Error("关闭后仍投递排队输入或保留额度")
		}
		if !bytes.Equal(inflight, make([]byte, len(inflight))) {
			t.Error("写入返回后仍保留在途输入")
		}
	})
	waitWrite := func(want int) []byte {
		t.Helper()
		select {
		case data := <-w.entered:
			if len(data) != want {
				t.Fatalf("写入长度为 %d，期望 %d", len(data), want)
			}
			return data
		case <-time.After(time.Second):
			t.Fatal("未进入预期的 PTY 写入")
			return nil
		}
	}
	if !i.offer(bytes.Repeat([]byte{'a'}, terminalwire.MaxData)) {
		t.Fatal("首次输入被拒绝")
	}
	waitWrite(terminalwire.MaxData)
	for range controllerInputLimit/terminalwire.MaxData - 1 {
		if !i.offer(bytes.Repeat([]byte{'b'}, terminalwire.MaxData)) {
			t.Fatal("未耗尽预算却拒绝输入")
		}
	}
	if i.offer([]byte{'c'}) {
		t.Fatal("在途输入没有计入额度")
	}
	select {
	case w.results <- inputWriteResult{n: 1}:
	case <-time.After(time.Second):
		t.Fatal("部分写入没有接收测试结果")
	}
	inflight = waitWrite(terminalwire.MaxData - 1)
	if !i.offer([]byte{'c'}) {
		t.Fatal("已写入的单字节未归还额度")
	}
	if i.offer([]byte{'d'}) || failures.Load() != 2 {
		t.Fatal("部分写入提前归还了未消费额度")
	}
	i.mu.Lock()
	queued := i.queue.Bytes()
	i.mu.Unlock()
	i.stop()
	if !bytes.Equal(queued, make([]byte, len(queued))) {
		t.Fatal("关闭后仍保留排队输入")
	}
	if !bytes.Equal(inflight, bytes.Repeat([]byte{'a'}, terminalwire.MaxData-1)) {
		t.Fatal("关闭时改写了 PTY 仍在读取的缓冲")
	}
	i.mu.Lock()
	pending, buffered := i.pending, i.queue.Len()
	i.mu.Unlock()
	if pending != len(inflight) || buffered != 0 {
		t.Fatal("关闭时未正确区分排队与在途额度")
	}
}

type invalidInputWriter struct{ result inputWriteResult }

func (w invalidInputWriter) Write([]byte) (int, error) { return w.result.n, w.result.err }

func TestControllerInputInvalidWritesReleaseBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result inputWriteResult
	}{
		{"negative", inputWriteResult{n: -1}},
		{"zero", inputWriteResult{}},
		{"oversized", inputWriteResult{n: 4}},
		{"partial_error", inputWriteResult{n: 1, err: io.ErrClosedPipe}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var failures atomic.Int32
			i := newControllerInput(invalidInputWriter{tc.result}, func() { failures.Add(1) })
			owned := []byte{'a', 'b', 'c'}
			if !i.offer(owned) {
				t.Fatal("输入未投递")
			}
			select {
			case <-i.done:
			case <-time.After(time.Second):
				t.Fatal("异常写入后协程未结束")
			}
			if failures.Load() != 1 || !i.closed || i.pending != 0 || i.queue.Len() != 0 || i.queue.Cap() != 0 {
				t.Fatal("异常写入没有释放输入及额度")
			}
			if !bytes.Equal(owned, make([]byte, len(owned))) {
				t.Fatal("输入原缓冲未尽力清理")
			}
		})
	}
}

func TestControllerInputConcurrentOffersAndStop(t *testing.T) {
	w := &heldInputWriter{entered: make(chan struct{}), release: make(chan struct{})}
	var failures atomic.Int32
	i := newControllerInput(w, func() { failures.Add(1) })
	t.Cleanup(func() {
		i.stop()
		close(w.release)
		select {
		case <-i.done:
		case <-time.After(time.Second):
			t.Error("输入协程未结束")
			return
		}
		if i.pending != 0 || i.queue.Cap() != 0 {
			t.Error("并发关闭后仍保留输入或额度")
		}
	})
	if !i.offer([]byte{'a'}) {
		t.Fatal("首次输入未投递")
	}
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("写入未开始")
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			<-start
			for range 32 {
				i.offer([]byte{'b'})
			}
		})
	}
	wg.Go(func() { <-start; i.stop() })
	close(start)
	wg.Wait()
	if failures.Load() != 0 || i.offer([]byte{'c'}) {
		t.Fatal("正常关闭误记失败或仍接收输入")
	}
}

func TestControllerInputIdleStopNeedsNoBuffer(t *testing.T) {
	i := newControllerInput(io.Discard, func() { t.Error("空闲关闭不应失败") })
	i.mu.Lock()
	allocated := i.queue.Cap()
	i.mu.Unlock()
	if allocated != 0 {
		t.Fatal("空闲会话预分配了输入缓存")
	}
	i.stop()
	select {
	case <-i.done:
	case <-time.After(time.Second):
		t.Fatal("空闲输入协程没有被关闭唤醒")
	}
}

func TestControllerInputInvalidFrameDoesNotAllocate(t *testing.T) {
	for _, size := range []int{0, terminalwire.MaxData + 1} {
		var failures atomic.Int32
		i := newControllerInput(io.Discard, func() { failures.Add(1) })
		if i.offer(make([]byte, size)) {
			t.Fatal("无效输入帧被接收")
		}
		i.stop()
		select {
		case <-i.done:
		case <-time.After(time.Second):
			t.Fatal("无效帧之后协程未结束")
		}
		if failures.Load() != 1 || i.pending != 0 || i.queue.Cap() != 0 {
			t.Fatal("无效输入分配了缓存或未明确失败")
		}
	}
}

type smallInputWriter struct {
	bytes.Buffer
	want int
	done chan struct{}
}

func (w *smallInputWriter) Write(b []byte) (int, error) {
	n, err := w.Buffer.Write(b[:min(2, len(b))])
	if w.Len() == w.want {
		close(w.done)
	}
	return n, err
}

func TestControllerInputCoalescesFramesWithoutChangingOrder(t *testing.T) {
	want := make([]byte, 0, 256)
	for n := range 256 {
		want = append(want, byte(n))
	}
	w := &smallInputWriter{want: len(want), done: make(chan struct{})}
	var failures atomic.Int32
	i := newControllerInput(w, func() { failures.Add(1) })
	for _, b := range want {
		if !i.offer([]byte{b}) {
			t.Fatal("少量分帧输入被拒绝")
		}
	}
	select {
	case <-w.done:
	case <-time.After(time.Second):
		t.Fatal("输入没有全部写入")
	}
	i.stop()
	<-i.done
	if !bytes.Equal(w.Bytes(), want) || failures.Load() != 0 || i.pending != 0 {
		t.Fatal("输入合并改变字节顺序或未释放额度")
	}
}

func TestControllerInputPreservesPartialBinaryWrites(t *testing.T) {
	want := []byte{0, 255, '\r', '\n', 27}
	w := &smallInputWriter{want: len(want), done: make(chan struct{})}
	failed := make(chan struct{}, 1)
	i := newControllerInput(w, func() { failed <- struct{}{} })
	owned := append([]byte(nil), want...)
	if !i.offer(owned) {
		t.Fatal("输入被拒绝")
	}
	select {
	case <-w.done:
	case <-time.After(time.Second):
		t.Fatal("分片输入未写完")
	}
	i.stop()
	<-i.done
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatal("二进制分片被改变")
	}
	if !bytes.Equal(owned, make([]byte, len(owned))) {
		t.Fatal("已消费输入缓冲未尽力清理")
	}
	select {
	case <-failed:
		t.Fatal("合法分片写入报错")
	default:
	}
}

type failedInputWriter struct{}

func (failedInputWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestControllerInputWriteFailureIsNotNormalEOF(t *testing.T) {
	f := newControllerFailure()
	i := newControllerInput(failedInputWriter{}, func() { f.record(inputFailureCode) })
	if !i.offer([]byte{'a'}) {
		t.Fatal("输入未投递")
	}
	<-i.done
	f.record(0)
	m, code := reapedResult(true, true, 5, int(f.code.Load()))
	if !m.Released || m.Clean || m.Code != 74 || code != 74 {
		t.Fatal("写失败被误记为正常关闭")
	}
}
