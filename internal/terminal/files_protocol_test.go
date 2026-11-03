package terminal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/terminalfiles"

	corev1 "k8s.io/api/core/v1"
	utilexec "k8s.io/client-go/util/exec"
)

func testFileBackend(t *testing.T, run fileExec) *containerFiles {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f := &containerFiles{ctx: ctx, cancel: cancel, identity: t.Name(), run: run, check: func(context.Context) error { return nil }}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestContainerFilesPathIsIndependentArgument(t *testing.T) {
	p := "/tmp/--name '\";$(touch injected)\nfile"
	f := testFileBackend(t, func(_ context.Context, args []string, _ io.Reader, out io.Writer) error {
		if !reflect.DeepEqual(args, fileCommand(fileListScript, p)) || strings.Contains(args[2], p) {
			t.Fatal("文件名进入脚本")
		}
		_, err := io.WriteString(out, "--name '\";$(touch injected)\nfile\x00f\x003\x001700000000.123\x00644\x00")
		return err
	})
	directory, err := f.List(context.Background(), p)
	if err != nil || len(directory.Entries) != 1 || directory.Entries[0].Name != "--name '\";$(touch injected)\nfile" {
		t.Fatalf("特殊名称协议不完整: %+v %v", directory, err)
	}
}

func TestContainerFilesListHasStrictBoundedNULProtocol(t *testing.T) {
	for _, broken := range []string{"a\nf\n3\n1\n644\n", "a\x00f\x003\x001\x00644", "../escape\x00f\x003\x001\x00644\x00", "a\x00f\x00-3\x001\x00644\x00"} {
		f := testFileBackend(t, func(_ context.Context, _ []string, _ io.Reader, out io.Writer) error {
			_, err := io.WriteString(out, broken)
			return err
		})
		if _, err := f.List(context.Background(), "/tmp"); err == nil {
			t.Fatalf("无效协议未拒绝: %q", broken)
		}
	}
	f := testFileBackend(t, func(_ context.Context, _ []string, _ io.Reader, out io.Writer) error {
		for n := 0; n <= terminalfiles.MaxEntries; n++ {
			if _, err := fmt.Fprintf(out, "file%d\x00f\x001\x001700000000\x00600\x00", n); err != nil {
				return err
			}
		}
		return nil
	})
	directory, err := f.List(context.Background(), "/tmp")
	if err != nil || len(directory.Entries) != terminalfiles.MaxEntries || !directory.Truncated {
		t.Fatalf("目录未有界截断: %d %+v %v", len(directory.Entries), directory.Truncated, err)
	}
}

func TestContainerFilesOpenIsStreamingAndRejectsSpecialEntries(t *testing.T) {
	f := testFileBackend(t, func(_ context.Context, _ []string, _ io.Reader, out io.Writer) error {
		_, err := io.WriteString(out, "/tmp/a\x00regular file\x003\x001700000000\x00640\x00abc")
		return err
	})
	r, entry, err := f.Open(context.Background(), "/tmp/a")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil || string(data) != "abc" || entry.Kind != "file" || entry.Mode != 0640 {
		t.Fatalf("读取结果错误: %q %+v %v", data, entry, err)
	}
	for _, kind := range []string{"symbolic link", "fifo", "character special file", "directory"} {
		t.Run(kind, func(t *testing.T) {
			f := testFileBackend(t, func(_ context.Context, _ []string, _ io.Reader, out io.Writer) error {
				_, err := fmt.Fprintf(out, "/tmp/a\x00%s\x003\x001700000000\x00640\x00", kind)
				return err
			})
			if _, _, err := f.Open(context.Background(), "/tmp/a"); !errors.Is(err, terminalfiles.ErrNotRegular) {
				t.Fatalf("特殊文件未拒绝: %v", err)
			}
		})
	}
}

func TestContainerFilesTransferLimitAndInputLength(t *testing.T) {
	f := testFileBackend(t, func(_ context.Context, _ []string, input io.Reader, out io.Writer) error {
		return emulateFileCommit(input, out, 3, nil)
	})
	if _, err := f.Put(context.Background(), "/tmp/a", strings.NewReader("abc"), 3, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Put(context.Background(), "/tmp/a", strings.NewReader("abcdefgh"), 3, ""); !errors.Is(err, terminalfiles.ErrTooLarge) {
		t.Fatalf("超出声明大小的输入仍可提交: %v", err)
	}
	if _, err := f.Put(context.Background(), "/tmp/a", strings.NewReader("ab"), 3, ""); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("不足声明大小的输入仍可提交: %v", err)
	}
	if _, err := f.Put(context.Background(), "/tmp/a", strings.NewReader(""), terminalfiles.MaxTransferBytes+1, ""); !errors.Is(err, terminalfiles.ErrTooLarge) {
		t.Fatal(err)
	}
	if _, err := f.Put(context.Background(), "/tmp/a", strings.NewReader(""), 0, "weak-etag"); !errors.Is(err, terminalfiles.ErrConflict) {
		t.Fatal(err)
	}
	stream := &fileStream{ReadCloser: io.NopCloser(strings.NewReader("abcd")), cancel: func() {}, remaining: 3}
	data, err := io.ReadAll(stream)
	if string(data) != "abc" || !errors.Is(err, terminalfiles.ErrTooLarge) {
		t.Fatalf("超限流读取错误: %q %v", data, err)
	}
}

func emulateFileCommit(input io.Reader, output io.Writer, size int64, staged func()) error {
	if _, err := io.CopyN(io.Discard, input, size); err != nil {
		return err
	}
	if staged != nil {
		staged()
	}
	type inputResult struct {
		data []byte
		err  error
	}
	read := make(chan inputResult, 1)
	go func() { data, err := io.ReadAll(input); read <- inputResult{data, err} }()
	_, err := io.WriteString(output, fileCommitReady)
	result := <-read
	if err != nil {
		return err
	}
	if result.err != nil {
		return result.err
	}
	if string(result.data) != fileCommitPermit {
		return fmt.Errorf("未收到明确提交许可")
	}
	_, err = fmt.Fprintf(output, "./a\x00regular file\x00%d\x001700000000\x00600\x00", size)
	return err
}

func TestContainerFilesCommitRechecksAuthorizationAndIdentity(t *testing.T) {
	for _, action := range []string{"撤销文件权限", "目标身份变化", "取消请求"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var staged atomic.Bool
			f := testFileBackend(t, func(_ context.Context, _ []string, input io.Reader, out io.Writer) error {
				return emulateFileCommit(input, out, 3, func() {
					staged.Store(true)
					if action == "取消请求" {
						cancel()
					}
				})
			})
			f.check = func(context.Context) error {
				if staged.Load() && action == "目标身份变化" {
					return terminalfiles.ErrConflict
				}
				return nil
			}
			ctx = terminalfiles.WithCommitAuthorization(ctx, func(context.Context) error {
				if staged.Load() && action == "撤销文件权限" {
					return fs.ErrPermission
				}
				return nil
			})
			_, err := f.Put(ctx, "/tmp/a", strings.NewReader("abc"), 3, "")
			want := map[string]error{"撤销文件权限": fs.ErrPermission, "目标身份变化": terminalfiles.ErrConflict, "取消请求": context.Canceled}[action]
			if !errors.Is(err, want) {
				t.Fatalf("暂存后失效仍发送提交许可: %v", err)
			}
		})
	}
}

