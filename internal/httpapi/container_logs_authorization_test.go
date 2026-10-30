package httpapi

import (
	"bytes"
	"context"
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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/moby/moby/api/pkg/stdcopy"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/dockerengine"
)

type authorizationLogService struct{ source io.ReadCloser }

func (s authorizationLogService) ContainerLogs(context.Context, string, string, dockerengine.ContainerLogOptions) (*dockerengine.ContainerLogStream, error) {
	return &dockerengine.ContainerLogStream{ReadCloser: s.source}, nil
}

func logWriterConnection(t *testing.T, serve func(*containerLogWriter)) *websocket.Conn {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		serve(&containerLogWriter{connection: connection})
	}))
	t.Cleanup(server.Close)
	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal("测试写入连接未建立")
	}
	t.Cleanup(func() { _ = connection.Close() })
	_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
	return connection
}

func TestContainerLogWriterRejectsRevokedBufferedData(t *testing.T) {
	connection := logWriterConnection(t, func(writer *containerLogWriter) {
		var allowed atomic.Bool
		allowed.Store(true)
		writer.authorized = allowed.Load
		if _, err := writer.Write([]byte("公开缓冲测试\n")); err != nil {
			t.Error("写入初始缓冲失败")
			return
		}
		allowed.Store(false)
		if err := writer.event(containerLogEvent{Type: "complete"}); !errors.Is(err, errContainerLogAccessRevoked) {
			t.Error("结束前刷新绕过了撤权")
		}
	})
	_, _, err := connection.ReadMessage()
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("撤权后的缓存被发送或没有策略关闭: %v", err)
	}
}

func TestContainerLogWriterRechecksEveryBoundedFrame(t *testing.T) {
	connection := logWriterConnection(t, func(writer *containerLogWriter) {
		checks := 0
		writer.authorized = func() bool { checks++; return checks == 1 }
		_, err := writer.Write(bytes.Repeat([]byte{'x'}, 96*1024))
		if !errors.Is(err, errContainerLogAccessRevoked) {
			t.Error("第二帧未拒绝撤权")
		}
	})
	messageType, payload, err := connection.ReadMessage()
	if err != nil || messageType != websocket.BinaryMessage || len(payload) != 32*1024 {
		t.Fatal("第一帧没有遵守32KiB边界")
	}
	if _, _, err := connection.ReadMessage(); !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("大段日志只在开头检查了授权: %v", err)
	}
}

func TestContainerLogWriterBatchesHistoryAndFlushesCompletion(t *testing.T) {
	var checks atomic.Int32
	connection := logWriterConnection(t, func(writer *containerLogWriter) {
		writer.authorized = func() bool { checks.Add(1); return true }
		for range 5000 {
			if _, err := writer.Write([]byte("公开测试日志 public fixture line\n")); err != nil {
				t.Error("批量历史写入失败")
				return
			}
		}
		if err := writer.event(containerLogEvent{Type: "complete"}); err != nil {
			t.Error("结束事件没有刷新最后一批")
		}
	})
	lines := 0
	for {
		messageType, payload, err := connection.ReadMessage()
		if err != nil {
			t.Fatal("完整历史输出提前断开")
		}
		if messageType == websocket.TextMessage {
			break
		}
		if len(payload) > 32*1024 {
			t.Fatal("日志帧超出32KiB")
		}
		lines += bytes.Count(payload, []byte{'\n'})
	}
	if lines != 5000 || checks.Load() > 16 {
		t.Fatalf("历史丢失或逐行查询授权: lines=%d checks=%d", lines, checks.Load())
	}
	t.Logf("5000条历史日志实际授权检查%d次", checks.Load())
}

func TestContainerLogWriterConcurrentWritesAndFlush(t *testing.T) {
	connection := logWriterConnection(t, func(writer *containerLogWriter) {
		var group sync.WaitGroup
		for range 4 {
			group.Go(func() {
				for range 100 {
					if _, err := writer.Write([]byte("test\n")); err != nil {
						t.Error("并发写入失败")
						return
					}
					if err := writer.flush(); err != nil {
						t.Error("并发刷新失败")
						return
					}
				}
			})
		}
		group.Wait()
		_ = writer.event(containerLogEvent{Type: "complete"})
	})
	lines := 0
	for {
		messageType, payload, err := connection.ReadMessage()
		if err != nil {
			t.Fatal("并发输出提前结束")
		}
		if messageType == websocket.TextMessage {
			break
		}
		lines += bytes.Count(payload, []byte{'\n'})
	}
	if lines != 400 {
		t.Fatalf("并发刷新丢失或重复日志: lines=%d", lines)
	}
}

func TestContainerLogWriterPreservesWriteFailure(t *testing.T) {
	connection := logWriterConnection(t, func(writer *containerLogWriter) {
		_ = writer.connection.Close()
		if _, err := writer.Write([]byte("公开测试\n")); err != nil {
			t.Error("初始缓冲不应需要网络写入")
			return
		}
		if err := writer.flush(); err == nil {
			t.Error("网络写入失败被忽略")
		}
		if _, err := writer.Write([]byte("不得继续\n")); err == nil {
			t.Error("网络失败后仍接受新的日志")
		}
	})
	_, _, _ = connection.ReadMessage()
}

