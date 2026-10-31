package terminal

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"edo/internal/terminalwire"
)

const (
	dockerCleanupTimeout = 10 * time.Second
	dockerOutputLimit    = 256 * 1024
)

var errDockerOutputFull = errors.New("终端输出超过读取能力，会话已关闭")

type dockerSession struct {
	ctx                       context.Context
	conn                      net.Conn
	source                    io.Reader
	closeAttach               func()
	closeClient               func() error
	inspect                   func(context.Context) (client.ExecInspectResult, error)
	execID, containerID       string
	output                    bytes.Buffer
	outputMu                  sync.Mutex
	outputReady               chan struct{}
	ready, ack, done, closing chan struct{}
	gate                      chan struct{}
	mu                        sync.Mutex
	result                    *terminalwire.Message
	failure                   error
	readMu                    sync.Mutex
	attachOnce, closeOnce     sync.Once
	closeErr                  error
}

func newDockerSession(ctx context.Context, conn net.Conn, source io.Reader, closeAttach func(), closeClient func() error, inspect func(context.Context) (client.ExecInspectResult, error), execID, containerID string) *dockerSession {
	s := &dockerSession{ctx: ctx, conn: conn, source: source, closeAttach: closeAttach, closeClient: closeClient, inspect: inspect, execID: execID, containerID: containerID, outputReady: make(chan struct{}, 1), ready: make(chan struct{}), ack: make(chan struct{}), done: make(chan struct{}), closing: make(chan struct{}), gate: make(chan struct{}, 1)}
	s.gate <- struct{}{}
	go s.pump()
	return s
}

func (s *dockerSession) closeTransport() { s.attachOnce.Do(s.closeAttach) }
func (s *dockerSession) recordFailure(err error) {
	s.mu.Lock()
	first := s.failure == nil
	if first {
		s.failure = err
	}
	s.mu.Unlock()
	if first {
		go func() { _ = s.Close() }()
	}
}
func (s *dockerSession) failureValue() error { s.mu.Lock(); defer s.mu.Unlock(); return s.failure }

// 预算按未消费字节计算，不把 PTY 的细碎帧误当成大量输出；通知不承载终端数据。
func (s *dockerSession) bufferOutput(data []byte) error {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	select {
	case <-s.closing:
		return nil
	default:
	}
	if len(data) > dockerOutputLimit-s.output.Len() {
		return errDockerOutputFull
	}
	_, _ = s.output.Write(data)
	select {
	case s.outputReady <- struct{}{}:
	default:
	}
	return nil
}

func (s *dockerSession) readOutput(data []byte) int {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	queued := s.output.Bytes()
	n := copy(data, queued)
	clear(queued[:n])
	s.output.Next(n)
	if s.output.Len() == 0 {
		s.output = bytes.Buffer{}
	}
	return n
}

func (s *dockerSession) start(ctx context.Context, start terminalwire.Message) error {
	if err := s.send(ctx, start, false); err != nil {
		return err
	}
	timer := time.NewTimer(7 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return terminalwire.ErrProtocol
	case <-s.done:
		return terminalwire.ErrProtocol
	case <-s.ready:
	}
	if err := s.failureValue(); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.closing:
				return
			case <-s.done:
				return
			case <-ticker.C:
				if err := s.send(context.WithoutCancel(s.ctx), terminalwire.Message{Type: "ping"}, false); err != nil {
					s.recordFailure(err)
					return
				}
			}
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.closing:
		case <-s.done:
		}
	}()
	return nil
}