func TestContainerFilesCommitRejectsMissingOrMalformedReady(t *testing.T) {
	for _, response := range []string{"", "untrusted-ready\n", "EDO-FILE-READY"} {
		f := testFileBackend(t, func(_ context.Context, _ []string, _ io.Reader, output io.Writer) error {
			_, err := io.WriteString(output, response)
			return err
		})
		if _, err := f.Put(context.Background(), "/tmp/a", strings.NewReader("abc"), 3, ""); !errors.Is(err, terminalfiles.ErrUnavailable) {
			t.Fatalf("无有效 ready 仍提交: %q %v", response, err)
		}
	}
}

func TestContainerFilesCancellationAndRuntimeChangesStopTransfer(t *testing.T) {
	started := make(chan struct{})
	f := testFileBackend(t, func(ctx context.Context, _ []string, _ io.Reader, _ io.Writer) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	var changed atomic.Bool
	f.check = func(context.Context) error {
		if changed.Load() {
			return terminalfiles.ErrConflict
		}
		return nil
	}
	result := make(chan error, 1)
	go func() { _, err := f.List(context.Background(), "/tmp"); result <- err }()
	<-started
	changed.Store(true)
	select {
	case err := <-result:
		if !errors.Is(err, terminalfiles.ErrConflict) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("运行时更改未停止传输")
	}
	f.Close()
	if _, err := f.List(context.Background(), "/tmp"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestContainerFilesExecuteSettlesRunBeforeStoppingValidation(t *testing.T) {
	remoteFailure := errors.New("测试远端操作失败")
	for _, test := range []struct {
		name     string
		runError error
		checkErr error
		external string
		want     error
	}{
		{name: "成功后的校验取消不改写结果"},
		{name: "远端失败后的校验取消保留原错", runError: remoteFailure, want: remoteFailure},
		{name: "真实核权失败仍拒绝", checkErr: fs.ErrPermission, want: fs.ErrPermission},
		{name: "父请求取消仍拒绝", external: "request", want: context.Canceled},
		{name: "后端租约取消仍拒绝", external: "backend", want: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			checking, checked := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			var backend *containerFiles
			backend = testFileBackend(t, func(context.Context, []string, io.Reader, io.Writer) error {
				select {
				case <-checking:
				case <-ctx.Done():
					return ctx.Err()
				}
				if test.external != "" {
					if test.external == "request" {
						cancel()
					} else {
						backend.cancel()
					}
					// 先让外部取消被校验器实际观测，再允许 run 返回成功。
					select {
					case <-checked:
					case <-time.After(time.Second):
						return errors.New("测试外部取消未收口")
					}
				}
				return test.runError
			})
			backend.check = func(checkCtx context.Context) error {
				if calls.Add(1) == 1 {
					return nil
				}
				close(checking)
				<-checkCtx.Done()
				defer close(checked)
				if test.checkErr != nil {
					return test.checkErr
				}
				return fmt.Errorf("测试目标校验结束: %w", checkCtx.Err())
			}
			got := backend.execute(ctx, nil, nil, io.Discard)
			if !errors.Is(got, test.want) || test.want == nil && got != nil {
				t.Fatalf("正常收尾覆盖了运行结果或放过外部失效: got=%v want=%v", got, test.want)
			}
			if calls.Load() != 2 {
				t.Fatalf("未精确进入一次在途周期校验: checks=%d", calls.Load())
			}
			select {
			case <-checked:
			default:
				t.Fatal("execute 返回时校验协程仍未结束")
			}
		})
	}
}

func TestContainerFilesPathLocksAreBoundedAndCancelable(t *testing.T) {
	release, err := lockFilePath(context.Background(), "same-path")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := lockFilePath(ctx, "same-path"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	release()
	var releases []func()
	for n := 0; n < 256; n++ {
		fn, err := lockFilePath(context.Background(), fmt.Sprintf("bound-%d", n))
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, fn)
	}
	if _, err := lockFilePath(context.Background(), "overflow"); !errors.Is(err, terminalfiles.ErrUnavailable) {
		t.Fatal(err)
	}
	for _, fn := range releases {
		fn()
	}
	filePathLocks.Lock()
	left := len(filePathLocks.entries)
	filePathLocks.Unlock()
	if left != 0 {
		t.Fatalf("锁表残留 %d", left)
	}
}

func TestContainerFilesPodIdentityRejectsReplacementAndStoppedContainer(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", ContainerID: "containerd://fixed", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	if got := filePodContainerIdentity(pod, "app"); got != "containerd://fixed" {
		t.Fatal(got)
	}
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}
	if got := filePodContainerIdentity(pod, "app"); got != "" {
		t.Fatal("已停止容器仍可使用文件通道")
	}
	if got := filePodContainerIdentity(pod, "other"); got != "" {
		t.Fatal("选择错误容器")
	}
}