func TestContainerLogsFlushLowVolumeWithoutWaitingForEOF(t *testing.T) {
	reader, input := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = input.Close() })
	handler := containerLogHandler{docker: authorizationLogService{source: reader}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	router := gin.New()
	router.GET("/logs/:id/:container_id", handler.stream)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	dialer := websocket.Dialer{Subprotocols: []string{containerLogSubprotocol}}
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/logs/fixture/public-container", nil)
	if err != nil {
		t.Fatal("低吞吐测试连接未建立")
	}
	t.Cleanup(func() { _ = connection.Close() })
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	var ready containerLogEvent
	if err := connection.ReadJSON(&ready); err != nil || ready.Type != "ready" {
		t.Fatal("未收到低吞吐就绪事件")
	}
	var frame bytes.Buffer
	writeDockerLogFrame(t, &frame, stdcopy.Stdout, "single line\n")
	started := time.Now()
	go func() { _, _ = input.Write(frame.Bytes()) }()
	messageType, payload, err := connection.ReadMessage()
	if err != nil || messageType != websocket.BinaryMessage || string(payload) != "single line\n" || time.Since(started) > 500*time.Millisecond {
		t.Fatal("少量日志没有按100ms刷新节奏送达")
	}
	t.Logf("单行日志等待%s，源仍未EOF", time.Since(started))
}

// 真实会话与项目授权配合受控日志源；不访问 Docker 或 Kubernetes。
func TestContainerLogsStopNewDataAfterReadPermissionRevoked(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	reader, input := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = input.Close() })
	handler := containerLogHandler{
		docker: authorizationLogService{source: reader},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		streamAuth: func(c *gin.Context) func() bool {
			return streamAuthorization(f.deps, c, access.PermissionClusterRead)
		},
	}
	router := gin.New()
	done := make(chan struct{})
	router.GET("/logs/:id/:container_id", func(c *gin.Context) {
		defer close(done)
		c.Request = c.Request.WithContext(database.WithDepartmentScope(c.Request.Context(), database.DepartmentScope{
			UserID: f.user.ID, DepartmentID: f.project.DepartmentID, ProjectID: f.project.ID,
		}))
		c.Set(currentUserKey, f.user)
		handler.stream(c)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	dialer := websocket.Dialer{Subprotocols: []string{containerLogSubprotocol}}
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/logs/fixture/public-container", http.Header{"Cookie": []string{f.cookie.String()}})
	if err != nil {
		t.Fatal("测试日志连接未建立")
	}
	t.Cleanup(func() { _ = connection.Close() })
	_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
	var ready containerLogEvent
	if err := connection.ReadJSON(&ready); err != nil || ready.Type != "ready" {
		t.Fatal("未收到日志就绪事件")
	}
	if err := f.deps.Access.SetUserPermissions(context.Background(), f.user.ID, access.UserPermissionOverrides{Deny: []string{access.PermissionClusterRead}}); err != nil {
		t.Fatal(err)
	}
	var frame bytes.Buffer
	writeDockerLogFrame(t, &frame, stdcopy.Stdout, "撤权后不应发送的公开测试日志\n")
	go func() { _, _ = input.Write(frame.Bytes()) }()
	messageType, _, err := connection.ReadMessage()
	if err == nil && messageType == websocket.BinaryMessage {
		t.Fatal("读取权限撤销后仍发送新的容器日志正文")
	}
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("未在下次正文发送前按策略关闭日志连接: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("撤权后处理器未及时释放日志源")
	}
}

// 走真实 Pod 日志路由与 client-go，但 API 是本测试创建的 TLS 服务，不连接任何真实集群。
func TestKubernetesLogsStopNewDataAfterSessionRevoked(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	send := make(chan struct{})
	clusterAPI := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"gitVersion":"v1.35.5"}`)
		case "/api/v1/namespaces/team/pods/public-pod/log":
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-send:
				_, _ = io.WriteString(w, "撤权后不应发送的公开Pod测试日志\n")
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(clusterAPI.Close)
	admin := loginForDepartmentTest(t, f.router, "admin")
	payload, _ := json.Marshal(map[string]any{"name": "pod-log-authorization", "mode": "kubeconfig", "default_namespace": "team", "kubeconfig": kubeconfigForHTTPTest(clusterAPI)})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/kubernetes/clusters", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-EDO-Project-ID", f.project.ID)
	request.AddCookie(admin)
	created := httptest.NewRecorder()
	f.router.ServeHTTP(created, request)
	var value struct{ Cluster struct{ ID string } }
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &value) != nil || value.Cluster.ID == "" {
		t.Fatalf("测试隔离集群记录未创建: status=%d", created.Code)
	}
	server := httptest.NewServer(f.router)
	t.Cleanup(server.Close)
	dialer := websocket.Dialer{Subprotocols: []string{containerLogSubprotocol}}
	path := "/api/v1/kubernetes/clusters/" + value.Cluster.ID + "/namespaces/team/pods/public-pod/containers/app/logs/ws"
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+path, http.Header{"Cookie": []string{f.cookie.String()}, "X-EDO-Project-ID": []string{f.project.ID}})
	if err != nil {
		t.Fatal("测试Pod日志连接未建立")
	}
	t.Cleanup(func() { _ = connection.Close() })
	_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
	var ready containerLogEvent
	if err := connection.ReadJSON(&ready); err != nil || ready.Type != "ready" {
		t.Fatal("未收到Pod日志就绪事件")
	}
	if err := f.deps.Sessions.Delete(context.Background(), f.cookie.Value); err != nil {
		t.Fatal(err)
	}
	close(send)
	if _, _, err := connection.ReadMessage(); !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("会话撤销后Pod仍下发日志或未按策略关闭: %v", err)
	}
}
