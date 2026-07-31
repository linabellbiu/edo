package dockerengine

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/hostcredential"
	"zrt/internal/model"
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
		DockerBuilderHost: "unix:///var/run/docker.sock",
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
	if err := db.Model(&model.HostCapability{}).
		Where("host_id = ? AND kind = ?", model.BuiltinLocalHostID, model.HostCapabilityDocker).
		Update("status", model.HostCapabilityUnchecked).Error; err != nil {
		t.Fatalf("修改本地 Docker 能力状态失败: %v", err)
	}
	uncheckedLocal, err := service.Find(ctx, LocalEndpointID)
	if err != nil || uncheckedLocal.IsActive {
		t.Fatalf("未就绪的本地 Docker 能力仍被当作可发布目标: endpoint=%+v err=%v", uncheckedLocal, err)
	}
	builderClient, err := service.BuilderClient()
	if err != nil {
		t.Fatalf("本地发布能力关闭后不应阻断独立构建客户端初始化: %v", err)
	}
	builderClient.Close()
	if err := db.Delete(&model.HostCapability{}, "host_id = ? AND kind = ?",
		model.BuiltinLocalHostID, model.HostCapabilityDocker).Error; err != nil {
		t.Fatalf("删除本地 Docker 能力失败: %v", err)
	}
	disabledLocal, err := service.Find(ctx, LocalEndpointID)
	if err != nil || disabledLocal.IsActive {
		t.Fatalf("已关闭的本地 Docker 能力仍被当作可发布目标: endpoint=%+v err=%v", disabledLocal, err)
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
		SSH: &SSHBundle{
			PrivateKey: privateKeyPEM, UseSudo: true,
		},
		SSHHostKeyFingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32)),
	})
	if err != nil || sshEndpoint.SSHCredentialCiphertext == "" || sshEndpoint.SSHCredentialCiphertext == privateKeyPEM {
		t.Fatalf("创建 SSH Docker 连接失败: endpoint=%+v err=%v", sshEndpoint, err)
	}
	decryptedSSH, err := manager.Decrypt(sshEndpoint.SSHCredentialCiphertext, sshAAD(sshEndpoint.ID))
	if err != nil {
		t.Fatalf("解密 SSH Docker 凭据失败: %v", err)
	}
	var storedSSH SSHBundle
	if err := json.Unmarshal([]byte(decryptedSSH), &storedSSH); err != nil {
		t.Fatalf("解析 SSH Docker 凭据失败: %v", err)
	}
	if !storedSSH.UseSudo || storedSSH.SudoPassword != "" {
		t.Fatalf("sudo 配置没有随 SSH 凭据加密保存: %+v", storedSSH)
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

	now := time.Now().UTC()
	host := model.Host{
		ID: "host-backed-docker", Name: "主机凭据兼容测试", Mode: model.HostModeSSH,
		Address: "new-host.example.com", SSHPort: 2202, SSHUsername: "operator",
		SSHHostKeyFingerprint: sshEndpoint.SSHHostKeyFingerprint,
		IsActive:              true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&host).Error; err != nil {
		t.Fatalf("创建 Docker 所属主机失败: %v", err)
	}
	if err := db.Model(&model.DockerEndpoint{}).Where("id = ?", sshEndpoint.ID).
		Update("host_id", host.ID).Error; err != nil {
		t.Fatalf("绑定 Docker 所属主机失败: %v", err)
	}
	sshEndpoint.HostID = host.ID
	hostURL, fallbackBundle, fingerprint, err := service.sshConfiguration(ctx, sshEndpoint)
	if err != nil {
		t.Fatalf("旧 Endpoint SSH 密文回退失败: %v", err)
	}
	if hostURL != "ssh://operator@new-host.example.com:2202" ||
		fallbackBundle.PrivateKey != privateKeyPEM ||
		fingerprint != host.SSHHostKeyFingerprint {
		t.Fatalf("旧 Endpoint SSH 密文回退结果错误: host=%q bundle=%+v fingerprint=%q", hostURL, fallbackBundle, fingerprint)
	}

	hostBundle := SSHBundle{Password: "host-password", UseSudo: true}
	hostPayload, _ := json.Marshal(hostBundle)
	hostCiphertext, err := manager.Encrypt(string(hostPayload), hostcredential.AAD(host.ID))
	if err != nil {
		t.Fatalf("加密主机 SSH 凭据失败: %v", err)
	}
	if err := db.Model(&model.Host{}).Where("id = ?", host.ID).
		Updates(map[string]any{
			"ssh_auth_type":             model.SSHAuthPassword,
			"ssh_credential_ciphertext": hostCiphertext,
		}).Error; err != nil {
		t.Fatalf("保存主机 SSH 凭据失败: %v", err)
	}
	_, preferredBundle, _, err := service.sshConfiguration(ctx, sshEndpoint)
	if err != nil {
		t.Fatalf("读取主机 SSH 凭据失败: %v", err)
	}
	if preferredBundle.Password != hostBundle.Password || preferredBundle.PrivateKey != "" {
		t.Fatalf("未优先使用主机 SSH 凭据: %+v", preferredBundle)
	}
}

func TestCompactContainerImageReferenceKeepsVersionReadable(t *testing.T) {
	digest := strings.Repeat("a", 64)
	if actual := compactContainerImageReference("registry.example.com/team/order_api@sha256:" + digest); actual != "order_api@aaaaaaaaaaaa" {
		t.Fatalf("Digest 镜像没有压缩为可读摘要: %q", actual)
	}
	if actual := compactContainerImageReference("registry.example.com/team/order_api:fea2410d1e47"); actual != "order_api:fea2410d1e47" {
		t.Fatalf("正式版本标签不应被改写: %q", actual)
	}
}
