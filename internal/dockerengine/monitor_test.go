package dockerengine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/model"
	"zrt/internal/secret"
)

type countingListener struct {
	net.Listener
	accepted atomic.Int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return connection, err
}

func TestPingForMonitorReusesDockerClient(t *testing.T) {
	status := &atomic.Int32{}
	status.Store(http.StatusOK)
	dockerHost, listener := newDockerPingServer(t, status)
	service, db := newMonitorTestService(t)
	endpoint := createMonitorEndpoint(t, db, "reuse-endpoint", dockerHost, "")

	if _, err := service.PingForMonitor(context.Background(), endpoint.ID); err != nil {
		t.Fatalf("首次 Docker 监控检查失败: %v", err)
	}
	first := monitorClientForTest(t, service, endpoint.ID)
	if _, err := service.PingForMonitor(context.Background(), endpoint.ID); err != nil {
		t.Fatalf("复用 Docker 监控连接失败: %v", err)
	}
	second := monitorClientForTest(t, service, endpoint.ID)
	if first != second {
		t.Fatal("同一 Docker endpoint 没有复用监控客户端")
	}
	if connections := listener.accepted.Load(); connections != 1 {
		t.Fatalf("Docker 监控没有复用已建立的 API 连接: %d", connections)
	}
}

func TestPingForMonitorEvictsFailedClient(t *testing.T) {
	status := &atomic.Int32{}
	status.Store(http.StatusOK)
	dockerHost, listener := newDockerPingServer(t, status)
	service, db := newMonitorTestService(t)
	endpoint := createMonitorEndpoint(t, db, "failure-endpoint", dockerHost, "")

	if _, err := service.PingForMonitor(context.Background(), endpoint.ID); err != nil {
		t.Fatal(err)
	}
	first := monitorClientForTest(t, service, endpoint.ID)
	status.Store(http.StatusServiceUnavailable)
	if _, err := service.PingForMonitor(context.Background(), endpoint.ID); err == nil {
		t.Fatal("Docker API 失败后仍返回成功")
	}
	assertMonitorClientMissing(t, service, endpoint.ID)

	status.Store(http.StatusOK)
	if _, err := service.PingForMonitor(context.Background(), endpoint.ID); err != nil {
		t.Fatalf("Docker API 恢复后重连失败: %v", err)
	}
	if recovered := monitorClientForTest(t, service, endpoint.ID); recovered == first {
		t.Fatal("Docker API 失败后复用了已失效客户端")
	}
	if connections := listener.accepted.Load(); connections < 2 {
		t.Fatalf("Docker API 失败后没有重建连接: %d", connections)
	}
}

