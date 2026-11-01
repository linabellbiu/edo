package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"edo/internal/terminalfiles"
)

type deadlineObservedBody struct {
	io.ReadCloser
	ctx                    context.Context
	started                chan struct{}
	once                   sync.Once
	timedOut               atomic.Bool
	contextCanceledAtError atomic.Bool
}

func (b *deadlineObservedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.once.Do(func() { close(b.started) })
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		b.timedOut.Store(true)
		b.contextCanceledAtError.Store(b.ctx.Err() != nil)
	}
	return n, err
}

// 使用真实http.Server全局deadline及真实TCP；授权始终有效，不通过假cancel制造失败。
func TestTerminalFileValidUploadOutlivesGlobalReadTimeout(t *testing.T) {
	const globalRead = 200 * time.Millisecond
	backend := &fakeFileBackend{value: []byte("old public fixture")}
	bodyReady := make(chan *deadlineObservedBody, 1)
	handlerDone := make(chan struct{})
	h := terminalFileHandler{open: func(context.Context, *gin.Context) (terminalfiles.Backend, error) { return backend, nil }, key: func(*gin.Context) string { return "host:deadline-fixture" }, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), checks: func(*gin.Context, ...string) func() bool { return func() bool { return true } }}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		body := &deadlineObservedBody{ReadCloser: c.Request.Body, ctx: c.Request.Context(), started: make(chan struct{})}
		c.Request.Body = body
		bodyReady <- body
		c.Next()
		close(handlerDone)
	})
	router.POST("/files/upload", h.upload)
	server := httptest.NewUnstartedServer(router)
	server.Config.ReadHeaderTimeout = globalRead
	server.Config.ReadTimeout = globalRead
	server.Config.WriteTimeout = 3 * time.Second
	server.Start()
	t.Cleanup(server.Close)
	connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err = connection.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	const content = "new public file bytes"
	if _, err = fmt.Fprintf(connection, "POST /files/upload?path=/demo.txt HTTP/1.1\r\nHost: fixture.invalid\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(content), content[:4]); err != nil {
		t.Fatal(err)
	}
	var body *deadlineObservedBody
	select {
	case body = <-bodyReady:
	case <-time.After(time.Second):
		t.Fatal("未进入真实上传处理器")
	}
	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("处理器尚未真正读取前缀")
	}
	// 明确跨过全局ReadTimeout，但远低于文件操作自身2分钟预算。
	timer := time.NewTimer(globalRead + 150*time.Millisecond)
	<-timer.C
	_, writeErr := io.WriteString(connection, content[4:])
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("有效持续上传未返回响应: read=%v write=%v", err, writeErr)
	}
	raw, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("上传处理器未结束")
	}
	var result struct {
		Code string `json:"code"`
		File struct {
			Size int64 `json:"size"`
		} `json:"file"`
	}
	decodeErr := json.Unmarshal(raw, &result)
	if response.StatusCode != http.StatusOK || decodeErr != nil || readErr != nil || closeErr != nil || writeErr != nil || body.timedOut.Load() || result.File.Size != int64(len(content)) || backend.puts != 1 || !bytes.Equal(backend.value, []byte(content)) || backend.closed != 1 {
		t.Fatalf("授权始终有效的上传被全局读截止截断: status=%d code=%s read_timeout=%v base_context_canceled=%v puts=%d read=%v write=%v", response.StatusCode, result.Code, body.timedOut.Load(), body.contextCanceledAtError.Load(), backend.puts, readErr, writeErr)
	}
}

type deadlineDownloadBackend struct {
	*fakeFileBackend
	gate    <-chan struct{}
	waiting chan struct{}
	once    sync.Once
}

func (b *deadlineDownloadBackend) Open(ctx context.Context, path string) (io.ReadCloser, terminalfiles.Entry, error) {
	return &deadlineDownloadReader{ctx: ctx, owner: b}, terminalfiles.Entry{Name: "demo.txt", Path: path, Kind: "file", Size: int64(len(b.value))}, nil
}

type deadlineDownloadReader struct {
	ctx    context.Context
	owner  *deadlineDownloadBackend
	offset int
}

func (r *deadlineDownloadReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.owner.value) {
		return 0, io.EOF
	}
	if r.offset >= 16*1024 {
		r.owner.once.Do(func() { close(r.owner.waiting) })
		select {
		case <-r.owner.gate:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
	}
	end := len(r.owner.value)
	if r.offset < 16*1024 {
		end = 16 * 1024
	}
	n := copy(p, r.owner.value[r.offset:end])
	r.offset += n
	return n, nil
}
func (*deadlineDownloadReader) Close() error { return nil }

