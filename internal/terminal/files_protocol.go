package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"edo/internal/terminalfiles"
)

// 路径始终是独立 argv；脚本是固定协议，不接受用户提供的命令。
// 目录、下载及私有暂存写入兼容 GNU/BusyBox，不向业务容器安装工具。
const fileScriptPrelude = `set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
LC_ALL=C
export PATH LC_ALL
unset ENV BASH_ENV CDPATH
for t in stat dd; do command -v "$t" >/dev/null 2>&1 || exit 64; done
[ "$(uname -s)" = Linux ] || exit 64
stat -c '%s' / >/dev/null 2>&1 || exit 64
emit() {
 name=$1
 metadata=$(stat -c '%f %s %Y %a' -- "$1") || exit 65
 set -- $metadata
 [ "$#" -eq 4 ] || exit 64
 kind=other
 case $((0x$1 & 0170000)) in 32768) kind=file;; 16384) kind=directory;; 40960) kind=symlink;; esac
 printf '%s\000%s\000%s\000%s\000%s\000' "$name" "$kind" "$2" "$3" "$4"
}
regular() { [ ! -L "$1" ] && [ -f "$1" ] || exit 66; }
p=$1
`

const fileListScript = fileScriptPrelude + `
[ -d "$p" ] && [ ! -L "$p" ] || exit 65
# 先固定实际目录，后续名称均通过 NUL 分隔，不受换行、引号或空格影响。
cd -P -- "$p" || exit 65
[ -r . ] && [ -x . ] || exit 65
count=0
for item in ./* ./.[!.]* ./..?*; do
 [ -e "$item" ] || [ -L "$item" ] || continue
 emit "$item"
 count=$((count+1))
 [ "$count" -lt 1001 ] || break
done
`

const fileOpenScript = fileScriptPrelude + `
regular "$p"
s=$(stat -c '%s' -- "$p") || exit 65
[ "$s" -le 67108864 ] || exit 67
emit "$p"
if dd if=/dev/null iflag=nofollow,nonblock count=0 2>/dev/null; then
 dd if="$p" iflag=nofollow,nonblock bs=65536 count=1025 2>/dev/null || exit 66
else
 # BusyBox 没有 nofollow；读取前后均校验普通文件，最多读取限制加一个块。
 # 这不是对容器内恶意同权限进程的隔离边界。
 regular "$p"
 dd if="$p" bs=65536 count=1025 2>/dev/null || exit 66
 regular "$p"
fi
`

const filePutScript = fileScriptPrelude + `
expected=$2
size=$3
for t in sha256sum mktemp chmod mv ln rm rmdir; do command -v "$t" >/dev/null 2>&1 || exit 64; done
dir=${p%/*}
base=${p##*/}
[ -n "$dir" ] || dir=/
[ -n "$base" ] && [ "$base" != . ] && [ "$base" != .. ] || exit 65
[ -d "$dir" ] && [ ! -L "$dir" ] || exit 65
cd -P -- "$dir" || exit 65
# 固定父目录描述符，进入私有暂存目录后不重新跟随外部可替换的父路径。
exec 4< . || exit 64
parent=/proc/$$/fd/4
[ -d "$parent" ] || exit 64
parentmode=$(stat -c '%a' -- .) || exit 65
parentmode=$((0$parentmode))
# 可被其他用户随意改名的非 sticky 目录不提供安全写入。
[ "$((parentmode & 0022))" -eq 0 ] || [ "$((parentmode & 01000))" -ne 0 ] || exit 64
target=$parent/$base
check() {
 if [ -z "$expected" ]; then
  [ ! -e "$target" ] && [ ! -L "$target" ] || exit 69
 else
  regular "$target"
  actual=$(sha256sum < "$target") || exit 65
  [ "${actual%% *}" = "$expected" ] || exit 68
 fi
}
check
mode=600
if [ -n "$expected" ]; then mode=$(stat -c '%a' -- "$target") || exit 65; fi
umask 077
stage=$(mktemp -d ./.edo-upload.XXXXXXXXXXXX) || exit 65
stage=${stage#./}
cd -P -- "$stage" || { rmdir -- "$parent/$stage"; exit 65; }
cleanup() { rm -f -- ./payload; rmdir -- "$parent/$stage" 2>/dev/null || :; }
trap cleanup EXIT HUP INT TERM
# payload 只在已经固定的 0700 私有目录中创建，从不重定向已有目标文件。
(set -C; : > ./payload) || exit 65
# 严格读取声明的字节数，后续控制帧不能被当作文件内容或 EOF 提交信号。
dd if=/dev/null iflag=fullblock count=0 2>/dev/null || exit 64
blocks=$((size/65536))
tail=$((size%65536))
if dd if=/dev/null of=/dev/null oflag=nofollow,nonblock count=0 2>/dev/null; then
 dd of=./payload oflag=nofollow,nonblock iflag=fullblock bs=65536 count="$blocks" 2>/dev/null || exit 65
else
 dd of=./payload iflag=fullblock bs=65536 count="$blocks" 2>/dev/null || exit 65
fi
if [ "$tail" -ne 0 ]; then dd of=./payload bs=1 seek="$((blocks*65536))" count="$tail" conv=notrunc 2>/dev/null || exit 65; fi
actual_size=$(stat -c '%s' -- ./payload) || exit 65
[ "$actual_size" -le 67108864 ] || exit 67
[ "$actual_size" = "$size" ] || exit 65
chmod -- "$mode" ./payload || exit 65
check
# 关闭连接或 stdin 只表示放弃，不能自动发布暂存文件。服务端复核当前
# 授权与资源身份后才发送提交帧；远端此时才进入不可逆的原子发布阶段。
printf 'EDO-FILE-READY\n'
IFS= read -r permit || exit 70
[ "$permit" = EDO-COMMIT ] || exit 70
# 等待授权期间也可能发生外部修改，许可不是跳过原文件版本校验。
check
if [ -z "$expected" ]; then
 # link 是原子的仅新建操作，不会覆盖在检查后出现的文件或符号链接。
 ln -T -- ./payload "$target" 2>/dev/null || exit 69
else
 mv -fT -- ./payload "$target" || exit 65
fi
emit "$target"
`