func TestContainerFilesProtocolExitCodesAreSafe(t *testing.T) {
	for code, want := range map[int]error{64: terminalfiles.ErrUnavailable, 65: terminalfiles.ErrInvalidPath, 66: terminalfiles.ErrNotRegular, 67: terminalfiles.ErrTooLarge, 68: terminalfiles.ErrConflict, 69: terminalfiles.ErrExists, 71: fs.ErrNotExist, 72: fs.ErrPermission, 73: terminalfiles.ErrUnsafeDirectory, 127: terminalfiles.ErrUnavailable} {
		if got := fileExecError(fileExecExitError(code)); !errors.Is(got, want) {
			t.Fatalf("%d: %v", code, got)
		}
	}
}

func TestContainerFilesUnsafeDirectoryKeepsSafeDiagnostic(t *testing.T) {
	remote := utilexec.CodeExitError{Err: errors.New("fixture_private_remote_message"), Code: 73}
	err := fileExecError(remote)
	var status fileExecExitError
	if !errors.Is(err, terminalfiles.ErrUnsafeDirectory) || !errors.As(err, &status) || status != 73 {
		t.Fatal("安全目录拒绝未保留稳定错误和数字协议状态")
	}
	if strings.Contains(err.Error(), "fixture_private") {
		t.Fatal("安全目录错误包含了远端原始输出")
	}
}

