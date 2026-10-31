package terminal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"edo/internal/terminalwire"
)

type controlledFixture struct {
	trailing                                                             bool
	tailGate                                                             chan struct{}
	ack                                                                  terminalwire.Message
	missingACK, malformed, flood                                         bool
	inspect                                                              func(context.Context) (client.ExecInspectResult, error)
	closeFrames, transportCloses, clientCloses, inspectCanceled, resizes atomic.Int32
	stopped                                                              atomic.Bool
	finished                                                             chan struct{}
	floodStart                                                           chan struct{}
}

// 仅测试端编码 Docker 固定的 8 字节 stdout 帧；生产端仍使用官方 stdcopy 解复用。
type dockerTestStdout struct{ io.Writer }

func (w dockerTestStdout) Write(b []byte) (int, error) {
	var h [8]byte
	h[0] = 1
	binary.BigEndian.PutUint32(h[4:], uint32(len(b)))
	if n, err := w.Writer.Write(h[:]); err != nil || n != len(h) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return 0, err
	}
	return w.Writer.Write(b)
}

func controlledTestSession(t *testing.T, ctx context.Context, f *controlledFixture) *dockerSession {
	t.Helper()
	a, b := net.Pipe()
	f.finished = make(chan struct{})
	go func() {
		defer close(f.finished)
		defer b.Close()
		_ = b.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReaderSize(b, terminalwire.MaxLine)
		out := dockerTestStdout{b}
		start, err := terminalwire.Read(reader)
		if err != nil || !terminalwire.ValidStart(start) {
			return
		}
		if f.malformed {
			_, _ = out.Write([]byte("not-json\n"))
		} else {
			_ = terminalwire.Write(out, terminalwire.Message{Type: "ready", NativeABI: true})
		}
		if f.flood {
			<-f.floodStart
			for i := 0; i < 40; i++ {
				if terminalwire.Write(out, terminalwire.Message{Type: "data", Data: bytes.Repeat([]byte{'x'}, terminalwire.MaxData)}) != nil {
					return
				}
			}
		}
		for {
			m, err := terminalwire.Read(reader)
			if err != nil {
				return
			}
			switch m.Type {
			case "ping":
			case "resize":
				f.resizes.Add(1)
			case "data":
				_ = terminalwire.Write(out, terminalwire.Message{Type: "data", Data: m.Data})
			case "close":
				f.closeFrames.Add(1)
				f.stopped.Store(true)
				if !f.missingACK {
					ack := f.ack
					ack.Type = "closed"
					ack.Tracked = 5
					_ = terminalwire.Write(out, ack)
				}
				if f.trailing {
					<-f.tailGate
					_, _ = out.Write([]byte("invalid-trailing-frame\n"))
				}
				return
			}
		}
	}()
	s := newDockerSession(ctx, a, a, func() { f.transportCloses.Add(1); _ = a.Close() }, func() error { f.clientCloses.Add(1); return nil }, func(probe context.Context) (client.ExecInspectResult, error) {
		if probe.Err() != nil {
			f.inspectCanceled.Add(1)
		}
		if f.inspect != nil {
			return f.inspect(probe)
		}
		return client.ExecInspectResult{ID: "exec", ContainerID: "container", Running: !f.stopped.Load()}, nil
	}, "exec", "container")
	t.Cleanup(func() {
		_ = s.Close()
		select {
		case <-f.finished:
		case <-time.After(time.Second):
			t.Error("模拟端未退出")
		}
	})
	return s
}

func startControlled(t *testing.T, s *dockerSession, ctx context.Context) {
	t.Helper()
	if err := s.start(ctx, terminalwire.Message{Type: "start", Shell: "sh", Rows: 30, Cols: 100, MaxMS: 60000, LeaseMS: terminalwire.LeaseMS}); err != nil {
		t.Fatal(err)
	}
}