const fileCommitReady = "EDO-FILE-READY\n"
const fileCommitPermit = "EDO-COMMIT\n"

// Exec 断连不保证终止远端进程，超时由目标内标准 timeout 独立兜底。
const filePutTimeoutScript = `PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
command -v timeout >/dev/null 2>&1 || exit 64
exec timeout 120 /bin/sh -c "$1" edo-files "$2" "$3" "$4"
`

type fileExec func(context.Context, []string, io.Reader, io.Writer) error

type containerFiles struct {
	ctx      context.Context
	cancel   context.CancelFunc
	run      fileExec
	check    func(context.Context) error
	close    func() error
	once     sync.Once
	identity string
}

func (f *containerFiles) Close() error {
	var err error
	f.once.Do(func() {
		f.cancel()
		if f.close != nil {
			err = f.close()
		}
	})
	return err
}

func (f *containerFiles) operation(ctx context.Context) (context.Context, func(), error) {
	if err := f.ctx.Err(); err != nil {
		return nil, nil, err
	}
	op, cancel := context.WithTimeout(ctx, 2*time.Minute)
	stop := context.AfterFunc(f.ctx, cancel)
	done := func() { stop(); cancel() }
	if err := f.check(op); err != nil {
		done()
		return nil, nil, err
	}
	return op, done, nil
}

func fileCommand(script, p string, args ...string) []string {
	return append([]string{"/bin/sh", "-c", script, "edo-files", p}, args...)
}

func filePutCommand(p, expected string, size int64) []string {
	return fileCommand(filePutTimeoutScript, filePutScript, p, expected, strconv.FormatInt(size, 10))
}

func (f *containerFiles) execute(ctx context.Context, command []string, input io.Reader, out io.Writer) error {
	op, done, err := f.operation(ctx)
	if err != nil {
		return err
	}
	defer done()
	// 正在进行的文件传输也要响应目标停用、容器重建与集群权限租约失效。
	var validationErr error
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-op.Done():
				return
			case <-ticker.C:
				if err := f.check(op); err != nil {
					validationErr = err
					done()
					return
				}
			}
		}
	}()
	err = f.run(op, command, input, out)
	done()
	<-finished
	if validationErr != nil {
		return validationErr
	}
	return err
}

