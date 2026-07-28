package dockerengine

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/secret"
)

func TestDockerEndpointSecurityDefaults(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:docker_endpoint_test?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开 Docker 测试数据库失败: %v", err)
	}
	defer database.Close(db)
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移 Docker 测试数据库失败: %v", err)
	}
	manager, _ := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	service := NewService(db, manager, config.Runtime{
		ConnectTimeout: time.Second, RequestTimeout: time.Second,
		DockerBuilderHost: "tcp://docker-builder:2375",
	})

	localEndpoint, err := service.Find(ctx, LocalEndpointID)
	if err != nil || !localEndpoint.IsActive || localEndpoint.Host != localEndpointHost || localEndpoint.SSHCredentialCiphertext != "" {
		t.Fatalf("内置本地 Docker 连接无效: endpoint=%+v err=%v", localEndpoint, err)
	}
	endpoints, err := service.List(ctx)
	if err != nil || len(endpoints) != 1 || endpoints[0].ID != LocalEndpointID {
		t.Fatalf("Docker 连接列表没有包含内置本地目标: endpoints=%+v err=%v", endpoints, err)
	}
	renamedLocal, err := service.Rename(ctx, LocalEndpointID, "开发机 Docker")
	if err != nil || renamedLocal.Name != "开发机 Docker" || renamedLocal.Host != localEndpointHost {
		t.Fatalf("修改本地 Docker 连接名称失败: endpoint=%+v err=%v", renamedLocal, err)
	}
	persistedLocal, err := service.Find(ctx, LocalEndpointID)
	if err != nil || persistedLocal.Name != "开发机 Docker" || persistedLocal.Host != localEndpointHost {
		t.Fatalf("本地 Docker 连接名称未持久化: endpoint=%+v err=%v", persistedLocal, err)
	}

	endpoint, err := service.Create(ctx, "admin", Input{Name: "local-docker", Host: "unix:///var/run/docker.sock"})
	if err != nil || endpoint.TLSCiphertext != "" {
		t.Fatalf("创建本地 Docker Socket 失败: endpoint=%+v err=%v", endpoint, err)
	}
	renamedEndpoint, err := service.Rename(ctx, endpoint.ID, "开发 Docker Socket")
	if err != nil || renamedEndpoint.Name != "开发 Docker Socket" || renamedEndpoint.Host != endpoint.Host {
		t.Fatalf("修改 Docker 连接名称影响了连接配置: endpoint=%+v err=%v", renamedEndpoint, err)
	}
	_, err = service.Create(ctx, "admin", Input{Name: "unsafe-remote", Host: "tcp://docker.example.com:2375"})
	if !errors.Is(err, ErrTLSRequired) {
		t.Fatalf("远程明文 Docker API 未被拒绝: %v", err)
	}
	_, err = service.Create(ctx, "admin", Input{Name: "bad-scheme", Host: "http://docker.example.com:2375"})
	if !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("不受支持的 Docker URL 未被拒绝: %v", err)
	}
	_, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	encodedKey, _ := x509.MarshalPKCS8PrivateKey(privateKey)
	privateKeyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}))
	sshEndpoint, err := service.Create(ctx, "admin", Input{
		Name: "ssh-docker", Host: "ssh://deploy@docker.example.com:22",
		SSH: &SSHBundle{PrivateKey: privateKeyPEM}, SSHHostKeyFingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32)),
	})
	if err != nil || sshEndpoint.SSHCredentialCiphertext == "" || sshEndpoint.SSHCredentialCiphertext == privateKeyPEM {
		t.Fatalf("创建 SSH Docker 连接失败: endpoint=%+v err=%v", sshEndpoint, err)
	}
	_, err = service.Create(ctx, "admin", Input{
		Name: "ssh-without-fingerprint", Host: "ssh://deploy@docker.example.com",
		SSH: &SSHBundle{PrivateKey: privateKeyPEM},
	})
	if !errors.Is(err, ErrSSHRequired) {
		t.Fatalf("缺少主机指纹的 SSH Docker 连接未被拒绝: %v", err)
	}
	_, err = service.Create(ctx, "admin", Input{
		Name: "ssh-invalid-port", Host: "ssh://deploy@docker.example.com:0",
		SSH: &SSHBundle{PrivateKey: privateKeyPEM}, SSHHostKeyFingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32)),
	})
	if !errors.Is(err, ErrInvalidSSH) {
		t.Fatalf("无效 SSH 端口未被拒绝: %v", err)
	}
}
