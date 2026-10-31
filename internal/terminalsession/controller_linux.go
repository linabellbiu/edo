//go:build linux && (amd64 || arm64)

package terminalsession

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	wire "edo/internal/terminalwire"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func Controller() int {
	reader := bufio.NewReaderSize(os.Stdin, wire.MaxLine)
	_ = os.Stdin.SetReadDeadline(time.Now().Add(3 * time.Second))
	start, err := wire.Read(reader)
	if err != nil || !wire.ValidStart(start) {
		return 65
	}
	_ = os.Stdin.SetReadDeadline(time.Time{})
	if unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != nil {
		return 66
	}
	master, slave, err := pty.Open()
	if err != nil {
		return 67
	}
	defer master.Close()
	defer slave.Close()
	if pty.Setsize(master, &pty.Winsize{Rows: start.Rows, Cols: start.Cols}) != nil {
		return 68
	}
	controlR, controlW, err := os.Pipe()
	if err != nil {
		return 69
	}
	defer controlR.Close()
	defer controlW.Close()
	eventR, eventW, err := os.Pipe()
	if err != nil {
		return 70
	}
	defer eventR.Close()
	defer eventW.Close()
	self, err := os.Executable()
	if err != nil {
		return 71
	}
	cmd := exec.Command(self, "worker", start.Shell)
	cmd.Env = os.Environ()
	cmd.Stdin = controlR
	cmd.Stdout = eventW
	cmd.Stderr = nil
	cmd.ExtraFiles = []*os.File{slave}
	if cmd.Start() != nil {
		return 72
	}
	defer cmd.Process.Release()
	controlR.Close()
	eventW.Close()
	slave.Close()
	failures := newControllerFailure()
	_ = eventR.SetReadDeadline(time.Now().Add(4 * time.Second))
	var ready [1]byte
	if _, err := io.ReadFull(eventR, ready[:]); err != nil || ready[0] != 0xa5 {
		controlW.Close()
		return finish(cmd.Process.Pid, false, nil, failures)
	}
	_ = eventR.SetReadDeadline(time.Time{})
	output := controllerOutput{failures: failures, write: func(m wire.Message) error {
		_ = os.Stdout.SetWriteDeadline(time.Now().Add(2 * time.Second))
		return wire.Write(os.Stdout, m)
	}}
	send := output.send
	if send(wire.Message{Type: "ready", NativeABI: true}) != nil {
		controlW.Close()
		return finish(cmd.Process.Pid, false, nil, failures)
	}
	fail := func() { failures.record(0) }
	input := newControllerInput(master, func() { failures.record(inputFailureCode) })
	defer input.stop()
	go func() {
		b := make([]byte, 16*1024)
		for {
			n, e := master.Read(b)
			if n > 0 && send(wire.Message{Type: "data", Data: b[:n]}) != nil {
				return
			}
			if e != nil {
				fail()
				return
			}
		}
	}()
	ping := make(chan struct{}, 1)
	go func() {
		for {
			m, e := wire.Read(reader)
			if e != nil {
				if errors.Is(e, io.EOF) {
					fail()
				} else {
					failures.record(inputFailureCode)
				}
				return
			}
			if !wire.ValidControl(m) {
				failures.record(inputFailureCode)
				return
			}
			switch m.Type {
			case "data":
				if !input.offer(m.Data) {
					return
				}
			case "resize":
				if m.Rows < 5 || m.Rows > 200 || m.Cols < 20 || m.Cols > 500 || pty.Setsize(master, &pty.Winsize{Rows: m.Rows, Cols: m.Cols}) != nil {
					failures.record(inputFailureCode)
					return
				}
			case "ping":
				select {
				case ping <- struct{}{}:
				default:
				}
			case "close":
				fail()
				return
			default:
				failures.record(inputFailureCode)
				return
			}
		}
	}()
	workerExit := make(chan struct{}, 1)
	go func() { var b [1]byte; _, _ = eventR.Read(b[:]); workerExit <- struct{}{} }()
	deadline, lease := time.NewTimer(time.Duration(start.MaxMS)*time.Millisecond), time.NewTimer(time.Duration(start.LeaseMS)*time.Millisecond)
	defer deadline.Stop()
	defer lease.Stop()
	running := true
	for running {
		select {
		case <-ping:
			if !lease.Stop() {
				select {
				case <-lease.C:
				default:
				}
			}
			lease.Reset(time.Duration(start.LeaseMS) * time.Millisecond)
		case <-failures.signal:
			running = false
		case <-workerExit:
			running = false
		case <-deadline.C:
			running = false
		case <-lease.C:
			running = false
		}
	}
	input.stop()
	controlW.Close()
	master.Close()
	return finish(cmd.Process.Pid, true, &output, failures)
}

func finish(workerPID int, started bool, output *controllerOutput, failures *controllerFailure) int {
	end := time.Now().Add(5 * time.Second)
	workerSeen, workerOK, count := false, false, 0
	for time.Now().Before(end) {
		var st unix.WaitStatus
		pid, err := unix.Wait4(-1, &st, unix.WALL|unix.WNOHANG, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.ECHILD) {
			if output != nil {
				return output.reaped(started, workerSeen && workerOK, count)
			}
			_, code := reapedResult(started, workerSeen && workerOK, count, int(failures.code.Load()))
			return code
		}
		if err != nil {
			return 77
		}
		if pid == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		count++
		if pid == workerPID {
			workerSeen = true
			workerOK = st.Exited() && st.ExitStatus() == 0
		}
	}
	// 超时只报告失败；不 detach、按其他 PID 清场或停止业务容器。
	if output != nil {
		_ = output.send(wire.Message{Type: "closed", Released: false, Code: 78})
	}
	return 78
}