func (f *containerFiles) List(ctx context.Context, value string) (terminalfiles.Directory, error) {
	p, err := terminalfiles.CleanPath(value)
	if err != nil {
		return terminalfiles.Directory{}, err
	}
	out := &fileBoundedBuffer{limit: (4096 + 128) * (terminalfiles.MaxEntries + 1)}
	if err := f.execute(ctx, fileCommand(fileListScript, p), nil, out); err != nil {
		return terminalfiles.Directory{}, err
	}
	fields := bytes.Split(out.Bytes(), []byte{0})
	if len(fields) == 1 && len(fields[0]) == 0 {
		return terminalfiles.Directory{Path: p, Entries: []terminalfiles.Entry{}}, nil
	}
	if len(fields)%5 != 1 || len(fields[len(fields)-1]) != 0 {
		return terminalfiles.Directory{}, terminalfiles.ErrUnavailable
	}
	result := terminalfiles.Directory{Path: p, Entries: []terminalfiles.Entry{}}
	for n := 0; n < len(fields)-1; n += 5 {
		if len(result.Entries) == terminalfiles.MaxEntries {
			result.Truncated = true
			break
		}
		fields[n] = bytes.TrimPrefix(fields[n], []byte("./"))
		entry, err := parseFileEntry(fields[n:n+5], p, true)
		if err != nil {
			return terminalfiles.Directory{}, err
		}
		result.Entries = append(result.Entries, entry)
	}
	return result, nil
}

func (f *containerFiles) Open(ctx context.Context, value string) (io.ReadCloser, terminalfiles.Entry, error) {
	p, err := terminalfiles.CleanPath(value)
	if err != nil {
		return nil, terminalfiles.Entry{}, err
	}
	operationContext, cancel := context.WithCancel(ctx)
	r, w := io.Pipe()
	go func() {
		err := f.execute(operationContext, fileCommand(fileOpenScript, p), nil, w)
		_ = w.CloseWithError(err)
	}()
	fields := make([][]byte, 0, 5)
	for len(fields) < 5 {
		field := make([]byte, 0, 128)
		for {
			var b [1]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				cancel()
				r.Close()
				return nil, terminalfiles.Entry{}, err
			}
			if b[0] == 0 {
				break
			}
			field = append(field, b[0])
			if len(field) > 4096 {
				cancel()
				r.Close()
				return nil, terminalfiles.Entry{}, terminalfiles.ErrUnavailable
			}
		}
		fields = append(fields, field)
	}
	entry, err := parseFileEntry(fields, path.Dir(p), false)
	if err != nil || entry.Kind != "file" || entry.Size > terminalfiles.MaxTransferBytes {
		cancel()
		r.Close()
		if err == nil {
			if entry.Size > terminalfiles.MaxTransferBytes {
				err = terminalfiles.ErrTooLarge
			} else {
				err = terminalfiles.ErrNotRegular
			}
		}
		return nil, terminalfiles.Entry{}, err
	}
	entry.Path = p
	entry.Name = path.Base(p)
	return &fileStream{ReadCloser: r, cancel: cancel, remaining: terminalfiles.MaxTransferBytes}, entry, nil
}

func (f *containerFiles) Put(ctx context.Context, value string, input io.Reader, size int64, expected string) (terminalfiles.Entry, error) {
	p, err := terminalfiles.CleanPath(value)
	if err != nil || p == "/" || input == nil {
		return terminalfiles.Entry{}, terminalfiles.ErrInvalidPath
	}
	if size < 0 || size > terminalfiles.MaxTransferBytes {
		return terminalfiles.Entry{}, terminalfiles.ErrTooLarge
	}
	if expected != "" && !validFileDigest(expected) {
		return terminalfiles.Entry{}, terminalfiles.ErrConflict
	}
	// 排队也计入操作期限，并在 Backend 关闭时即时退出。
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	stop := context.AfterFunc(f.ctx, cancel)
	defer func() { stop(); cancel() }()
	release, err := lockFilePath(waitCtx, f.identity+":"+p)
	if err != nil {
		return terminalfiles.Entry{}, err
	}
	defer release()
	out := &fileBoundedBuffer{limit: 4608}
	transfer := newFileCommitTransfer(waitCtx, input, size)
	defer transfer.Close()
	writer := &fileCommitWriter{transfer: transfer, output: out, authorize: func() error {
		if err := waitCtx.Err(); err != nil {
			return err
		}
		if err := f.check(waitCtx); err != nil {
			return err
		}
		return terminalfiles.AuthorizeCommit(waitCtx)
	}}
	err = f.execute(waitCtx, filePutCommand(p, expected, size), transfer, writer)
	if failure := writer.failure.Load(); failure != nil {
		return terminalfiles.Entry{}, failure.err
	}
	if err != nil {
		return terminalfiles.Entry{}, err
	}
	if !writer.ready.Load() {
		return terminalfiles.Entry{}, terminalfiles.ErrUnavailable
	}
	fields := bytes.Split(out.Bytes(), []byte{0})
	if len(fields) != 6 || len(fields[5]) != 0 {
		return terminalfiles.Entry{}, terminalfiles.ErrUnavailable
	}
	entry, err := parseFileEntry(fields[:5], path.Dir(p), false)
	entry.Path = p
	entry.Name = path.Base(p)
	return entry, err
}

