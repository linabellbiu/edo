package terminal

import (
	"context"
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
