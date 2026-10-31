package terminalsession

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	for range 8 {
		if !i.offer([]byte{'b'}) {
			t.Fatal("有界队列过早溢出")
		}
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
	if failures.Load() != 1 || w.calls.Load() != 1 || len(i.queue) != 0 {
		t.Fatal("排队输入被继续执行或未清理")
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
