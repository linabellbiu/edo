package terminal

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type resourceLeaseFake struct {
	closed  chan struct{}
	once    sync.Once
	resizes atomic.Int32
}

func (*resourceLeaseFake) Read([]byte) (int, error)             { return 0, io.EOF }
func (*resourceLeaseFake) Write(data []byte) (int, error)       { return len(data), nil }
func (f *resourceLeaseFake) Resize(context.Context, Size) error { f.resizes.Add(1); return nil }
func (f *resourceLeaseFake) Close() error                       { f.once.Do(func() { close(f.closed) }); return nil }

func TestTerminalResourceLeaseClosesOnTargetChangeOrCancellation(t *testing.T) {
	for _, cancelOnly := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		var changed atomic.Bool
		base := &resourceLeaseFake{closed: make(chan struct{})}
		session := guardResourceSession(ctx, base, func(context.Context) error {
			if changed.Load() {
				return ErrTargetNotReady
			}
			return nil
		}, time.Millisecond)
		if cancelOnly {
			cancel()
		} else {
			changed.Store(true)
		}
		select {
		case <-base.closed:
		case <-time.After(time.Second):
			t.Fatal("目标失效后旧终端未关闭")
		}
		if _, err := session.Write([]byte("不得发送")); err == nil {
			t.Fatal("关闭后的终端仍接受输入")
		}
		_ = session.Close()
		cancel()
	}
}

func TestTerminalResourceLeaseResizeRechecksTarget(t *testing.T) {
	base := &resourceLeaseFake{closed: make(chan struct{})}
	var changed atomic.Bool
	session := guardResourceSession(context.Background(), base, func(context.Context) error {
		if changed.Load() {
			return ErrTargetNotReady
		}
		return nil
	}, time.Hour)
	defer session.Close()
	if err := session.Resize(context.Background(), Size{Columns: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	changed.Store(true)
	if err := session.Resize(context.Background(), Size{Columns: 80, Rows: 24}); err == nil {
		t.Fatal("resize未复核已变化目标")
	}
	if base.resizes.Load() != 1 {
		t.Fatal("失效目标仍收到resize")
	}
}

type failedResourceCleanup struct {
	*resourceLeaseFake
	err   error
	calls atomic.Int32
}

func (f *failedResourceCleanup) Close() error {
	f.calls.Add(1)
	_ = f.resourceLeaseFake.Close()
	return f.err
}

func TestTerminalResourceLeasePreservesCleanupFailureForEveryCloser(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "显式关闭", true: "租约取消后关闭"}[automatic], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("测试会话回收未确认")
			base := &failedResourceCleanup{resourceLeaseFake: &resourceLeaseFake{closed: make(chan struct{})}, err: failure}
			session := guardResourceSession(ctx, base, func(context.Context) error { return nil }, time.Hour)
			if automatic {
				cancel()
				select {
				case <-base.closed:
				case <-time.After(time.Second):
					t.Fatal("租约结束没有回收会话")
				}
			}
			var callers sync.WaitGroup
			for range 16 {
				callers.Go(func() {
					if err := session.Close(); !errors.Is(err, failure) {
						t.Errorf("重复关闭丢失首次回收错误: %v", err)
					}
				})
			}
			callers.Wait()
			if base.calls.Load() != 1 {
				t.Fatalf("底层回收执行了 %d 次，应仅执行一次", base.calls.Load())
			}
		})
	}
}
