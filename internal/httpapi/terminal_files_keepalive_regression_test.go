package httpapi

import (
	"bufio"
	"context"
	"fmt"
	"io"
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

// 仅后端文件流为内存夹具；HTTP、TCP、keep-alive 与请求 context 均使用真实实现。
type keepaliveFileBackend struct {
	cancelled bool
	blocked   chan struct{}
	opened    chan context.Context
	closed    atomic.Int32
}

func (b *keepaliveFileBackend) List(context.Context, string) (terminalfiles.Directory, error) {
	return terminalfiles.Directory{}, nil
}
func (b *keepaliveFileBackend) Open(ctx context.Context, path string) (io.ReadCloser, terminalfiles.Entry, error) {
	b.opened <- ctx
	if !b.cancelled {
		return io.NopCloser(strings.NewReader("public complete fixture")), terminalfiles.Entry{Name: "fixture.bin", Path: path, Kind: "file", Size: 23}, nil
	}
	return &keepaliveBlockedReader{ctx: ctx, blocked: b.blocked}, terminalfiles.Entry{Name: "fixture.bin", Path: path, Kind: "file", Size: 1024 * 1024}, nil
}
func (b *keepaliveFileBackend) Put(context.Context, string, io.Reader, int64, string) (terminalfiles.Entry, error) {
	return terminalfiles.Entry{}, fmt.Errorf("本下载夹具不允许上传提交")
}
func (b *keepaliveFileBackend) Close() error { b.closed.Add(1); return nil }

type keepaliveBlockedReader struct {
	ctx     context.Context
	blocked chan struct{}
	sent    bool
	once    sync.Once
}

func (r *keepaliveBlockedReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		n := min(len(p), 32*1024)
		for i := range p[:n] {
			p[i] = 0x43
		}
		return n, nil
	}
	r.once.Do(func() { close(r.blocked) })
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}
func (*keepaliveBlockedReader) Close() error { return nil }