func (s *dockerSession) send(ctx context.Context, m terminalwire.Message, allowClosing bool) error {
	stop := s.closing
	if allowClosing {
		stop = nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-stop:
		return io.ErrClosedPipe
	case <-s.gate:
	}
	defer func() { s.gate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !allowClosing {
		select {
		case <-s.closing:
			return io.ErrClosedPipe
		default:
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	if err := s.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	return terminalwire.Write(s.conn, m)
}

func (s *dockerSession) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	for {
		select {
		case <-s.closing:
			return 0, io.EOF
		default:
		}
		if n := s.readOutput(b); n > 0 {
			return n, nil
		}
		select {
		case <-s.closing:
			return 0, io.EOF
		case <-s.outputReady:
		case <-s.done:
			if n := s.readOutput(b); n > 0 {
				return n, nil
			}
			if err := s.failureValue(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
	}
}

func (s *dockerSession) Write(b []byte) (int, error) {
	written := 0
	for len(b) > 0 {
		n := min(len(b), terminalwire.MaxData)
		if err := s.send(s.ctx, terminalwire.Message{Type: "data", Data: b[:n]}, false); err != nil {
			return written, err
		}
		written += n
		b = b[n:]
	}
	return written, nil
}
func (s *dockerSession) Resize(ctx context.Context, size Size) error {
	if !validSize(size) {
		return ErrInvalidRequest
	}
	return s.send(ctx, terminalwire.Message{Type: "resize", Rows: size.Rows, Cols: size.Columns}, false)
}

func (s *dockerSession) pump() {
	defer close(s.done)
	w := &dockerProtocolWriter{session: s}
	_, err := stdcopy.StdCopy(w, rejectDockerStderr{}, &boundedDockerMux{Reader: s.source})
	if err == nil && len(w.pending) != 0 {
		err = terminalwire.ErrProtocol
	}
	if err != nil {
		s.recordFailure(err)
	}
}

// PTY 洪泛时丢弃后续数据并关闭本会话，但继续读取控制 ACK，不能被浏览器慢读卡住。
type dockerProtocolWriter struct {
	session *dockerSession
	pending []byte
	ready   bool
	closed  bool
}

func (w *dockerProtocolWriter) Write(b []byte) (int, error) {
	count := len(b)
	for len(b) > 0 {
		at := bytes.IndexByte(b, '\n')
		if at < 0 {
			if len(w.pending)+len(b) > terminalwire.MaxLine {
				return 0, terminalwire.ErrProtocol
			}
			w.pending = append(w.pending, b...)
			return count, nil
		}
		if len(w.pending)+at+1 > terminalwire.MaxLine {
			return 0, terminalwire.ErrProtocol
		}
		w.pending = append(w.pending, b[:at+1]...)
		b = b[at+1:]
		m, err := terminalwire.Decode(w.pending)
		w.pending = w.pending[:0]
		if err != nil || !terminalwire.ValidOutput(m) || w.closed {
			return 0, terminalwire.ErrProtocol
		}
		s := w.session
		switch m.Type {
		case "ready":
			if w.ready {
				return 0, terminalwire.ErrProtocol
			}
			w.ready = true
			close(s.ready)
		case "data":
			if !w.ready {
				return 0, terminalwire.ErrProtocol
			}
			if err := s.bufferOutput(m.Data); err != nil {
				s.recordFailure(err)
			}
			clear(m.Data)
		case "closed":
			if !w.ready {
				return 0, terminalwire.ErrProtocol
			}
			w.closed = true
			s.mu.Lock()
			s.result = &m
			s.mu.Unlock()
			close(s.ack)
		}
	}
	return count, nil
}

type rejectDockerStderr struct{}

func (rejectDockerStderr) Write(b []byte) (int, error) { return 0, terminalwire.ErrProtocol }

// 先限制官方 stdcopy 将要分配的单个复用帧，避免不可信长度造成大内存分配。
type boundedDockerMux struct {
	io.Reader
	header    [8]byte
	offset    int
	remaining uint32
}

func (r *boundedDockerMux) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if r.remaining == 0 && r.offset == 0 {
		if _, err := io.ReadFull(r.Reader, r.header[:]); err != nil {
			return 0, err
		}
		if r.header[0] != 1 && r.header[0] != 2 || r.header[1] != 0 || r.header[2] != 0 || r.header[3] != 0 {
			return 0, terminalwire.ErrProtocol
		}
		r.remaining = binary.BigEndian.Uint32(r.header[4:])
		if r.remaining > 256*1024 {
			return 0, terminalwire.ErrProtocol
		}
		r.offset = 8
	}
	if r.offset > 0 {
		n := copy(b, r.header[8-r.offset:])
		r.offset -= n
		return n, nil
	}
	n, err := r.Reader.Read(b[:min(len(b), int(r.remaining))])
	r.remaining -= uint32(n)
	return n, err
}

func (s *dockerSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.closing)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), dockerCleanupTimeout)
		defer cancel()
		defer func() {
			// 必须等完整输出结束后再确认最终协议状态；不能在 ACK 后截断尾帧。
			select {
			case <-s.done:
			case <-ctx.Done():
				slog.WarnContext(ctx, "等待受控终端完整输出结束超时", "operation", "terminal_docker_close_stream_timeout", "exec_id", s.execID, "err", ctx.Err())
				s.closeErr = ErrDockerTerminalRelease
				s.closeTransport()
			}
			s.closeTransport()
			joined := time.NewTimer(time.Second)
			select {
			case <-s.done:
			case <-joined.C:
				slog.WarnContext(ctx, "受控终端本地读取协程未结束", "operation", "terminal_docker_close_reader_timeout", "exec_id", s.execID)
				s.closeErr = ErrDockerTerminalRelease
			}
			joined.Stop()
			if err := s.failureValue(); err != nil {
				slog.WarnContext(ctx, "受控终端输出最终核验失败", "operation", "terminal_docker_close_final_protocol", "exec_id", s.execID, "err", err)
				s.closeErr = ErrDockerTerminalRelease
			}
			s.outputMu.Lock()
			clear(s.output.Bytes())
			s.output = bytes.Buffer{}
			s.outputMu.Unlock()
			if err := s.closeClient(); err != nil {
				slog.WarnContext(ctx, "关闭 Docker 终端客户端失败", "operation", "terminal_docker_client_close", "exec_id", s.execID, "err", err)
				s.closeErr = ErrDockerTerminalRelease
			}
		}()
		select {
		case <-s.ack:
		default:
			if err := s.send(ctx, terminalwire.Message{Type: "close"}, true); err != nil {
				slog.WarnContext(ctx, "发送受控终端关闭帧失败", "operation", "terminal_docker_close_frame", "exec_id", s.execID, "err", err)
			}
		}
		select {
		case <-s.ack:
		case <-s.done:
			s.closeTransport()
		case <-ctx.Done():
			s.closeTransport()
		}
		s.mu.Lock()
		result := s.result
		failure := s.failure
		s.mu.Unlock()
		if result == nil || !result.Released || !result.Clean || result.Code != 0 || failure != nil {
			s.closeErr = ErrDockerTerminalRelease
			released, clean, code := false, false, 0
			if result != nil {
				released, clean, code = result.Released, result.Clean, result.Code
			}
			slog.WarnContext(ctx, "受控终端未返回完整回收确认", "operation", "terminal_docker_close_ack", "exec_id", s.execID, "ack_received", result != nil, "released", released, "clean", clean, "code", code, "err", failure)
		}
		for {
			current, err := s.inspect(ctx)
			if err != nil || current.ID != s.execID || current.ContainerID != s.containerID {
				s.closeErr = ErrDockerTerminalRelease
				slog.WarnContext(ctx, "核验受控终端执行身份失败", "operation", "terminal_docker_close_inspect", "exec_id", s.execID, "container_id", s.containerID, "identity_match", current.ID == s.execID && current.ContainerID == s.containerID, "err", err)
				break
			}
			if !current.Running {
				if current.ExitCode != 0 {
					s.closeErr = ErrDockerTerminalRelease
					slog.WarnContext(ctx, "受控终端异常退出", "operation", "terminal_docker_close_exit", "exec_id", s.execID, "exit_code", current.ExitCode)
				}
				break
			}
			timer := time.NewTimer(25 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				slog.WarnContext(ctx, "等待受控终端执行停止超时", "operation", "terminal_docker_close_exec_timeout", "exec_id", s.execID, "err", ctx.Err())
				s.closeErr = ErrDockerTerminalRelease
			case <-timer.C:
			}
			if ctx.Err() != nil {
				break
			}
		}
	})
	return s.closeErr
}

var _ Session = (*dockerSession)(nil)
