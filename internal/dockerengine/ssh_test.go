package dockerengine

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"zrt/internal/config"
)

func TestSSHConnectionWithPasswordAndDockerCheck(t *testing.T) {
	address, fingerprint, commands := startDockerSSHTestServer(t, "secret")
	service := &Service{config: config.Runtime{ConnectTimeout: time.Second, RequestTimeout: 2 * time.Second}}
	result, err := service.TestSSH(context.Background(), Input{
		Name: "测试 Docker", Host: "ssh://deploy@" + address,
		SSH: &SSHBundle{Password: "secret"},
	})
	if err != nil {
		t.Fatalf("密码方式测试 Docker SSH 失败: %v", err)
	}
	if result.Fingerprint != fingerprint || result.DockerVersion != "27.1.1" {
		t.Fatalf("Docker SSH 测试结果错误: %+v", result)
	}
	if command := <-commands; command != "docker version --format '{{.Server.Version}}'" {
		t.Fatalf("连接测试执行了非预期命令: %s", command)
	}
}

func TestSSHAuthenticationAndDockerPullCommandValidation(t *testing.T) {
	if _, err := sshAuthMethods(SSHBundle{Password: "secret", PrivateKey: "key"}); !errors.Is(err, ErrInvalidSSH) {
		t.Fatalf("同时提供密码和私钥未被拒绝: %v", err)
	}
	command, err := dockerPullCommand("registry.example.com/team/api:2026.07")
	if err != nil || command != "docker pull --quiet 'registry.example.com/team/api:2026.07'" {
		t.Fatalf("docker pull 命令生成错误: command=%q err=%v", command, err)
	}
	if _, err := dockerPullCommand("registry.example.com/team/api:latest; id"); err == nil {
		t.Fatal("包含 Shell 注入内容的镜像地址未被拒绝")
	}
}

func TestLoadImageWithSSHStreamsArchiveAndVerifiesImageID(t *testing.T) {
	address, fingerprint, commands, uploads := startDockerSSHImageLoadTestServer(t, "secret")
	connector, err := newSSHConnector("ssh://deploy@"+address, SSHBundle{Password: "secret"}, fingerprint, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	image := "zrt.local/order-api:abcdef123456-12345678"
	imageID := "sha256:" + strings.Repeat("a", 64)
	archive := "docker-save-archive"
	if err := loadImageWithSSH(context.Background(), connector, image, imageID, strings.NewReader(archive)); err != nil {
		t.Fatalf("通过 SSH 加载镜像失败: %v", err)
	}
	if command := <-commands; command != "docker image load --quiet" {
		t.Fatalf("镜像导入命令错误: %s", command)
	}
	if uploaded := <-uploads; string(uploaded) != archive {
		t.Fatalf("docker save 流没有完整传输: %q", uploaded)
	}
	expectedInspect := "docker image inspect --format '{{.Id}}' '" + image + "'"
	if command := <-commands; command != expectedInspect {
		t.Fatalf("镜像校验命令错误: %s", command)
	}
}

func startDockerSSHTestServer(t *testing.T, password string) (string, string, <-chan string) {
	address, fingerprint, commands, _ := startDockerSSHTestServerWithUploads(t, password)
	return address, fingerprint, commands
}

func startDockerSSHImageLoadTestServer(t *testing.T, password string) (string, string, <-chan string, <-chan []byte) {
	return startDockerSSHTestServerWithUploads(t, password)
}

func startDockerSSHTestServerWithUploads(t *testing.T, password string) (string, string, <-chan string, <-chan []byte) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成 SSH 服务端密钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("解析 SSH 服务端密钥失败: %v", err)
	}
	serverConfig := &ssh.ServerConfig{
		PasswordCallback: func(metadata ssh.ConnMetadata, value []byte) (*ssh.Permissions, error) {
			if metadata.User() != "deploy" || string(value) != password {
				return nil, errors.New("认证失败")
			}
			return nil, nil
		},
	}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听 SSH 测试端口失败: %v", err)
	}
	commands := make(chan string, 10)
	uploads := make(chan []byte, 10)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveDockerSSHTestConnection(connection, serverConfig, commands, uploads)
		}
	}()
	return listener.Addr().String(), ssh.FingerprintSHA256(signer.PublicKey()), commands, uploads
}

func serveDockerSSHTestConnection(connection net.Conn, config *ssh.ServerConfig, commands chan<- string, uploads chan<- []byte) {
	serverConnection, channels, requests, err := ssh.NewServerConn(connection, config)
	if err != nil {
		_ = connection.Close()
		return
	}
	defer serverConnection.Close()
	go ssh.DiscardRequests(requests)
	for channelRequest := range channels {
		if channelRequest.ChannelType() != "session" {
			_ = channelRequest.Reject(ssh.UnknownChannelType, "仅支持 session")
			continue
		}
		channel, channelRequests, err := channelRequest.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for request := range channelRequests {
				if request.Type != "exec" {
					_ = request.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
					_ = request.Reply(false, nil)
					continue
				}
				commands <- payload.Command
				_ = request.Reply(true, nil)
				status := uint32(1)
				switch {
				case payload.Command == "docker version --format '{{.Server.Version}}'":
					_, _ = io.WriteString(channel, "27.1.1\n")
					status = 0
				case payload.Command == "docker image load --quiet":
					value, readErr := io.ReadAll(channel)
					if readErr == nil {
						uploads <- value
						_, _ = io.WriteString(channel, "Loaded image\n")
						status = 0
					}
				case strings.HasPrefix(payload.Command, "docker image inspect --format '{{.Id}}' "):
					_, _ = io.WriteString(channel, "sha256:"+strings.Repeat("a", 64)+"\n")
					status = 0
				default:
					_, _ = io.WriteString(channel.Stderr(), fmt.Sprintf("不支持的命令: %s", strings.TrimSpace(payload.Command)))
				}
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
				return
			}
		}()
	}
}