func TestTerminalFileValidDownloadOutlivesGlobalWriteTimeout(t *testing.T) {
	const globalWrite = 200 * time.Millisecond
	gate := make(chan struct{})
	var release sync.Once
	backend := &deadlineDownloadBackend{fakeFileBackend: &fakeFileBackend{value: bytes.Repeat([]byte{0x43}, 64*1024)}, gate: gate, waiting: make(chan struct{})}
	handlerDone := make(chan struct{})
	h := terminalFileHandler{open: func(context.Context, *gin.Context) (terminalfiles.Backend, error) { return backend, nil }, key: func(*gin.Context) string { return "host:deadline-fixture" }, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), checks: func(*gin.Context, ...string) func() bool { return func() bool { return true } }}
	router := gin.New()
	router.GET("/files/download", func(c *gin.Context) { defer close(handlerDone); h.download(c) })
	server := httptest.NewUnstartedServer(router)
	server.Config.ReadHeaderTimeout = 5 * time.Second
	server.Config.ReadTimeout = 5 * time.Second
	server.Config.WriteTimeout = globalWrite
	server.Start()
	t.Cleanup(func() { release.Do(func() { close(gate) }); server.Close() })
	connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err = connection.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(connection, "GET /files/download?path=/demo.txt HTTP/1.1\r\nHost: fixture.invalid\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	prefix := make([]byte, 16*1024)
	if _, err = io.ReadFull(response.Body, prefix); err != nil {
		t.Fatalf("没有真实下载前缀: %v", err)
	}
	select {
	case <-backend.waiting:
	case <-time.After(time.Second):
		t.Fatal("下载尚未进入受控后续读取")
	}
	timer := time.NewTimer(globalWrite + 150*time.Millisecond)
	<-timer.C
	release.Do(func() { close(gate) })
	rest, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("下载处理器未结束")
	}
	actual := append(prefix, rest...)
	if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil || !bytes.Equal(actual, backend.value) || backend.closed != 1 {
		t.Fatalf("有效下载被全局写截止截断: status=%d received=%d want=%d read=%v close=%v", response.StatusCode, len(actual), len(backend.value), readErr, closeErr)
	}
	if strings.Contains(string(actual), "file_permission_denied") {
		t.Fatal("下载混入授权错误")
	}
}

type fileNoDeadlineWriter struct{ writer http.ResponseWriter }

func (w fileNoDeadlineWriter) Header() http.Header         { return w.writer.Header() }
func (w fileNoDeadlineWriter) Write(b []byte) (int, error) { return w.writer.Write(b) }
func (w fileNoDeadlineWriter) WriteHeader(status int)      { w.writer.WriteHeader(status) }

func TestTerminalFileUnsupportedControllerFailsBeforeOpen(t *testing.T) {
	var opened atomic.Int32
	h := terminalFileHandler{open: func(context.Context, *gin.Context) (terminalfiles.Backend, error) {
		opened.Add(1)
		return &fakeFileBackend{}, nil
	}, key: func(*gin.Context) string { return "host:unsupported-fixture" }, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	router := gin.New()
	router.POST("/files/upload", h.upload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(fileNoDeadlineWriter{w}, r) }))
	defer server.Close()
	response, err := server.Client().Post(server.URL+"/files/upload?path=/demo.txt", "application/octet-stream", strings.NewReader("public"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusServiceUnavailable || !bytes.Contains(raw, []byte(`"code":"file_unavailable"`)) || opened.Load() != 0 {
		t.Fatalf("不支持controller时仍进入后端: status=%d opened=%d read=%v", response.StatusCode, opened.Load(), err)
	}
}

func TestTerminalFileParentDeadlineReturns504WithinGrace(t *testing.T) {
	backend := &fakeFileBackend{value: []byte("old public fixture")}
	started := make(chan struct{})
	done := make(chan struct{})
	h := terminalFileHandler{open: func(context.Context, *gin.Context) (terminalfiles.Backend, error) { return backend, nil }, key: func(*gin.Context) string { return "host:bounded-fixture" }, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), checks: func(*gin.Context, ...string) func() bool { return func() bool { return true } }}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 150*time.Millisecond)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Request.Body = &observedHTTPUploadBody{ReadCloser: c.Request.Body, readStarted: started, closed: make(chan struct{})}
		c.Next()
		close(done)
	})
	router.POST("/files/upload", h.upload)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/identity" {
			if r.Context().Err() != nil {
				w.WriteHeader(500)
				return
			}
			_, _ = io.WriteString(w, "healthy")
			return
		}
		router.ServeHTTP(w, r)
	}))
	server.Config.ReadHeaderTimeout = time.Second
	server.Config.ReadTimeout = time.Second
	server.Config.WriteTimeout = time.Second
	server.Start()
	defer server.Close()
	connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = io.WriteString(connection, "POST /files/upload?path=/demo.txt HTTP/1.1\r\nHost: fixture.invalid\r\nContent-Length: 512\r\n\r\nCCCC"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("短期限请求没有真正读body")
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	raw, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("短期限后处理器未收口")
	}
	if response.StatusCode != http.StatusGatewayTimeout || !bytes.Contains(raw, []byte(`"code":"file_timeout"`)) || readErr != nil || closeErr != nil || !response.Close || backend.puts != 0 || string(backend.value) != "old public fixture" || backend.closed != 1 {
		t.Fatalf("短期限未完整504或改变旧内容: status=%d close=%v puts=%d read=%v", response.StatusCode, response.Close, backend.puts, readErr)
	}
	fresh, err := server.Client().Get(server.URL + "/identity")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := io.ReadAll(fresh.Body)
	_ = fresh.Body.Close()
	if err != nil || fresh.StatusCode != 200 || string(identity) != "healthy" {
		t.Fatal("超时后后续真实请求上下文失效")
	}
}