func TestMonitorClientInvalidatesWithEndpointLifecycle(t *testing.T) {
	status := &atomic.Int32{}
	status.Store(http.StatusOK)
	dockerHost, _ := newDockerPingServer(t, status)
	service, db := newMonitorTestService(t)
	endpoint := createMonitorEndpoint(t, db, "lifecycle-endpoint", dockerHost, "")
	ctx := context.Background()

	if _, err := service.PingForMonitor(ctx, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	first := monitorClientForTest(t, service, endpoint.ID)
	if _, err := service.Rename(ctx, endpoint.ID, "更新后的 Docker"); err != nil {
		t.Fatalf("更新 Docker endpoint 失败: %v", err)
	}
	assertMonitorClientMissing(t, service, endpoint.ID)
	if _, err := service.PingForMonitor(ctx, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	if updated := monitorClientForTest(t, service, endpoint.ID); updated == first {
		t.Fatal("Docker endpoint 更新后没有重建监控客户端")
	}

	if err := service.SetActive(ctx, endpoint.ID, false); err != nil {
		t.Fatalf("停用 Docker endpoint 失败: %v", err)
	}
	assertMonitorClientMissing(t, service, endpoint.ID)
	if _, err := service.PingForMonitor(ctx, endpoint.ID); !errors.Is(err, ErrEndpointNotFound) {
		t.Fatalf("已停用 Docker endpoint 仍可监控: %v", err)
	}
	if err := service.SetActive(ctx, endpoint.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PingForMonitor(ctx, endpoint.ID); err != nil {
		t.Fatal(err)
	}

	if err := db.Delete(&model.DockerEndpoint{}, "id = ?", endpoint.ID).Error; err != nil {
		t.Fatalf("删除 Docker endpoint 失败: %v", err)
	}
	if _, err := service.PingForMonitor(ctx, endpoint.ID); !errors.Is(err, ErrEndpointNotFound) {
		t.Fatalf("已删除 Docker endpoint 仍可监控: %v", err)
	}
	assertMonitorClientMissing(t, service, endpoint.ID)
}

func TestMonitorClientTracksAssignedHostVersion(t *testing.T) {
	status := &atomic.Int32{}
	status.Store(http.StatusOK)
	dockerHost, _ := newDockerPingServer(t, status)
	service, db := newMonitorTestService(t)
	now := time.Now().UTC()
	host := model.Host{
		ID: "monitor-host", Name: "monitor-host", Mode: model.HostModeSSH,
		Address: "docker.example.com", SSHPort: 22, SSHUsername: "zrt",
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&host).Error; err != nil {
		t.Fatalf("创建 Docker 所属主机失败: %v", err)
	}
	endpoint := createMonitorEndpoint(t, db, "host-version-endpoint", dockerHost, host.ID)
	ctx := context.Background()
	if _, err := service.PingForMonitor(ctx, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	first := monitorClientForTest(t, service, endpoint.ID)
	if err := db.Model(&model.Host{}).Where("id = ?", host.ID).
		Update("updated_at", now.Add(time.Second)).Error; err != nil {
		t.Fatalf("更新 Docker 所属主机版本失败: %v", err)
	}
	if _, err := service.PingForMonitor(ctx, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	if updated := monitorClientForTest(t, service, endpoint.ID); updated == first {
		t.Fatal("所属主机更新后没有重建 Docker 监控客户端")
	}
}

func newDockerPingServer(t *testing.T, status *atomic.Int32) (string, *countingListener) {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/_ping" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("API-Version", "1.48")
		response.WriteHeader(int(status.Load()))
		if request.Method != http.MethodHead {
			_, _ = io.WriteString(response, "docker ping")
		}
	}))
	listener := &countingListener{Listener: server.Listener}
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return strings.Replace(server.URL, "http://", "tcp://", 1), listener
}

func newMonitorTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: dsn,
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开 Docker 监控测试数据库失败: %v", err)
	}
	t.Cleanup(func() { database.Close(db) })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移 Docker 监控测试数据库失败: %v", err)
	}
	manager, err := secret.New("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatalf("初始化 Docker 监控测试密钥失败: %v", err)
	}
	return NewService(db, manager, config.Runtime{
		ConnectTimeout: time.Second, RequestTimeout: time.Second,
	}), db
}

func createMonitorEndpoint(t *testing.T, db *gorm.DB, id, host, hostID string) model.DockerEndpoint {
	t.Helper()
	now := time.Now().UTC()
	endpoint := model.DockerEndpoint{
		ID: id, HostID: hostID, Name: id, Host: host, IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatalf("创建 Docker 监控测试 endpoint 失败: %v", err)
	}
	return endpoint
}

func monitorClientForTest(t *testing.T, service *Service, id string) any {
	t.Helper()
	service.monitorMu.Lock()
	defer service.monitorMu.Unlock()
	cached, exists := service.monitorClients[id]
	if !exists {
		t.Fatalf("Docker endpoint %s 没有缓存监控客户端", id)
	}
	return cached.client
}

func assertMonitorClientMissing(t *testing.T, service *Service, id string) {
	t.Helper()
	service.monitorMu.Lock()
	defer service.monitorMu.Unlock()
	if _, exists := service.monitorClients[id]; exists {
		t.Fatalf("Docker endpoint %s 的失效监控客户端仍在缓存中", id)
	}
}