func TestContainerFilesOpenMapsRemoteMissingPermissionAndType(t *testing.T) {
	for _, test := range []struct {
		name string
		code int
		want error
	}{
		{"路径不存在", 71, fs.ErrNotExist}, {"文件不可读", 72, fs.ErrPermission}, {"非普通文件", 66, terminalfiles.ErrNotRegular},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote := utilexec.CodeExitError{Err: errors.New("固定文件协议退出"), Code: test.code}
			f := testFileBackend(t, func(context.Context, []string, io.Reader, io.Writer) error { return fileExecError(remote) })
			reader, _, err := f.Open(context.Background(), "/tmp/new.txt")
			if reader != nil {
				reader.Close()
				t.Fatal("失败路径不应返回文件流")
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("远端状态未正确映射: %v", err)
			}
			if test.code != 66 {
				var exit interface{ ExitStatus() int }
				if !errors.As(err, &exit) || exit.ExitStatus() != test.code {
					t.Fatal("内部诊断丢失原始退出状态")
				}
			}
		})
	}
}

func TestContainerFilesPathChecksDistinguishMissingTypeAndPermission(t *testing.T) {
	// 直接运行生产协议中的路径检查函数，不运行Linux工具探测或连接真实容器。
	start := strings.Index(fileScriptPrelude, "require_path() {")
	if start < 0 {
		start = strings.Index(fileScriptPrelude, "regular() {")
	}
	end := strings.LastIndex(fileScriptPrelude, "\np=$1")
	if start < 0 || end <= start {
		t.Fatal("生产路径检查函数缺失")
	}
	checks := fileScriptPrelude[start:end]
	dir := t.TempDir()
	plain := dir + "/plain"
	if err := os.WriteFile(plain, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	link := dir + "/link"
	if err := os.Symlink(plain, link); err != nil {
		t.Fatal(err)
	}
	dangling := dir + "/dangling"
	if err := os.Symlink(dir+"/absent-target", dangling); err != nil {
		t.Fatal(err)
	}
	denied := dir + "/denied"
	if err := os.Mkdir(denied, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(denied, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(denied, 0700) })
	unreadable := dir + "/unreadable"
	if err := os.WriteFile(unreadable, []byte("fixture"), 0000); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, path string
		want       error
	}{
		{"新文件不存在", dir + "/new.txt", fs.ErrNotExist},
		{"缺失父目录", dir + "/missing/new.txt", fs.ErrNotExist},
		{"普通文件", plain, nil},
		{"目录不是文件", dir, terminalfiles.ErrNotRegular},
		{"符号链接不是普通文件", link, terminalfiles.ErrNotRegular},
		{"悬空链接不是新文件", dangling, terminalfiles.ErrNotRegular},
		{"父目录无搜索权限", denied + "/hidden.txt", fs.ErrPermission},
		{"普通文件无读权限", unreadable, fs.ErrPermission},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.want == fs.ErrPermission && os.Geteuid() == 0 {
				t.Skip("root可穿透此权限夹具；协议退出码与HTTP映射另有Mock覆盖")
			}
			err := exec.Command("/bin/sh", "-c", checks+"\nregular \"$1\"", "edo-files", test.path).Run()
			if err == nil {
				if test.want != nil {
					t.Fatal("路径错误被当作成功")
				}
				return
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal("路径检查命令未正常退出")
			}
			if !errors.Is(fileExecError(fileExecExitError(exit.ExitCode())), test.want) {
				t.Fatalf("路径错误类型不符，exit=%d want=%v", exit.ExitCode(), test.want)
			}
		})
	}
}

