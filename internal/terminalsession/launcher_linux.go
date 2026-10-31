//go:build linux && (amd64 || arm64)

package terminalsession

import (
	"io"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"edo/internal/terminalsession/policy"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

func installPolicy() error {
	action := uint32(unix.SECCOMP_RET_KILL_PROCESS)
	result, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_GET_ACTION_AVAIL, 0, uintptr(unsafe.Pointer(&action)))
	if errno != 0 {
		return errno
	}
	if result != 0 {
		return unix.EBUSY
	}
	i, err := policy.Instructions(runtime.GOARCH)
	if err != nil {
		return err
	}
	raw, err := bpf.Assemble(i)
	if err != nil {
		return err
	}
	filters := make([]unix.SockFilter, len(raw))
	for i, x := range raw {
		filters[i] = unix.SockFilter{Code: x.Op, Jt: x.Jt, Jf: x.Jf, K: x.K}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	program := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	result, _, errno = unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&program)))
	runtime.KeepAlive(filters)
	if errno != 0 {
		return errno
	}
	// TSYNC 失败也可能返回正 TID 而 errno 为零，不能当作成功释放 gate。
	if result != 0 {
		return unix.EBUSY
	}
	return nil
}

func Launcher(shell string) int {
	command, ok := shellPath(shell)
	if !ok {
		return 79
	}
	if unix.Gettid() != unix.Getpid() {
		return 80
	}
	gate, ready := os.NewFile(3, "private-gate"), os.NewFile(4, "private-ready")
	if gate == nil || ready == nil {
		return 81
	}
	defer gate.Close()
	defer ready.Close()
	if installPolicy() != nil {
		return 82
	}
	if _, err := ready.Write([]byte{0xa5}); err != nil {
		return 83
	}
	var permit [1]byte
	if _, err := io.ReadFull(gate, permit[:]); err != nil || permit[0] != 0x5a {
		return 84
	}
	gate.Close()
	ready.Close()
	// 只有可信 worker 已原子启用全部追踪选项后才会释放 gate。
	if syscall.Exec(command, []string{command}, shellEnvironment(os.Environ())) != nil {
		return 85
	}
	return 86
}
