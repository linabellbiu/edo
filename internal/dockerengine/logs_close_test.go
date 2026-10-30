package dockerengine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

func TestContainerLogStreamRealSDKReleasesSilentHTTPBody(t *testing.T) {
	for _, action := range []string{"取消", "关闭", "取消并关闭"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			requestCanceled := make(chan struct{})
			handlerDone := make(chan struct{})
			stopServer := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				if r.Method != http.MethodGet || r.URL.Path != "/v1.55/containers/log-close-test/logs" || r.URL.Query().Get("follow") != "1" {
					t.Errorf("日志请求目标或参数错误: method=%s path=%s follow=%s", r.Method, r.URL.Path, r.URL.Query().Get("follow"))
					http.Error(w, "日志请求无效", http.StatusBadRequest)
					return
				}
				// 真实 HTTP 分块响应只发送响应头，后续没有日志或结束标记。
				w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					close(requestCanceled)
				case <-stopServer:
				}
			}))
			var stream *ContainerLogStream
			t.Cleanup(func() {
				cancel()
				close(stopServer)
				server.CloseClientConnections()
				if stream != nil {
					_ = stream.Close()
				}
				server.Close()
			})

			httpClient := server.Client()
			httpClient.Transport.(*http.Transport).ResponseHeaderTimeout = 2 * time.Second
			apiClient, err := client.New(client.WithHTTPClient(httpClient), client.WithHost(server.URL), client.WithAPIVersion("1.55"))
			if err != nil {
				t.Fatalf("创建测试 Docker 客户端失败: %v", err)
			}
			t.Cleanup(func() { _ = apiClient.Close() })
			logs, err := apiClient.ContainerLogs(ctx, "log-close-test", client.ContainerLogsOptions{ShowStdout: true, Follow: true, Tail: "1"})
			if err != nil {
				t.Fatalf("打开静默 HTTP 日志流失败: %v", err)
			}
			stream = &ContainerLogStream{ReadCloser: logs, apiClient: apiClient}

			type readResult struct {
				n   int
				err error
			}
			readDone := make(chan readResult, 1)
			go func() {
				n, err := stream.Read(make([]byte, 1))
				readDone <- readResult{n: n, err: err}
			}()
			select {
			case result := <-readDone:
				t.Fatalf("静默日志在释放前意外结束: n=%d err=%v", result.n, result.err)
			case <-time.After(30 * time.Millisecond):
			}

			completed, stopWaiting := context.WithTimeout(t.Context(), 2*time.Second)
			defer stopWaiting()
			closeDone := make(chan error, 1)
			go func() {
				var closeErr error
				var pending sync.WaitGroup
				start := make(chan struct{})
				if action != "关闭" {
					pending.Go(func() { <-start; cancel() })
				}
				if action != "取消" {
					pending.Go(func() { <-start; closeErr = stream.Close() })
				}
				close(start)
				pending.Wait()
				closeDone <- closeErr
			}()
			var firstCloseErr error
			select {
			case firstCloseErr = <-closeDone:
			case <-completed.Done():
				t.Fatal("释放静默 HTTP 日志流超时")
			}
			select {
			case result := <-readDone:
				if result.n != 0 || result.err == nil {
					t.Errorf("日志流释放后未解除静默读取: n=%d err=%v", result.n, result.err)
				}
			case <-completed.Done():
				t.Fatal("日志流释放后读取仍然阻塞")
			}
			select {
			case <-requestCanceled:
			case <-completed.Done():
				t.Fatal("日志流释放后 HTTP 服务请求未取消")
			}
			select {
			case <-handlerDone:
			case <-completed.Done():
				t.Fatal("日志流释放后 HTTP 服务未退出")
			}
			// 先验证本次动作独立释放请求，再检查重复关闭，避免 Close 掩盖取消失效。
			repeatedClose := make(chan [2]error, 1)
			go func() { repeatedClose <- [2]error{stream.Close(), stream.Close()} }()
			select {
			case closeErrors := <-repeatedClose:
				if closeErrors[0] != closeErrors[1] || (action != "取消" && firstCloseErr != closeErrors[0]) {
					t.Errorf("重复关闭未保留首次关闭结果: first=%v repeated=%v", firstCloseErr, closeErrors)
				}
			case <-completed.Done():
				t.Fatal("重复关闭静默 HTTP 日志流超时")
			}
		})
	}
}
