//go:build linux || darwin

package k3s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// macOS 只在临时 PATH 内模拟缺失的 Linux 工具；Linux 仍验证真实 GNU 命令。
func writeLinuxToolFixtures(t *testing.T, directory string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"timeout", "sha256sum", "ln"} {
		fixture := filepath.Join(directory, "edo-fixture-"+tool)
		body := "EDO_K3S_LINUX_TOOL_FIXTURE=1 GORACE=atexit_sleep_ms=0 exec " + shellQuote(executable) + " -test.run='^TestK3sLinuxToolFixtureProcess$' -- " + tool + " \"$@\"\n"
		writePreflightCommand(t, directory, filepath.Base(fixture), body)
		writePreflightCommand(t, directory, tool, "exec "+shellQuote(fixture)+" \"$@\"\n")
	}
}

func TestK3sLinuxToolFixtureProcess(t *testing.T) {
	if os.Getenv("EDO_K3S_LINUX_TOOL_FIXTURE") != "1" {
		return
	}
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		os.Exit(125)
	}
	args := os.Args[separator+2:]
	switch os.Args[separator+1] {
	case "timeout":
		os.Exit(runTimeoutFixture(args))
	case "sha256sum":
		os.Exit(runChecksumFixture(args))
	case "ln":
		os.Exit(runNoClobberLinkFixture(args))
	default:
		os.Exit(125)
	}
}

func runTimeoutFixture(args []string) int {
	if len(args) < 2 {
		return 125
	}
	duration, err := time.ParseDuration(args[0] + "s")
	if err != nil || duration <= 0 {
		return 125
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	command := exec.CommandContext(ctx, args[1], args[2:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		// 超时结束该夹具自己的子进程组，避免 Shell 的后代继续占用管道或运行。
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = 250 * time.Millisecond
	err = command.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return 124
	}
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() >= 0 {
		return exitError.ExitCode()
	}
	return 127
}

func runChecksumFixture(args []string) int {
	if len(args) != 2 || args[0] != "-c" {
		return 2
	}
	manifest, err := os.ReadFile(args[1])
	if err != nil {
		return 1
	}
	checked := 0
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		hash, file, found := strings.Cut(line, "  ")
		if !found {
			return 1
		}
		expected, err := hex.DecodeString(hash)
		if err != nil || len(expected) != sha256.Size {
			return 1
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return 1
		}
		actual := sha256.Sum256(data)
		if !strings.EqualFold(hash, hex.EncodeToString(actual[:])) {
			return 1
		}
		checked++
	}
	if checked == 0 {
		return 1
	}
	return 0
}

func runNoClobberLinkFixture(args []string) int {
	if len(args) != 3 || args[0] != "-T" {
		return 2
	}
	// 与 GNU ln -T 相同，目标已存在、为目录或符号链接时均原子失败。
	if err := os.Link(args[1], args[2]); err != nil {
		return 1
	}
	return 0
}

func TestTimeoutFixtureIsBoundedAndKeepsExitStatus(t *testing.T) {
	if status := runTimeoutFixture([]string{"1", "sh", "-c", "exit 37"}); status != 37 {
		t.Fatalf("未保留命令退出状态: %d", status)
	}
	started := time.Now()
	if status := runTimeoutFixture([]string{"0.1", "sh", "-c", "sleep 5 & wait"}); status != 124 {
		t.Fatalf("超时必须返回 124: %d", status)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("超时后子进程组未有界退出: %s", elapsed)
	}
}

func TestChecksumFixtureValidatesRealContent(t *testing.T) {
	directory := t.TempDir()
	file, manifest := filepath.Join(directory, "binary"), filepath.Join(directory, "sha256.txt")
	if err := os.WriteFile(file, []byte("reviewed-binary"), 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("reviewed-binary"))
	if err := os.WriteFile(manifest, []byte(hex.EncodeToString(hash[:])+"  "+file+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if status := runChecksumFixture([]string{"-c", manifest}); status != 0 {
		t.Fatalf("真实摘要未通过: %d", status)
	}
	if err := os.WriteFile(file, []byte("replaced-binary"), 0600); err != nil {
		t.Fatal(err)
	}
	if status := runChecksumFixture([]string{"-c", manifest}); status == 0 {
		t.Fatal("内容已变化却通过摘要校验")
	}
}