// 文件体与提交许可共用有序 stdin，但许可仅在收到远端 ready 后生成。
// 额外输入、短输入与取消会关闭控制管道，因此 EOF 永远不能变成许可。
type fileCommitTransfer struct {
	input     io.Reader
	remaining int64
	checked   bool
	permitted atomic.Bool
	reader    *io.PipeReader
	writer    *io.PipeWriter
	stop      func() bool
	once      sync.Once
	errMu     sync.Mutex
	err       error
}

func newFileCommitTransfer(ctx context.Context, input io.Reader, size int64) *fileCommitTransfer {
	r, w := io.Pipe()
	t := &fileCommitTransfer{input: input, remaining: size, reader: r, writer: w}
	t.stop = context.AfterFunc(ctx, func() { t.abort(ctx.Err()) })
	return t
}

func (t *fileCommitTransfer) abort(err error) {
	t.once.Do(func() {
		t.errMu.Lock()
		t.err = err
		t.errMu.Unlock()
		_ = t.reader.CloseWithError(err)
		_ = t.writer.CloseWithError(err)
	})
}

func (t *fileCommitTransfer) failure(fallback error) error {
	t.errMu.Lock()
	defer t.errMu.Unlock()
	if t.err != nil {
		return t.err
	}
	return fallback
}

func (t *fileCommitTransfer) Close() error {
	if t.stop != nil {
		t.stop()
	}
	t.abort(context.Canceled)
	return nil
}

func (t *fileCommitTransfer) closePendingInput() {
	if !t.permitted.Load() {
		_ = t.Close()
	}
}

func (t *fileCommitTransfer) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if t.remaining > 0 {
		if int64(len(p)) > t.remaining {
			p = p[:t.remaining]
		}
		n, err := t.input.Read(p)
		t.remaining -= int64(n)
		if errors.Is(err, io.EOF) && t.remaining == 0 {
			t.checked, err = true, nil
		} else if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			t.abort(err)
		}
		return n, err
	}
	if !t.checked {
		var probe [1]byte
		n, err := t.input.Read(probe[:])
		if n != 0 {
			t.abort(terminalfiles.ErrTooLarge)
			return 0, terminalfiles.ErrTooLarge
		}
		if !errors.Is(err, io.EOF) {
			if err != nil {
				t.abort(err)
			}
			return 0, err
		}
		t.checked = true
	}
	return t.reader.Read(p)
}

type fileCommitWriter struct {
	transfer  *fileCommitTransfer
	output    io.Writer
	authorize func() error
	prefix    []byte
	ready     atomic.Bool
	failure   atomic.Pointer[fileCommitFailure]
}

type fileCommitFailure struct{ err error }

func (w *fileCommitWriter) fail(err error) (int, error) {
	w.failure.CompareAndSwap(nil, &fileCommitFailure{err: err})
	w.transfer.abort(err)
	return 0, err
}

