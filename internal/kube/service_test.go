package kube

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/model"
	"zrt/internal/secret"
)

const safeKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: production
  cluster:
    server: https://kubernetes.example.com
users:
- name: zrt
  user:
    token: test-token
contexts:
- name: production
  context:
    cluster: production
    user: zrt
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
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:kube_cluster_test?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开 Kubernetes 测试数据库失败: %v", err)
	}
	defer database.Close(db)
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移 Kubernetes 测试数据库失败: %v", err)
	}
	manager, _ := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	service := NewService(db, manager, config.Runtime{ConnectTimeout: time.Second, RequestTimeout: time.Second})
	value := safeKubeconfig
	cluster, err := service.Create(ctx, "admin", Input{
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
