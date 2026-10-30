package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"

	"edo/internal/config"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/terminalfiles"
	"edo/internal/testutil"
)

type dockerFileExitOptions struct {
	code            int
	running         bool
	foreign         bool
	inspectFailure  bool
	malformed       bool
	waitForExit     <-chan struct{}
	allowDisconnect bool
}

type dockerFileExitFixture struct {
	backend      terminalfiles.Backend
	inputRead    chan struct{}
	peerClosed   chan struct{}
	inspectCalls atomic.Int32
}

// 使用正式 Moby 客户端及双向 HTTP 101 接缝，不连接 Docker，也不伪造 Backend.Put 的返回错误。
func newDockerFileExitFixture(t *testing.T, options dockerFileExitOptions) *dockerFileExitFixture {
	t.Helper()
	const payload = "synthetic-file"
	id := strings.Repeat("d", 64)
	const execID = "file-early-exit"
	f := &dockerFileExitFixture{inputRead: make(chan struct{}), peerClosed: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("API-Version", "1.55")
			_, _ = io.WriteString(w, "OK")
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/"+id+"/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "State": map[string]any{"Running": true}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/"+id+"/exec"):
			var input container.ExecCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error("读取文件执行配置失败", err)
				return
			}
			if input.Tty || input.Privileged || !input.AttachStdin || !input.AttachStdout || !input.AttachStderr || len(input.Cmd) != 8 || input.Cmd[2] != filePutTimeoutScript || input.Cmd[4] != filePutScript {
				t.Error("文件执行没有使用正式非特权流协议")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"Id":%q}`, execID)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec/"+execID+"/start"):
			var input container.ExecStartRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Tty || input.Detach {
				t.Error("文件连接未使用非 TTY 双向流")
				return
			}
			connection, buffer, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error("文件测试连接升级失败", err)
				return
			}
			defer connection.Close()
			defer close(f.peerClosed)
			_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
			_, _ = buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n")
			if err := buffer.Flush(); err != nil {
				t.Error("发送文件升级响应失败", err)
				return
			}
			if _, err := io.CopyN(io.Discard, buffer.Reader, int64(len(payload))); err != nil {
				t.Error("未收齐固定合成输入", err)
				return
			}
			close(f.inputRead)
			if options.waitForExit != nil {
				select {
				case <-options.waitForExit:
				case <-time.After(4 * time.Second):
					t.Error("文件测试未在预算内结束")
					return
				}
			}
			if options.malformed {
				// 非法复用头会造成真正的 StdCopy 读取错误，不能被退出 0 吞掉。
				_, _ = connection.Write([]byte{9, 0, 0, 0, 0, 0, 0, 1, 'x'})
			}
			// 不发送 READY，只关闭输出半连接：输入已收齐，上传协程仍在等待提交许可。
			if err := connection.(*net.TCPConn).CloseWrite(); err != nil {
				if !options.allowDisconnect {
					t.Error("关闭文件输出半连接失败", err)
				}
				return
			}
			trailing, _ := io.Copy(io.Discard, buffer.Reader)
			if trailing != 0 {
				t.Error("远端早退后仍发送了提交许可")
			}
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/exec/"+execID+"/json"):
			f.inspectCalls.Add(1)
			if options.inspectFailure {
				http.Error(w, "synthetic inspect failure", http.StatusServiceUnavailable)
				return
			}
			resultID := id
			if options.foreign {
				resultID = strings.Repeat("e", 64)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ID": execID, "ContainerID": resultID, "Running": options.running, "ExitCode": options.code})
		default:
			t.Errorf("文件测试出现未知请求：%s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	db := testutil.NewDatabase(t)
	manager, err := secret.New("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := model.DockerEndpoint{ID: uuid.NewString(), Name: "file-early-exit", Host: strings.Replace(server.URL, "http://", "tcp://", 1), IsActive: true, CreatedBy: uuid.NewString(), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(dockerengine.NewService(db, manager, config.Runtime{ConnectTimeout: time.Second, RequestTimeout: time.Second}), nil, time.Hour)
	f.backend, err = service.OpenDockerFiles(t.Context(), endpoint.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.backend.Close() })
	return f
}

func (f *dockerFileExitFixture) put(ctx context.Context) error {
	_, err := f.backend.Put(ctx, "/tmp/synthetic.txt", strings.NewReader("synthetic-file"), 14, strings.Repeat("a", 64))
	return err
}

func TestDockerFilesEarlyProtocolExitKeepsConflict(t *testing.T) {
	for _, test := range []struct {
		name string
		code int
		want error
	}{
		{"旧摘要", 68, terminalfiles.ErrConflict},
		{"已存在", 69, terminalfiles.ErrExists},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newDockerFileExitFixture(t, dockerFileExitOptions{code: test.code})
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if err := f.put(ctx); !errors.Is(err, test.want) {
				t.Errorf("远端早退被输入管道错误覆盖：want=%v，got=%v", test.want, err)
			}
			if f.inspectCalls.Load() != 1 {
				t.Error("输入管道关闭后没有读取准确 Exec 退出状态")
			}
			select {
			case <-f.peerClosed:
			case <-ctx.Done():
				t.Fatal("远端早退后上传仍等待提交许可")
			}
		})
	}
}

func TestDockerFilesOperationCancellationKeepsCause(t *testing.T) {
	for _, name := range []string{"取消", "截止时间"} {
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			f := newDockerFileExitFixture(t, dockerFileExitOptions{code: 68, waitForExit: release, allowDisconnect: true})
			var ctx context.Context
			var cancel context.CancelFunc
			want := context.Canceled
			if name == "截止时间" {
				ctx, cancel = context.WithTimeout(t.Context(), 500*time.Millisecond)
				want = context.DeadlineExceeded
			} else {
				ctx, cancel = context.WithCancel(t.Context())
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- f.put(ctx) }()
			select {
			case <-f.inputRead:
			case <-time.After(2 * time.Second):
				close(release)
				t.Fatal("取消前未进入真实上传流")
			}
			if name == "取消" {
				cancel()
			} else {
				<-ctx.Done()
			}
			close(release)
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("真实操作终止原因被远端退出覆盖：want=%v，got=%v", want, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("取消后上传仍等待提交许可")
			}
			if f.inspectCalls.Load() != 0 {
				t.Error("操作已取消仍继续请求 Exec 结果")
			}
			select {
			case <-f.peerClosed:
			case <-time.After(2 * time.Second):
				t.Fatal("操作取消后双向连接没有释放")
			}
		})
	}
}

func TestDockerFilesUnconfirmedExitCannotBecomeSuccess(t *testing.T) {
	for _, test := range []struct {
		name    string
		options dockerFileExitOptions
		want    error
	}{
		{"退出零但许可流中断", dockerFileExitOptions{}, io.ErrClosedPipe},
		{"退出零但复用流损坏", dockerFileExitOptions{malformed: true}, nil},
		{"仍在运行", dockerFileExitOptions{running: true, code: 68}, terminalfiles.ErrUnavailable},
		{"目标身份不符", dockerFileExitOptions{foreign: true, code: 68}, terminalfiles.ErrUnavailable},
		{"结果读取失败", dockerFileExitOptions{inspectFailure: true}, nil},
		{"未知退出状态", dockerFileExitOptions{code: 73}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newDockerFileExitFixture(t, test.options)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			err := f.put(ctx)
			if err == nil {
				t.Fatal("未确认或损坏的文件操作被当成成功")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("未确认目标被错误解释为文件冲突：want=%v，got=%v", test.want, err)
			}
			if errors.Is(err, terminalfiles.ErrConflict) || errors.Is(err, terminalfiles.ErrExists) {
				t.Fatal("未确认执行结果被误报成可重试文件冲突")
			}
			if f.inspectCalls.Load() != 1 {
				t.Error("没有读取真实 Exec 结果来区分执行状态")
			}
		})
	}
}