func (w *fileCommitWriter) Write(p []byte) (int, error) {
	total := len(p)
	if !w.ready.Load() {
		n := min(len(p), len(fileCommitReady)-len(w.prefix))
		w.prefix = append(w.prefix, p[:n]...)
		p = p[n:]
		if !bytes.Equal(w.prefix, []byte(fileCommitReady)[:len(w.prefix)]) {
			return w.fail(terminalfiles.ErrUnavailable)
		}
		if len(w.prefix) != len(fileCommitReady) {
			return total, nil
		}
		if err := w.authorize(); err != nil {
			return w.fail(err)
		}
		if _, err := io.WriteString(w.transfer.writer, fileCommitPermit); err != nil {
			return w.fail(w.transfer.failure(err))
		}
		_ = w.transfer.writer.Close()
		w.transfer.permitted.Store(true)
		w.ready.Store(true)
	}
	if len(p) > 0 {
		if _, err := w.output.Write(p); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// 限制 EDO 自身对同一路径的并发覆盖；外部进程在摘要校验与 rename 间修改的
// 极小竞态不具备内核 CAS 语义，不能将此协议视为隔离不可信容器内进程的沙箱。
var filePathLocks = struct {
	sync.Mutex
	entries map[string]*filePathLock
}{entries: map[string]*filePathLock{}}

type filePathLock struct {
	held       chan struct{}
	references int
}

func lockFilePath(ctx context.Context, key string) (func(), error) {
	filePathLocks.Lock()
	entry := filePathLocks.entries[key]
	if entry != nil && entry.references >= 32 {
		filePathLocks.Unlock()
		return nil, terminalfiles.ErrUnavailable
	}
	if entry == nil {
		if len(filePathLocks.entries) >= 256 {
			filePathLocks.Unlock()
			return nil, terminalfiles.ErrUnavailable
		}
		entry = &filePathLock{held: make(chan struct{}, 1)}
		filePathLocks.entries[key] = entry
	}
	entry.references++
	filePathLocks.Unlock()
	drop := func() {
		filePathLocks.Lock()
		entry.references--
		if entry.references == 0 {
			delete(filePathLocks.entries, key)
		}
		filePathLocks.Unlock()
	}
	select {
	case entry.held <- struct{}{}:
		return func() { <-entry.held; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

func validFileDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func parseFileEntry(fields [][]byte, parent string, listed bool) (terminalfiles.Entry, error) {
	if len(fields) != 5 {
		return terminalfiles.Entry{}, terminalfiles.ErrUnavailable
	}
	name := string(fields[0])
	if !utf8.ValidString(name) || listed && (name == "" || name == "." || name == ".." || strings.Contains(name, "/")) {
		return terminalfiles.Entry{}, terminalfiles.ErrUnavailable
	}
	kind := "other"
	switch string(fields[1]) {
	case "f", "file", "regular file", "regular empty file":
		kind = "file"
	case "d", "directory":
		kind = "directory"
	case "l", "symbolic link":
		kind = "symlink"
	}
	size, e1 := strconv.ParseInt(string(fields[2]), 10, 64)
	seconds := strings.SplitN(string(fields[3]), ".", 2)[0]
	stamp, e2 := strconv.ParseInt(seconds, 10, 64)
	mode, e3 := strconv.ParseUint(string(fields[4]), 8, 32)
	if e1 != nil || e2 != nil || e3 != nil || size < 0 || mode > 07777 {
		return terminalfiles.Entry{}, terminalfiles.ErrUnavailable
	}
	return terminalfiles.Entry{Name: name, Path: path.Join(parent, name), Kind: kind, Size: size, ModifiedAt: time.Unix(stamp, 0).UTC(), Mode: uint32(mode)}, nil
}

type fileBoundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *fileBoundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, terminalfiles.ErrTooLarge
	}
	return b.Buffer.Write(p)
}

type fileStream struct {
	io.ReadCloser
	cancel    context.CancelFunc
	remaining int64
}

func (s *fileStream) Read(p []byte) (int, error) {
	if s.remaining == 0 {
		var b [1]byte
		n, err := s.ReadCloser.Read(b[:])
		if n > 0 {
			return 0, terminalfiles.ErrTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > s.remaining {
		p = p[:s.remaining]
	}
	n, err := s.ReadCloser.Read(p)
	s.remaining -= int64(n)
	return n, err
}
func (s *fileStream) Close() error { s.cancel(); return s.ReadCloser.Close() }

type fileExecExitError int

func (e fileExecExitError) Error() string { return fmt.Sprintf("文件协议退出状态 %d", int(e)) }
func fileExecError(err error) error {
	if err == nil {
		return nil
	}
	var local fileExecExitError
	status := 0
	if errors.As(err, &local) {
		status = int(local)
	} else {
		var e interface{ ExitStatus() int }
		if errors.As(err, &e) {
			status = e.ExitStatus()
		}
	}
	switch status {
	case 64, 127:
		return terminalfiles.ErrUnavailable
	case 65:
		return terminalfiles.ErrInvalidPath
	case 66:
		return terminalfiles.ErrNotRegular
	case 67:
		return terminalfiles.ErrTooLarge
	case 68:
		return terminalfiles.ErrConflict
	case 69:
		return terminalfiles.ErrExists
	}
	return err
}