// 显式运行时只创建无网络、无宿主机挂载、有资源限额的临时容器，不改任何业务容器。
func TestContainerFilesLinuxToolProtocol(t *testing.T) {
	if os.Getenv("EDO_TEST_TERMINAL_TOOLS") != "1" {
		t.Skip("使用 EDO_TEST_TERMINAL_TOOLS=1 验证已缓存镜像，不自动拉取")
	}
	for _, image := range []string{"node:24-bookworm-slim", "alpine:3.22"} {
		t.Run(image, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			harness := `set -eu
mkdir /tmp/protocol
name=$(printf 'line\nname ;$(touch injected)')
path=/tmp/protocol/$name
printf old >"$path"
chmod 640 "$path"
ln -s "$path" /tmp/protocol/link
mkfifo /tmp/protocol/fifo
/bin/sh -c "$2" edo-files /tmp/protocol > /tmp/list
test -s /tmp/list
/bin/sh -c "$3" edo-files "$path" > /tmp/open
test -s /tmp/open
set +e
/bin/sh -c "$3" edo-files /tmp/protocol/link >/dev/null
result=$?
set -e
[ "$result" -eq 66 ]
set +e
/bin/sh -c "$3" edo-files /tmp/protocol/fifo >/dev/null
result=$?
set -e
[ "$result" -eq 66 ]
printf 'newEDO-COMMIT\n' | /bin/sh -c "$4" edo-files "$path" "$5" 3 >/tmp/put
[ "$(cat "$path")" = new ]
[ "$(stat -c %a "$path")" = 640 ]
set +e
printf again | /bin/sh -c "$4" edo-files "$path" "$5" 5 >/dev/null
result=$?
set -e
[ "$result" -eq 68 ]
set +e
/bin/sh -c "$3" edo-files '/tmp/protocol/new file' >/dev/null
result=$?
set -e
[ "$result" -eq 71 ]
printf 'createEDO-COMMIT\n' | /bin/sh -c "$4" edo-files '/tmp/protocol/new file' '' 6 >/tmp/create
[ "$(stat -c %a '/tmp/protocol/new file')" = 600 ]
/bin/sh -c "$3" edo-files '/tmp/protocol/new file' >/tmp/open-created
test -s /tmp/open-created
set +e
printf duplicate | /bin/sh -c "$4" edo-files '/tmp/protocol/new file' '' 9 >/dev/null
result=$?
set -e
[ "$result" -eq 69 ]
set +e
printf bad-size | /bin/sh -c "$4" edo-files /tmp/protocol/fail '' 1 >/dev/null
result=$?
set -e
[ "$result" -eq 70 ]
[ ! -e /tmp/protocol/fail ]
[ "$(find /tmp/protocol -maxdepth 1 -name '.edo-upload.*' | wc -l)" -eq 0 ]
# 收齐文件之后直接 EOF 仍是放弃，不得把断连误认为提交。
printf old >"$path"
set +e
printf new | /bin/sh -c "$4" edo-files "$path" "$5" 3 >/tmp/abandoned
result=$?
set -e
[ "$result" -eq 70 ]
[ "$(cat "$path")" = old ]
[ "$(find /tmp/protocol -maxdepth 1 -name '.edo-upload.*' | wc -l)" -eq 0 ]
# 错误控制帧同样不能提交，空文件也必须单独提供许可。
set +e
printf 'newDENY\n' | /bin/sh -c "$4" edo-files "$path" "$5" 3 >/tmp/denied
result=$?
set -e
[ "$result" -eq 70 ]
[ "$(cat "$path")" = old ]
printf 'EDO-COMMIT\n' | /bin/sh -c "$4" edo-files /tmp/protocol/empty '' 0 >/tmp/empty
[ "$(stat -c %s /tmp/protocol/empty)" -eq 0 ]
printf 'timedEDO-COMMIT\n' | /bin/sh -c "$6" edo-files "$4" /tmp/protocol/timed '' 5 >/tmp/timed
[ "$(cat /tmp/protocol/timed)" = timed ]
# 验证完整块及尾部字节不会吞掉随后发送的控制帧。
dd if=/dev/zero of=/tmp/large bs=70000 count=1 2>/dev/null
(cat /tmp/large; printf 'EDO-COMMIT\n') | /bin/sh -c "$4" edo-files /tmp/protocol/large '' 70000 >/tmp/large-result
[ "$(stat -c %s /tmp/protocol/large)" -eq 70000 ]
# 在传输已开始后改变旧文件，第二次摘要核验必须阻止替换。
printf old >"$path"
mkfifo /tmp/input
/bin/sh -c "$4" edo-files "$path" "$5" 3 </tmp/input >/tmp/conflict &
operation=$!
exec 5>/tmp/input
printf n >&5
attempts=0
while [ "$(find /tmp/protocol -maxdepth 1 -name '.edo-upload.*' | wc -l)" -eq 0 ]; do
 attempts=$((attempts+1))
 [ "$attempts" -lt 100 ] || exit 92
 sleep .01
done
printf changed >"$path"
printf ew >&5
exec 5>&-
set +e
wait "$operation"
result=$?
set -e
[ "$result" -eq 68 ]
[ "$(cat "$path")" = changed ]
[ "$(find /tmp/protocol -maxdepth 1 -name '.edo-upload.*' | wc -l)" -eq 0 ]
# READY 之后、授权许可到来前的外部修改仍应被最终摘要校验拒绝。
printf old >"$path"
mkfifo /tmp/commit-input
/bin/sh -c "$4" edo-files "$path" "$5" 3 </tmp/commit-input >/tmp/commit-ready &
operation=$!
exec 5>/tmp/commit-input
printf new >&5
attempts=0
while ! grep -q EDO-FILE-READY /tmp/commit-ready; do
 attempts=$((attempts+1))
 [ "$attempts" -lt 100 ] || exit 93
 sleep .01
done
printf externally-changed >"$path"
printf 'EDO-COMMIT\n' >&5
exec 5>&-
set +e
wait "$operation"
result=$?
set -e
[ "$result" -eq 68 ]
[ "$(cat "$path")" = externally-changed ]
[ "$(find /tmp/protocol -maxdepth 1 -name '.edo-upload.*' | wc -l)" -eq 0 ]
# 不足以安全暂存的非 sticky 公共可写目录拒绝操作。
mkdir /tmp/unsafe
chmod 777 /tmp/unsafe
set +e
printf x | /bin/sh -c "$4" edo-files /tmp/unsafe/a '' 1 >/dev/null
result=$?
set -e
[ "$result" -eq 73 ]
[ ! -e /tmp/unsafe/a ]
[ "$(find /tmp/unsafe -maxdepth 1 -name '.edo-upload.*' | wc -l)" -eq 0 ]
# 私有目录和带 sticky 位的共享目录仍可安全提交。
mkdir -m 700 /tmp/owned
printf 'pEDO-COMMIT\n' | /bin/sh -c "$4" edo-files /tmp/owned/a '' 1 >/dev/null
[ "$(cat /tmp/owned/a)" = p ]
mkdir -m 1777 /tmp/sticky
printf 'sEDO-COMMIT\n' | /bin/sh -c "$4" edo-files /tmp/sticky/a '' 1 >/dev/null
[ "$(cat /tmp/sticky/a)" = s ]
[ ! -e /tmp/protocol/injected ]
`
			digest := sha256.Sum256([]byte("old"))
			args := []string{"run", "--rm", "--pull=never", "--network=none", "--read-only", "--user=10001:10001", "--label=io.edo.acceptance.case=terminal-tools", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=96m", "--cpus=.5", "--tmpfs=/tmp:rw,noexec,nosuid,size=80m", "--entrypoint=/bin/sh", image, "-c", harness, "edo-files-test", image, fileListScript, fileOpenScript, filePutScript, hex.EncodeToString(digest[:]), filePutTimeoutScript}
			out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("隔离镜像协议验证失败: %v %s", err, out)
			}
			// 同时通过真实双向流运行 Go 侧握手，不只拼接静态许可测试脚本。
			backend := testFileBackend(t, func(ctx context.Context, command []string, input io.Reader, output io.Writer) error {
				args := []string{"run", "--rm", "--pull=never", "-i", "--network=none", "--read-only", "--user=10001:10001", "--label=io.edo.acceptance.case=terminal-tools", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=96m", "--cpus=.5", "--tmpfs=/tmp:rw,noexec,nosuid,size=80m", "--entrypoint=/bin/sh", image}
				args = append(args, command[1:]...)
				cmd := exec.CommandContext(ctx, "docker", args...)
				cmd.Stdin, cmd.Stdout, cmd.Stderr = input, output, io.Discard
				err := cmd.Run()
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					return fileExecError(fileExecExitError(exit.ExitCode()))
				}
				return err
			})
			if reader, _, err := backend.Open(ctx, "/tmp/not-created"); reader != nil || !errors.Is(err, fs.ErrNotExist) {
				if reader != nil {
					reader.Close()
				}
				t.Fatalf("真实Linux文件流未把缺失路径映射为不存在: %v", err)
			}
			var authorizations atomic.Int32
			authorized := terminalfiles.WithCommitAuthorization(ctx, func(context.Context) error { authorizations.Add(1); return nil })
			payload := strings.Repeat("界", 23000)
			entry, err := backend.Put(authorized, "/tmp/duplex file", strings.NewReader(payload), int64(len(payload)), "")
			if err != nil || entry.Size != int64(len(payload)) || authorizations.Load() != 1 {
				t.Fatalf("真实双向握手未正确提交: %+v auth=%d err=%v", entry, authorizations.Load(), err)
			}
			denied := terminalfiles.WithCommitAuthorization(ctx, func(context.Context) error { return fs.ErrPermission })
			if _, err := backend.Put(denied, "/tmp/denied file", strings.NewReader("new"), 3, ""); !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("真实双向流未保留提交授权拒绝: %v", err)
			}
		})
	}
}

func TestContainerFilesOpenRejectsTruncatedMetadata(t *testing.T) {
	f := testFileBackend(t, func(_ context.Context, _ []string, _ io.Reader, out io.Writer) error {
		_, err := io.Copy(out, bytes.NewBufferString("truncated"))
		return err
	})
	if _, _, err := f.Open(context.Background(), "/tmp/a"); err == nil {
		t.Fatal("残缺元信息未拒绝")
	}
}