func keepaliveIdentity(w http.ResponseWriter, r *http.Request) {
	if r.Context().Err() != nil {
		http.Error(w, "auth_unavailable", http.StatusInternalServerError)
		return
	}
	cookie, err := r.Cookie("public_fixture")
	if err != nil || cookie.Value != "present" {
		http.Error(w, "authentication_required", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"context_ok":true,"authenticated":true}`)
}

func keepaliveTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	// 与生产同时配置两个读取超时，确保下一请求能重设头部 deadline 而暴露基础 context 污染。
	server.Config.ReadHeaderTimeout = 3 * time.Second
	server.Config.ReadTimeout = 8 * time.Second
	server.Config.WriteTimeout = 8 * time.Second
	server.Start()
	t.Cleanup(server.Close)
	return server
}
func keepaliveDial(t *testing.T, server *httptest.Server) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(12 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn, bufio.NewReader(conn)
}
func keepaliveSecondIdentity(t *testing.T, conn net.Conn, reader *bufio.Reader) {
	t.Helper()
	if _, err := io.WriteString(conn, "GET /identity HTTP/1.1\r\nHost: fixture.invalid\r\nCookie: public_fixture=present\r\n\r\n"); err != nil {
		t.Fatalf("同一 TCP 写入第二请求失败: %v", err)
	}
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("同一 TCP 无法读取第二请求: %v", err)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != `{"context_ok":true,"authenticated":true}` {
		t.Fatalf("同一 TCP 第二请求 context/身份失败: status=%d body=%s read=%v", response.StatusCode, body, err)
	}
}

func TestTerminalFileKeepaliveRequestContext(t *testing.T) {
	for _, test := range []struct {
		name                    string
		cancelDownload, getBody bool
	}{
		{name: "normal_get"},
		{name: "revoked_partial_get", cancelDownload: true},
		{name: "revoked_partial_get_with_complete_body", cancelDownload: true, getBody: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cancelDownload, getBody := test.cancelDownload, test.getBody
			var allowed atomic.Bool
			allowed.Store(true)
			backend := &keepaliveFileBackend{cancelled: cancelDownload, blocked: make(chan struct{}), opened: make(chan context.Context, 1)}
			h := terminalFileHandler{
				open:   func(context.Context, *gin.Context) (terminalfiles.Backend, error) { return backend, nil },
				key:    func(*gin.Context) string { return "host:public-keepalive-fixture" },
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				checks: func(*gin.Context, ...string) func() bool { return allowed.Load },
			}
			router := gin.New()
			router.GET("/files/download", h.download)
			done := make(chan struct{})
			var noBody atomic.Bool
			server := keepaliveTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/identity" {
					keepaliveIdentity(w, r)
					return
				}
				noBody.Store(r.Body == http.NoBody)
				router.ServeHTTP(w, r)
				close(done)
			}))
			conn, reader := keepaliveDial(t, server)
			t.Cleanup(func() { allowed.Store(false) })
			request := "GET /files/download?path=/fixture.bin HTTP/1.1\r\nHost: fixture.invalid\r\n"
			if getBody {
				request += "Content-Length: 8\r\n"
			}
			if getBody {
				request += "\r\nBODYDATA"
			} else {
				request += "\r\n"
			}
			if _, err := io.WriteString(conn, request); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
			if err != nil {
				t.Fatal(err)
			}
			var bytes int64
			if cancelDownload {
				prefix := make([]byte, 32*1024)
				n, readErr := io.ReadFull(response.Body, prefix)
				if readErr != nil || n != len(prefix) {
					t.Fatalf("未进入真实部分下载: n=%d err=%v", n, readErr)
				}
				bytes += int64(n)
				select {
				case <-backend.blocked:
				case <-time.After(time.Second):
					t.Fatal("文件流未阻塞，不能测试进行中撤权")
				}
				if opened := <-backend.opened; opened.Err() != nil {
					t.Fatalf("撤权前文件请求已取消，前提无效: %v", opened.Err())
				}
				allowed.Store(false)
			}
			n, readErr := io.Copy(io.Discard, response.Body)
			bytes += n
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatalf("读取真实 HTTP 分块结束失败: %v", readErr)
			}
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("文件处理器未自然退出")
			}
			if response.StatusCode != http.StatusOK || response.Close || noBody.Load() != !getBody || backend.closed.Load() != 1 {
				t.Fatalf("GET/keepalive前提失败: status=%d close=%v noBody=%v backendClose=%d", response.StatusCode, response.Close, noBody.Load(), backend.closed.Load())
			}
			expected := int64(23)
			if cancelDownload {
				expected = 32 * 1024
			}
			if bytes != expected {
				t.Fatalf("下载实际字节错误: got=%d want=%d", bytes, expected)
			}
			// 不新建连接、不重登、不修改 Cookie；直接检查同一真实 TCP 下一请求的原始 context。
			keepaliveSecondIdentity(t, conn, reader)
		})
	}
}

func TestTerminalFileCancelledUploadConnectionClosed(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	backend := &fakeFileBackend{}
	bodyStarted := make(chan struct{})
	done := make(chan struct{})
	h := terminalFileHandler{
		open:   func(context.Context, *gin.Context) (terminalfiles.Backend, error) { return backend, nil },
		key:    func(*gin.Context) string { return "host:public-upload-fixture" },
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		checks: func(*gin.Context, ...string) func() bool { return allowed.Load },
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request.Body = &observedHTTPUploadBody{ReadCloser: c.Request.Body, readStarted: bodyStarted, closed: make(chan struct{})}
		c.Next()
	})
	router.POST("/files/upload", h.upload)
	server := keepaliveTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/identity" {
			keepaliveIdentity(w, r)
			return
		}
		router.ServeHTTP(w, r)
		close(done)
	}))
	conn, reader := keepaliveDial(t, server)
	t.Cleanup(func() { allowed.Store(false) })
	if _, err := io.WriteString(conn, "POST /files/upload?path=/fixture.bin HTTP/1.1\r\nHost: fixture.invalid\r\nContent-Type: application/octet-stream\r\nContent-Length: 512\r\n\r\nCCCCCCCCCCCCCCCC"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-bodyStarted:
	case <-time.After(time.Second):
		t.Fatal("上传未实际读取请求体")
	}
	allowed.Store(false)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusForbidden || !response.Close {
		t.Fatalf("取消实际上传必须拒绝复用连接: status=%d close=%v err=%v", response.StatusCode, response.Close, err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("上传处理器未自然退出")
	}
	if backend.puts != 0 || backend.closed != 1 {
		t.Fatalf("撤权上传产生提交或未回收: puts=%d close=%d", backend.puts, backend.closed)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		t.Fatalf("取消上传后旧TCP未由服务端关闭: %v", err)
	}
	// 新连接只作对照，不把它冒充旧连接复用成功。
	fresh, freshReader := keepaliveDial(t, server)
	keepaliveSecondIdentity(t, fresh, freshReader)
}
