package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/gin-gonic/gin"

	"edo/internal/access"
	"edo/internal/terminalfiles"
)

type fakeFileBackend struct {
	value               []byte
	expected            string
	puts, opens, closed int
	err                 error
	beforeCommit        func()
}

func (f *fakeFileBackend) List(context.Context, string) (terminalfiles.Directory, error) {
	return terminalfiles.Directory{Path: "/", Entries: []terminalfiles.Entry{{Name: "demo.txt", Path: "/demo.txt", Kind: "file", Size: int64(len(f.value))}}}, f.err
}
func (f *fakeFileBackend) Open(context.Context, string) (io.ReadCloser, terminalfiles.Entry, error) {
	f.opens++
	return io.NopCloser(bytes.NewReader(f.value)), terminalfiles.Entry{Name: "demo.txt", Path: "/demo.txt", Kind: "file", Size: int64(len(f.value))}, f.err
}
func (f *fakeFileBackend) Put(ctx context.Context, path string, reader io.Reader, size int64, expected string) (terminalfiles.Entry, error) {
	f.puts++
	f.expected = expected
	if f.err != nil {
		return terminalfiles.Entry{}, f.err
	}
	if f.beforeCommit != nil {
		f.beforeCommit()
	}
	if err := terminalfiles.AuthorizeCommit(ctx); err != nil {
		return terminalfiles.Entry{}, err
	}
	f.value, _ = io.ReadAll(reader)
	return terminalfiles.Entry{Name: "demo.txt", Path: path, Kind: "file", Size: size}, nil
}
func (f *fakeFileBackend) Close() error { f.closed++; return nil }

func fileTestRouter(f *fakeFileBackend, denied string) (*gin.Engine, *[]string) {
	checked := []string{}
	h := terminalFileHandler{open: func(context.Context, *gin.Context) (terminalfiles.Backend, error) { return f, nil }, key: func(*gin.Context) string { return "host:test" }, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), checks: func(_ *gin.Context, codes ...string) func() bool {
		return func() bool {
			for _, code := range codes {
				checked = append(checked, code)
				if code == denied {
					return false
				}
			}
			return true
		}
	}}
	r := gin.New()
	r.GET("/files", h.list)
	r.GET("/files/stat", h.stat)
	r.GET("/files/content", h.content)
	r.GET("/files/download", h.download)
	r.POST("/files/upload", h.upload)
	r.PUT("/files/content", h.edit)
	return r, &checked
}
func fileRequest(r http.Handler, method, path, body, etag string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if etag != "" {
		request.Header.Set("If-Match", etag)
	}
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	return response
}

