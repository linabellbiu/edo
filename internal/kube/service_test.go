package kube

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
)

const safeKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: production
  cluster:
    server: https://kubernetes.example.com
users:
- name: edo
  user:
    token: test-token
contexts:
- name: production
  context:
    cluster: production
    user: edo
    namespace: default
current-context: production
`

func TestSafeKubeconfigRejectsCommandAndFileReferences(t *testing.T) {
	if _, err := safeRESTConfig([]byte(safeKubeconfig)); err != nil {
		t.Fatalf("安全 kubeconfig 被拒绝: %v", err)
	}
	execConfig := strings.ReplaceAll(safeKubeconfig, "token: test-token", "exec:\n      command: malicious\n      apiVersion: client.authentication.k8s.io/v1")
	if _, err := safeRESTConfig([]byte(execConfig)); !errors.Is(err, ErrUnsafeKubeconfig) {
		t.Fatalf("包含 exec 的 kubeconfig 未被拒绝: %v", err)
	}
	fileConfig := strings.ReplaceAll(safeKubeconfig, "server: https://kubernetes.example.com", "server: https://kubernetes.example.com\n    certificate-authority: /etc/passwd")
	if _, err := safeRESTConfig([]byte(fileConfig)); !errors.Is(err, ErrUnsafeKubeconfig) {
		t.Fatalf("包含本地文件引用的 kubeconfig 未被拒绝: %v", err)
	}
	insecureConfig := strings.ReplaceAll(safeKubeconfig, "server: https://kubernetes.example.com", "server: https://kubernetes.example.com\n    insecure-skip-tls-verify: true")
	if _, err := safeRESTConfig([]byte(insecureConfig)); !errors.Is(err, ErrUnsafeKubeconfig) {
		t.Fatalf("关闭 TLS 校验的 kubeconfig 未被拒绝: %v", err)
	}
}

func TestKubeconfigIsEncrypted(t *testing.T) {
	server := newKubeVersionServer(t)
	service := newKubeTestService(t, time.Second)
	value := kubeconfigForServer(server)
	cluster, err := service.Create(context.Background(), "admin", Input{
		Name: "production", Mode: model.KubernetesKubeconfig,
		DefaultNamespace: "default", Kubeconfig: &value,
	})
	if err != nil {
		t.Fatalf("创建 Kubernetes 集群失败: %v", err)
	}
	if cluster.KubeconfigCiphertext == "" || strings.Contains(cluster.KubeconfigCiphertext, "test-token") {
		t.Fatal("kubeconfig 未加密存储")
	}
}

func TestConnectionUsesUnsavedConfigWithoutPersistingIt(t *testing.T) {
	server := newKubeVersionServer(t)
	service := newKubeTestService(t, time.Second)
	kubeconfig := kubeconfigForServer(server)

	result, err := service.Test(context.Background(), Input{
		Name: "preview", Mode: model.KubernetesKubeconfig,
		DefaultNamespace: "default", Kubeconfig: &kubeconfig,
	})
	if err != nil {
		t.Fatalf("测试未保存的 Kubernetes 连接失败: %v", err)
	}
	if result.APIServer != server.URL || result.Version != "v1.32.4" {
		t.Fatalf("Kubernetes 连接测试结果不正确: %+v", result)
	}
	var count int64
	if err := service.db.Model(&model.KubernetesCluster{}).Count(&count).Error; err != nil {
		t.Fatalf("查询 Kubernetes 集群数量失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("连接测试不应保存集群，实际保存 %d 条", count)
	}
}

func TestCreateRejectsUnreachableClusterWithoutPersistingIt(t *testing.T) {
	server := newKubeVersionServer(t)
	kubeconfig := kubeconfigForServer(server)
	server.Close()
	service := newKubeTestService(t, 150*time.Millisecond)

	_, err := service.Create(context.Background(), "admin", Input{
		Name: "unreachable", Mode: model.KubernetesKubeconfig,
		DefaultNamespace: "default", Kubeconfig: &kubeconfig,
	})
	if !errors.Is(err, ErrClusterUnreachable) {
		t.Fatalf("不可连接的 Kubernetes 集群未被拒绝: %v", err)
	}
	var count int64
	if err := service.db.Model(&model.KubernetesCluster{}).Count(&count).Error; err != nil {
		t.Fatalf("查询 Kubernetes 集群数量失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("连接失败后不应保存集群，实际保存 %d 条", count)
	}
}

func TestPingReadsServerVersion(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/version" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"gitVersion":"v1.32.4"}`)
	}))
	defer server.Close()

	service := newKubeTestService(t, 5*time.Second)
	clusterID := createKubeTestCluster(t, service, server)
	version, err := service.Ping(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("检查 Kubernetes API 失败: %v", err)
	}
	if version != "v1.32.4" {
		t.Fatalf("Kubernetes 版本不正确: %q", version)
	}
}

func TestPingHonorsContextDeadline(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	requestCanceled := make(chan struct{}, 1)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if requests.Add(1) == 1 {
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `{"gitVersion":"v1.32.4"}`)
			return
		}
		requestStarted <- struct{}{}
		<-request.Context().Done()
		requestCanceled <- struct{}{}
	}))
	defer server.Close()

	service := newKubeTestService(t, 5*time.Second)
	clusterID := createKubeTestCluster(t, service, server)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	_, err := service.Ping(ctx, clusterID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("调用方超时未传递到 Kubernetes 请求: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= time.Second {
		t.Fatalf("Kubernetes 请求没有及时取消: %s", elapsed)
	}
	select {
	case <-requestStarted:
	default:
		t.Fatal("Kubernetes API 没有收到健康检查请求")
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("Kubernetes API 请求在 context 超时后仍未取消")
	}
}

func newKubeTestService(t *testing.T, requestTimeout time.Duration) *Service {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: dsn,
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开 Kubernetes 测试数据库失败: %v", err)
	}
	t.Cleanup(func() { database.Close(db) })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移 Kubernetes 测试数据库失败: %v", err)
	}
	manager, err := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatalf("初始化 Kubernetes 测试密钥失败: %v", err)
	}
	return NewService(db, manager, config.Runtime{ConnectTimeout: time.Second, RequestTimeout: requestTimeout})
}

func createKubeTestCluster(t *testing.T, service *Service, server *httptest.Server) string {
	t.Helper()
	kubeconfig := kubeconfigForServer(server)
	cluster, err := service.Create(context.Background(), "admin", Input{
		Name: "test-cluster", Mode: model.KubernetesKubeconfig,
		DefaultNamespace: "default", Kubeconfig: &kubeconfig,
	})
	if err != nil {
		t.Fatalf("创建 Kubernetes 测试集群失败: %v", err)
	}
	return cluster.ID
}

func newKubeVersionServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/version" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"gitVersion":"v1.32.4"}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func kubeconfigForServer(server *httptest.Server) string {
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
    certificate-authority-data: %s
users:
- name: edo
  user:
    token: test-token
contexts:
- name: test
  context:
    cluster: test
    user: edo
current-context: test
`, server.URL, base64.StdEncoding.EncodeToString(certificate))
}
