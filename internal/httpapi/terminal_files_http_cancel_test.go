package httpapi

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

type observedHTTPUploadBody struct {
	io.ReadCloser
	readStarted chan struct{}
	closed      chan struct{}
	readOnce    sync.Once
	closeOnce   sync.Once
}

func (b *observedHTTPUploadBody) Read(data []byte) (int, error) {
	n, err := b.ReadCloser.Read(data)
	if n > 0 {
		b.readOnce.Do(func() { close(b.readStarted) })
	}
	return n, err
}

func (b *observedHTTPUploadBody) Close() error {
	// 保留 net/http 真正的 Close 行为，不用信号通道代替其锁与剩余请求体读取。
	err := b.ReadCloser.Close()
	b.closeOnce.Do(func() { close(b.closed) })
	return err
}

func TestTerminalFilesHTTPRealUploadCancellation(t *testing.T) {
	for _, framing := range []string{"content-length", "chunked"} {
		for _, cause := range []string{"revoke", "timeout", "cancel"} {
			t.Run(framing+"/"+cause, func(t *testing.T) {
				var allowed atomic.Bool
				allowed.Store(true)
				original := []byte("original public fixture\n")
				originalHash := sha256.Sum256(original)
				backend := &fakeFileBackend{value: append([]byte(nil), original...)}
				bodyReady := make(chan *observedHTTPUploadBody, 1)
				cancelReady := make(chan context.CancelFunc, 1)
				handlerDone := make(chan struct{})
				h := terminalFileHandler{
					open:   func(context.Context, *gin.Context) (terminalfiles.Backend, error) { return backend, nil },
					key:    func(*gin.Context) string { return "host:real-http-test" },
					logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
					checks: func(*gin.Context, ...string) func() bool { return allowed.Load },
				}
				router := gin.New()
				router.Use(func(c *gin.Context) {
					budget := 8 * time.Second
					if cause == "timeout" {
						budget = 250 * time.Millisecond
					}
					ctx, cancel := context.WithTimeout(c.Request.Context(), budget)
					defer cancel()
					body := &observedHTTPUploadBody{ReadCloser: c.Request.Body, readStarted: make(chan struct{}), closed: make(chan struct{})}
					c.Request = c.Request.WithContext(ctx)
					c.Request.Body = body
					bodyReady <- body
					cancelReady <- cancel
					c.Next()
				})
				router.POST("/files/upload", h.upload)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(handlerDone)
					router.ServeHTTP(w, r)
				}))
				defer server.Close()
				connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer connection.Close()
				if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				framingHeader, prefix := "Content-Length: 512\r\n", strings.Repeat("x", 16)
				if framing == "chunked" {
					framingHeader, prefix = "Transfer-Encoding: chunked\r\n", "10\r\n"+prefix+"\r\n"
				}
				if _, err := fmt.Fprintf(connection, "POST /files/upload?path=/demo.txt HTTP/1.1\r\nHost: test.invalid\r\nContent-Type: application/octet-stream\r\nIf-Match: %s\r\n%s\r\n%s", hex.EncodeToString(originalHash[:]), framingHeader, prefix); err != nil {
					t.Fatal(err)
				}
				var body *observedHTTPUploadBody
				select {
				case body = <-bodyReady:
				case <-time.After(time.Second):
					t.Fatal("真实上传没有进入处理器")
				}
				select {
				case <-body.readStarted:
				case <-time.After(time.Second):
					t.Fatal("真实请求体尚未开始读取，不能断言进行中取消")
				}
				cancel := <-cancelReady
				if cause == "revoke" {
					allowed.Store(false)
				} else if cause == "cancel" {
					cancel()
				}
				if err := connection.SetReadDeadline(time.Now().Add(4 * time.Second)); err != nil {
					t.Fatal(err)
				}
				response, responseErr := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPost})
				if responseErr != nil {
					t.Errorf("请求体仍未完成时没有收到取消响应: %v", responseErr)
				} else {
					var payload struct {
						Code string `json:"code"`
					}
					decodeErr := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&payload)
					_ = response.Body.Close()
					status, code := http.StatusForbidden, "file_permission_denied"
					if cause == "timeout" {
						status, code = http.StatusGatewayTimeout, "file_timeout"
					}
					if decodeErr != nil || response.StatusCode != status || payload.Code != code {
						t.Errorf("真实请求取消原因不准确: status=%d code=%s decode=%v", response.StatusCode, payload.Code, decodeErr)
					}
				}
				_ = connection.Close()
				cancel()
				select {
				case <-handlerDone:
				case <-time.After(time.Second):
					t.Fatal("取消并关闭客户端后处理器未退出")
				}
				select {
				case <-body.closed:
				default:
					t.Fatal("处理器返回后真实请求体的取消清理仍未退出")
				}
				if backend.puts != 0 || sha256.Sum256(backend.value) != originalHash || backend.closed != 1 {
					t.Fatalf("取消后仍提交或未释放后端: puts=%d closed=%d", backend.puts, backend.closed)
				}
			})
		}
	}
}

func TestTerminalFilesHTTPCompleteUploadKeepsConnectionReusable(t *testing.T) {
	backend := &fakeFileBackend{}
	router, _ := fileTestRouter(backend, "")
	handled := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r)
		handled <- struct{}{}
	}))
	defer server.Close()
	connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	for _, content := range []string{"first public fixture", "second public fixture"} {
		if _, err := fmt.Fprintf(connection, "POST /files/upload?path=/demo.txt HTTP/1.1\r\nHost: test.invalid\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n%s", len(content), content); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
		if err != nil {
			t.Fatalf("完整上传后连接无法复用: %v", err)
		}
		var payload struct {
			File terminalFileResponse `json:"file"`
		}
		err = json.NewDecoder(response.Body).Decode(&payload)
		_, drainErr := io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		digest := sha256.Sum256([]byte(content))
		if err != nil || drainErr != nil || response.StatusCode != http.StatusOK || payload.File.ETag != hex.EncodeToString(digest[:]) {
			t.Fatalf("完整上传结果错误: status=%d decode=%v drain=%v", response.StatusCode, err, drainErr)
		}
		select {
		case <-handled:
		case <-time.After(time.Second):
			t.Fatal("完整上传后处理器未退出")
		}
	}
	if backend.puts != 2 || backend.closed != 2 {
		t.Fatalf("完整上传未提交或未释放后端: puts=%d closed=%d", backend.puts, backend.closed)
	}
}
