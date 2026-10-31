//go:build linux && (amd64 || arm64)

package terminalsession

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"edo/internal/terminalsession/traceplan"
	"golang.org/x/sys/unix"
)

const traceOptions = unix.PTRACE_O_EXITKILL | unix.PTRACE_O_TRACEFORK | unix.PTRACE_O_TRACEVFORK | unix.PTRACE_O_TRACECLONE | unix.PTRACE_O_TRACEEXEC | unix.PTRACE_O_TRACEEXIT

func ptraceRequest(request, tid int, options uintptr) error {
	_, _, e := unix.Syscall6(unix.SYS_PTRACE, uintptr(request), uintptr(tid), 0, options, 0, 0)
	if e != 0 {
		return e
	}
	return nil
}

func groupStop(sig unix.Signal) bool {
	return sig == unix.SIGSTOP || sig == unix.SIGTSTP || sig == unix.SIGTTIN || sig == unix.SIGTTOU
}

func Worker(shell string) int {
	runtime.LockOSThread()
	if _, ok := shellPath(shell); !ok {
		return 89
	}
	// worker 整体退出才结束追踪关系；从不 PTRACE_DETACH。
	go func() { var b [1]byte; _, _ = os.Stdin.Read(b[:]); os.Exit(0) }()
	tty := os.NewFile(3, "session-tty")
	if tty == nil {
		return 90
	}
	defer tty.Close()
	gateR, gateW, err := os.Pipe()
	if err != nil {
		return 91
	}
	defer gateR.Close()
	defer gateW.Close()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return 92
	}
	defer readyR.Close()
	defer readyW.Close()
	self, err := os.Executable()
	if err != nil {
		return 93
	}
	cmd := exec.Command(self, "launcher", shell)
	cmd.Env = os.Environ()
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.ExtraFiles = []*os.File{gateR, readyW}
	pidfd := -1
	cmd.SysProcAttr = &unixProcAttr{Setsid: true, Setctty: true, Ctty: 0, Pdeathsig: unix.SIGKILL, PidFD: &pidfd}
	if cmd.Start() != nil {
		return 94
	}
	defer cmd.Process.Release()
	if pidfd >= 0 {
		defer unix.Close(pidfd)
	}
	gateR.Close()
	readyW.Close()
	if err := readyR.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return 95
	}
	var ready [1]byte
	if _, err := io.ReadFull(readyR, ready[:]); err != nil || ready[0] != 0xa5 {
		return 96
	}
	root := cmd.Process.Pid
	if err := ptraceRequest(unix.PTRACE_SEIZE, root, traceOptions); err != nil {
		return 97
	}
	if _, err := gateW.Write([]byte{0x5a}); err != nil {
		return 98
	}
	gateW.Close()
	if _, err := os.Stdout.Write([]byte{0xa5}); err != nil {
		return 99
	}
	ledger := traceplan.New(root)
	for {
		var st unix.WaitStatus
		tid, err := unix.Wait4(-1, &st, unix.WALL, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 100
		}
		if st.Exited() || st.Signaled() {
			ledger.Exit(tid)
			if tid == root {
				return 0
			}
			continue
		}
		if !st.Stopped() {
			return 101
		}
		event := int(st) >> 16
		if event == unix.PTRACE_EVENT_EXEC {
			old, err := unix.PtraceGetEventMsg(tid)
			if err != nil {
				return 102
			}
			if !ledger.Exec(tid, int(old)) {
				return 103
			}
			if int(old) == root {
				root = tid
			}
		}
		action := ledger.Stop(tid, event == unix.PTRACE_EVENT_STOP && st.StopSignal() == unix.SIGTRAP, event == unix.PTRACE_EVENT_STOP && groupStop(st.StopSignal()))
		if action == "reject" {
			return 104
		}
		if action == "pending" {
			continue
		}
		if action == "initial" {
			if unix.PtraceCont(tid, 0) != nil {
				return 106
			}
			continue
		}
		if event == unix.PTRACE_EVENT_FORK || event == unix.PTRACE_EVENT_VFORK || event == unix.PTRACE_EVENT_CLONE {
			child, err := unix.PtraceGetEventMsg(tid)
			if err != nil || child == 0 || len(ledger.Known) >= 256 {
				return 105
			}
			if ledger.Register(int(child)) {
				if unix.PtraceCont(int(child), 0) != nil {
					return 106
				}
			}
		}
		if action == "listen" {
			if ptraceRequest(unix.PTRACE_LISTEN, tid, 0) != nil {
				return 107
			}
			continue
		}
		signal := 0
		if event == 0 {
			signal = int(st.StopSignal())
		}
		if err := unix.PtraceCont(tid, signal); err != nil && !errors.Is(err, unix.ESRCH) {
			return 108
		}
	}
}