func TestTerminalFilesHTTPReadsHashAndDownloadsWithoutChangingContent(t *testing.T) {
	f := &fakeFileBackend{value: []byte("文件内容\n")}
	router, _ := fileTestRouter(f, "")
	for _, path := range []string{"/files/content?path=/demo.txt", "/files/stat?path=/demo.txt"} {
		response := fileRequest(router, http.MethodGet, path, "", "")
		if response.Code != 200 {
			t.Fatalf("读取失败: %d %s", response.Code, response.Body)
		}
		var payload struct {
			File terminalFileResponse `json:"file"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(f.value)
		if payload.File.ETag != hex.EncodeToString(hash[:]) {
			t.Fatal("文件摘要与真实内容不同")
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("敏感文件响应不得被缓存")
		}
	}
	response := fileRequest(router, http.MethodGet, "/files/download?path=/demo.txt", "", "")
	if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), f.value) || response.Header().Get("Content-Disposition") == "" {
		t.Fatal("下载未保留文件字节或缺少附件响应头")
	}
	if f.puts != 0 || f.closed != 3 {
		t.Fatal("读取修改了文件或未关闭后端")
	}
}

func TestTerminalFilesHTTPUploadOverwriteRequiresAdditionalReadAndEdit(t *testing.T) {
	hash := sha256.Sum256([]byte("old"))
	etag := hex.EncodeToString(hash[:])
	for _, permission := range []string{access.PermissionTerminalFileRead, access.PermissionTerminalFileEdit} {
		f := &fakeFileBackend{}
		router, _ := fileTestRouter(f, permission)
		response := fileRequest(router, http.MethodPost, "/files/upload?path=/demo.txt", "new", etag)
		if response.Code != http.StatusForbidden || f.puts != 0 {
			t.Fatalf("无 %s 仍可覆盖文件", permission)
		}
	}
	f := &fakeFileBackend{}
	router, codes := fileTestRouter(f, "")
	response := fileRequest(router, http.MethodPost, "/files/upload?path=/demo.txt", "new", etag)
	if response.Code != 200 || string(f.value) != "new" || f.expected != etag {
		t.Fatalf("有权覆盖失败: %d %s", response.Code, response.Body)
	}
	for _, want := range []string{access.PermissionTerminalFileRead, access.PermissionTerminalFileUpload, access.PermissionTerminalFileEdit} {
		if !strings.Contains(strings.Join(*codes, ","), want) {
			t.Fatalf("缺少 %s 检查", want)
		}
	}
	response = fileRequest(router, http.MethodPost, "/files/upload?path=/new.txt", "create", "")
	if response.Code != 200 || f.expected != "" {
		t.Fatal("上传新文件不应伪造覆盖摘要")
	}
}

func TestTerminalFilesHTTPEditRequiresETagAndRejectsBinaryOrOversizedPreview(t *testing.T) {
	f := &fakeFileBackend{}
	router, _ := fileTestRouter(f, "")
	response := fileRequest(router, http.MethodPut, "/files/content", `{"path":"/demo.txt","content":"new"}`, "")
	if response.Code != 409 || f.puts != 0 {
		t.Fatal("缺少原内容摘要仍可编辑")
	}
	f.value = []byte{0, 1, 2}
	response = fileRequest(router, http.MethodGet, "/files/content?path=/demo.txt", "", "")
	if response.Code != 400 {
		t.Fatal("二进制文件被当成文本预览")
	}
	f.value = bytes.Repeat([]byte("x"), int(terminalfiles.MaxTextBytes)+1)
	response = fileRequest(router, http.MethodGet, "/files/content?path=/demo.txt", "", "")
	if response.Code != 413 {
		t.Fatal("超过文本上限仍被读入预览")
	}
}

func TestTerminalFilesHTTPRejectsPathInjectionAndDoesNotExposeDiagnosticErrors(t *testing.T) {
	f := &fakeFileBackend{}
	router, _ := fileTestRouter(f, "")
	for _, path := range []string{"/files?path=../etc", "/files?path=/foo/../etc", "/files?path=%00"} {
		if response := fileRequest(router, http.MethodGet, path, "", ""); response.Code != 400 {
			t.Fatalf("无效路径未拒绝: %s", path)
		}
	}
	f.err = errors.New("ssh-private-address and secret-file-content")
	response := fileRequest(router, http.MethodGet, "/files?path=/", "", "")
	if response.Code != 502 || strings.Contains(response.Body.String(), "ssh-private") || strings.Contains(response.Body.String(), "secret-file") {
		t.Fatal("客户端泄露了诊断错误")
	}
}

func TestTerminalFilesHTTPRevokedBeforeCommitDoesNotPersist(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			var allowed atomic.Bool
			allowed.Store(true)
			f := &fakeFileBackend{value: []byte("original"), beforeCommit: func() { allowed.Store(false) }}
			h := terminalFileHandler{
				open: func(context.Context, *gin.Context) (terminalfiles.Backend, error) { return f, nil },
				key:  func(*gin.Context) string { return "host:test" }, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				checks: func(*gin.Context, ...string) func() bool { return allowed.Load },
			}
			r := gin.New()
			r.POST("/files/upload", h.upload)
			r.PUT("/files/content", h.edit)
			path, body := "/files/upload?path=/demo.txt", "candidate"
			if method == http.MethodPut {
				path = "/files/content"
				body = `{"path":"/demo.txt","content":"candidate","etag":"` + strings.Repeat("a", 64) + `"}`
			}
			response := fileRequest(r, method, path, body, "")
			if response.Code != http.StatusForbidden || string(f.value) != "original" || f.closed != 1 {
				t.Fatalf("提交前撤权仍写入或未关闭: %d %q %d", response.Code, f.value, f.closed)
			}
		})
	}
}

type interruptedUploadBody struct {
	closed chan struct{}
	onRead func()
	once   sync.Once
}

func (b *interruptedUploadBody) Read([]byte) (int, error) {
	b.onRead()
	<-b.closed
	return 0, errors.New("private-upload-reader-closed")
}

func (b *interruptedUploadBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestTerminalFilesHTTPInterruptedUploadReportsCancellationCause(t *testing.T) {
	for _, test := range []struct {
		name   string
		revoke bool
		status int
		code   string
	}{
		{"上传中撤权", true, http.StatusForbidden, "file_permission_denied"},
		{"上传读取超时", false, http.StatusGatewayTimeout, "file_timeout"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var allowed atomic.Bool
			allowed.Store(true)
			backend := &fakeFileBackend{value: []byte("original")}
			h := terminalFileHandler{
				open: func(context.Context, *gin.Context) (terminalfiles.Backend, error) { return backend, nil },
				key:  func(*gin.Context) string { return "host:test" }, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				checks: func(*gin.Context, ...string) func() bool { return allowed.Load },
			}
			router := gin.New()
			router.POST("/files/upload", h.upload)
			budget := 5 * time.Second
			if !test.revoke {
				budget = 200 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			body := &interruptedUploadBody{closed: make(chan struct{}), onRead: func() {
				if test.revoke {
					allowed.Store(false)
				}
			}}
			defer body.Close()
			request := httptest.NewRequest(http.MethodPost, "/files/upload?path=/demo.txt", body).WithContext(ctx)
			response, done := httptest.NewRecorder(), make(chan struct{})
			go func() { defer close(done); router.ServeHTTP(response, request) }()
			select {
			case <-done:
			case <-time.After(6 * time.Second):
				cancel()
				t.Fatal("上传取消后未停止读取")
			}
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("取消原因被误报为普通读取失败: status=%d body=%s", response.Code, response.Body)
			}
			if backend.puts != 0 || string(backend.value) != "original" || backend.closed != 1 {
				t.Fatalf("中断上传仍写入或未释放后端: puts=%d closed=%d", backend.puts, backend.closed)
			}
			if strings.Contains(response.Body.String(), "private-upload-reader-closed") {
				t.Fatal("内部读取错误泄漏到客户端")
			}
		})
	}
}

func TestTerminalFilesHTTPUploadKeepsUnrelatedReadFailurePrivate(t *testing.T) {
	backend := &fakeFileBackend{value: []byte("original")}
	router, _ := fileTestRouter(backend, "")
	request := httptest.NewRequest(http.MethodPost, "/files/upload?path=/demo.txt", iotest.ErrReader(errors.New("private-upload-read-failure")))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "private-upload-read-failure") {
		t.Fatalf("未取消的读取故障被误判或泄露: %d %s", response.Code, response.Body)
	}
	if backend.puts != 0 || backend.closed != 1 || string(backend.value) != "original" {
		t.Fatal("读取失败后仍提交文件或未释放后端")
	}
}