type fileDeadlineCaptureWriter struct {
	http.ResponseWriter
	reads                   int
	readDeadline, timeWrite time.Time
}

func (w *fileDeadlineCaptureWriter) SetReadDeadline(d time.Time) error {
	w.reads++
	w.readDeadline = d
	return nil
}
func (w *fileDeadlineCaptureWriter) SetWriteDeadline(d time.Time) error { w.timeWrite = d; return nil }

func TestTerminalFileDeadlineAlignmentBoundedAndReadScope(t *testing.T) {
	for _, readBody := range []bool{false, true} {
		writer := &fileDeadlineCaptureWriter{ResponseWriter: httptest.NewRecorder()}
		c, _ := gin.CreateTestContext(writer)
		c.Request = httptest.NewRequest(http.MethodPost, "/files/upload", nil)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		deadline, _ := ctx.Deadline()
		h := terminalFileHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		err := h.alignDeadlines(c, ctx, readBody)
		cancel()
		if err != nil || !writer.timeWrite.Equal(deadline.Add(time.Second)) || writer.timeWrite.IsZero() || writer.reads != map[bool]int{false: 0, true: 1}[readBody] || readBody && !writer.readDeadline.Equal(deadline) {
			t.Fatalf("文件期限不是操作边界+固定1秒结算或读范围扩大: readBody=%v reads=%d err=%v", readBody, writer.reads, err)
		}
	}
}

func TestTerminalFileFailureClassificationUsesKnownCause(t *testing.T) {
	timeout := &net.DNSError{Err: "public timeout fixture", IsTimeout: true}
	permission, cancelPermission := context.WithCancelCause(context.Background())
	cancelPermission(fs.ErrPermission)
	client, cancelClient := context.WithCancel(context.Background())
	cancelClient()
	explicitCancel, cancelExplicit := context.WithCancelCause(context.Background())
	cancelExplicit(errTerminalFileCanceled)
	deadline, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	for _, tc := range []struct {
		name      string
		ctx       context.Context
		err, want error
	}{
		{"撤权制造的read timeout仍403", permission, errors.Join(timeout, context.Canceled), fs.ErrPermission},
		{"PEONY读timeout加generic取消不是权限", client, errors.Join(timeout, context.Canceled), context.DeadlineExceeded},
		{"明确取消cause", explicitCancel, errors.Join(timeout, context.Canceled), errTerminalFileCanceled},
		{"操作期限", deadline, timeout, context.DeadlineExceeded},
		{"独立transport超时", context.Background(), timeout, context.DeadlineExceeded},
		{"纯取消", context.Background(), context.Canceled, errTerminalFileCanceled},
		{"后端不可用不因清理cancel误分类", client, terminalfiles.ErrUnavailable, terminalfiles.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminalFileFailure(tc.ctx, tc.err); !errors.Is(got, tc.want) {
				t.Fatalf("错误分类不符: got=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestTerminalFileRealClientAbortDoesNotBecomePermission(t *testing.T) {
	backend := &fakeFileBackend{value: []byte("original public fixture")}
	started := make(chan struct{})
	finished := make(chan int, 1)
	h := terminalFileHandler{open: func(context.Context, *gin.Context) (terminalfiles.Backend, error) { return backend, nil }, key: func(*gin.Context) string { return "host:client-abort-fixture" }, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), checks: func(*gin.Context, ...string) func() bool { return func() bool { return true } }}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request.Body = &observedHTTPUploadBody{ReadCloser: c.Request.Body, readStarted: started, closed: make(chan struct{})}
		c.Next()
		finished <- c.Writer.Status()
	})
	router.POST("/files/upload", h.upload)
	server := httptest.NewServer(router)
	defer server.Close()
	connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err = io.WriteString(connection, "POST /files/upload?path=/demo.txt HTTP/1.1\r\nHost: fixture.invalid\r\nContent-Length: 1048576\r\n\r\nCCCC"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("客户端断连前服务端未真正读取body")
	}
	// 这是本客户端真正关闭TCP，不调用服务端cancel函数；不要求向断开的客户端交付响应。
	if err = connection.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-finished:
		if status != http.StatusRequestTimeout && status != http.StatusGatewayTimeout {
			t.Fatalf("真实断连误报权限或未分类: status=%d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("真实客户端断连后处理器未收口")
	}
	if backend.puts != 0 || backend.closed != 1 || string(backend.value) != "original public fixture" {
		t.Fatal("真实断连写入文件或未释放后端")
	}
}