func TestControlledDockerBinaryResizeAndConcurrentClose(t *testing.T) {
	f := &controlledFixture{ack: terminalwire.Message{Released: true, Clean: true}}
	s := controlledTestSession(t, context.Background(), f)
	startControlled(t, s, context.Background())
	want := []byte{0, 255, '\r', '\n', 27}
	if n, err := s.Write(want); err != nil || n != len(want) {
		t.Fatal(n, err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(s, got); err != nil || !bytes.Equal(got, want) {
		t.Fatal("内部 PTY 数据被外层复用帧污染", err)
	}
	if err := s.Resize(context.Background(), Size{Columns: 132, Rows: 42}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if f.closeFrames.Load() != 1 || f.transportCloses.Load() != 1 || f.clientCloses.Load() != 1 || f.resizes.Load() != 1 {
		t.Fatal("重复关闭或错误地调用外层 resize")
	}
}

func TestControlledDockerCanceledCallerStillConfirmsCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &controlledFixture{ack: terminalwire.Message{Released: true, Clean: true}}
	s := controlledTestSession(t, ctx, f)
	startControlled(t, s, ctx)
	cancel()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if f.inspectCanceled.Load() != 0 || !f.stopped.Load() {
		t.Fatal("使用了已取消的清理 context")
	}
}

func TestControlledDockerNeverAcceptsUnreleasedWrongExecOrInspectError(t *testing.T) {
	for _, c := range []struct {
		name string
		f    *controlledFixture
	}{
		{"未释放", &controlledFixture{ack: terminalwire.Message{Released: false}}},
		{"异常 worker", &controlledFixture{ack: terminalwire.Message{Released: true, Clean: false}}},
		{"非零 ACK", &controlledFixture{ack: terminalwire.Message{Released: true, Clean: false, Code: 117}}},
		{"缺少 ACK", &controlledFixture{missingACK: true}},
		{"错误 exec", &controlledFixture{ack: terminalwire.Message{Released: true, Clean: true}, inspect: func(context.Context) (client.ExecInspectResult, error) {
			return client.ExecInspectResult{ID: "other", ContainerID: "container"}, nil
		}}},
		{"错误容器", &controlledFixture{ack: terminalwire.Message{Released: true, Clean: true}, inspect: func(context.Context) (client.ExecInspectResult, error) {
			return client.ExecInspectResult{ID: "exec", ContainerID: "other"}, nil
		}}},
		{"inspect 失败", &controlledFixture{ack: terminalwire.Message{Released: true, Clean: true}, inspect: func(context.Context) (client.ExecInspectResult, error) {
			return client.ExecInspectResult{}, io.ErrUnexpectedEOF
		}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := controlledTestSession(t, context.Background(), c.f)
			startControlled(t, s, context.Background())
			if !errors.Is(s.Close(), ErrDockerTerminalRelease) {
				t.Fatal("错误地报告释放成功")
			}
		})
	}
}

func TestControlledDockerProtocolFailureDoesNotReportRelease(t *testing.T) {
	f := &controlledFixture{malformed: true}
	s := controlledTestSession(t, context.Background(), f)
	if err := s.start(context.Background(), terminalwire.Message{Type: "start", Shell: "sh", Rows: 30, Cols: 100, MaxMS: 60000, LeaseMS: terminalwire.LeaseMS}); err == nil {
		t.Fatal("接受无效协议")
	}
	if !errors.Is(s.Close(), ErrDockerTerminalRelease) {
		t.Fatal("无 ACK 却报告释放成功")
	}
}

func TestControlledDockerSlowReaderKeepsControlPumpAndBoundsMemory(t *testing.T) {
	f := &controlledFixture{ack: terminalwire.Message{Released: true, Clean: true}, flood: true, floodStart: make(chan struct{})}
	s := controlledTestSession(t, context.Background(), f)
	startControlled(t, s, context.Background())
	close(f.floodStart)
	select {
	case <-s.closing:
	case <-time.After(3 * time.Second):
		t.Fatal("慢读未触发关闭")
	}
	if !errors.Is(s.Close(), ErrDockerTerminalRelease) {
		t.Fatal("洪泛不应记正常成功")
	}
	if !f.stopped.Load() || f.closeFrames.Load() != 1 || s.result == nil || !s.result.Released {
		t.Fatal("TTY 队列阻断了控制 ACK")
	}
	if len(s.output) != 0 || len(s.pending) != 0 || cap(s.output) != 8 {
		t.Fatal("关闭后仍保留终端缓冲")
	}
}

func TestControlledDockerResourceInvalidationUsesExistingCloseOnly(t *testing.T) {
	f := &controlledFixture{ack: terminalwire.Message{Released: true, Clean: true}}
	s := controlledTestSession(t, context.Background(), f)
	startControlled(t, s, context.Background())
	guard := guardResourceSession(context.Background(), s, func(context.Context) error { return ErrTargetNotReady }, time.Hour)
	if !errors.Is(guard.Resize(context.Background(), Size{Columns: 100, Rows: 30}), ErrTargetNotReady) {
		t.Fatal("失效目标仍可调整")
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	if f.resizes.Load() != 0 || f.closeFrames.Load() != 1 {
		t.Fatal("撤权后执行了新的控制操作")
	}
}

func TestControlledDockerMuxRejectsAllocationSizedHeaders(t *testing.T) {
	for _, n := range []uint32{256*1024 + 1, ^uint32(0)} {
		var header [8]byte
		header[0] = 1
		binary.BigEndian.PutUint32(header[4:], n)
		r := boundedDockerMux{Reader: bytes.NewReader(header[:])}
		if _, err := io.ReadAll(&r); !errors.Is(err, terminalwire.ErrProtocol) {
			t.Fatal("未阻断不可信复用帧长度")
		}
	}
}

func TestControlledDockerCloseDrainsTrailingProtocolBeforeSuccess(t *testing.T) {
	for _, afterInspect := range []bool{false, true} {
		t.Run(map[bool]string{false: "inspect期间", true: "inspect之后"}[afterInspect], func(t *testing.T) {
			gate := make(chan struct{})
			var release sync.Once
			defer release.Do(func() { close(gate) })
			observed := make(chan struct{})
			var observedOnce sync.Once
			var s *dockerSession
			f := &controlledFixture{ack: terminalwire.Message{Released: true, Clean: true}, trailing: true, tailGate: gate}
			f.inspect = func(context.Context) (client.ExecInspectResult, error) {
				observedOnce.Do(func() { close(observed) })
				if !afterInspect {
					release.Do(func() { close(gate) })
					<-s.done
				}
				return client.ExecInspectResult{ID: "exec", ContainerID: "container"}, nil
			}
			s = controlledTestSession(t, context.Background(), f)
			startControlled(t, s, context.Background())
			result := make(chan error, 1)
			go func() { result <- s.Close() }()
			<-observed
			if afterInspect {
				select {
				case err := <-result:
					t.Errorf("输出未结束就返回：%v", err)
				case <-time.After(20 * time.Millisecond):
				}
				release.Do(func() { close(gate) })
			}
			select {
			case err := <-result:
				if !errors.Is(err, ErrDockerTerminalRelease) {
					t.Fatal("尾随无效帧被误报成功", err)
				}
			case <-time.After(time.Second):
				t.Fatal("关闭未闭合")
			}
		})
	}
}

func TestControlledDockerInspectDeadlineRemainsBoundedFailure(t *testing.T) {
	f := &controlledFixture{ack: terminalwire.Message{Released: true, Clean: true}, inspect: func(ctx context.Context) (client.ExecInspectResult, error) {
		<-ctx.Done()
		return client.ExecInspectResult{}, ctx.Err()
	}}
	s := controlledTestSession(t, context.Background(), f)
	startControlled(t, s, context.Background())
	began := time.Now()
	if !errors.Is(s.Close(), ErrDockerTerminalRelease) || time.Since(began) < dockerCleanupTimeout {
		t.Fatal("超时未保持真实失败结果")
	}
	if f.transportCloses.Load() != 1 || f.clientCloses.Load() != 1 {
		t.Fatal("超时后本地连接未释放")
	}
}

func TestControlledDockerStartupSendFailureAndCanceledContextKeepCleanupIndependent(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "发送失败", true: "启动已取消"}[canceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				cancel()
			}
			a, b := net.Pipe()
			_ = b.Close()
			var inspected, closed atomic.Int32
			s := newDockerSession(ctx, a, a, func() { _ = a.Close() }, func() error { closed.Add(1); return nil }, func(probe context.Context) (client.ExecInspectResult, error) {
				if probe.Err() != nil {
					t.Error("清理继承了已取消 context")
				}
				inspected.Add(1)
				return client.ExecInspectResult{ID: "exec", ContainerID: "container", ExitCode: 65}, nil
			}, "exec", "container")
			if err := s.start(ctx, terminalwire.Message{Type: "start", Shell: "sh", Rows: 30, Cols: 100, MaxMS: 60000, LeaseMS: terminalwire.LeaseMS}); err == nil {
				t.Fatal("启动失败却显示成功")
			}
			if !errors.Is(s.Close(), ErrDockerTerminalRelease) || inspected.Load() != 1 || closed.Load() != 1 {
				t.Fatal("缺少确认不能报成功，仍须释放本地连接")
			}
		})
	}
}
