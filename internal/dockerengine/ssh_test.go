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

func TestDockerPullWithRegistryAuthKeepsCredentialOutOfCommand(t *testing.T) {
	const credential = "private-password-value"
	command, input, err := dockerPullWithRegistryAuth("docker pull --quiet 'registry.example.com/app@sha256:abc'", RegistryAuth{
		ServerAddress: "https://registry.example.com", Host: "registry.example.com",
		Username: "deployer", Credential: credential,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(command, credential) || !strings.Contains(command, "--password-stdin") ||
		!strings.Contains(command, "mktemp -d /tmp/zrt-docker-config") {
		t.Fatalf("远程拉取命令没有安全传递认证信息: %s", command)
	}
	payload, err := io.ReadAll(input)
	if err != nil || string(payload) != credential+"\n" {
		t.Fatalf("远程登录标准输入不正确: %q err=%v", payload, err)
	}
	prepared, preparedInput := (dockerSSHCommandMode{prefix: "sudo -S -p '' -- ", password: "sudo-password"}).prepare(
		command, strings.NewReader(credential+"\n"),
	)
	if strings.Contains(prepared, credential) || strings.Contains(prepared, "sudo-password") {
		t.Fatal("registry 或 sudo 凭据进入了远程命令")
	}
	combined, err := io.ReadAll(preparedInput)
	if err != nil || string(combined) != "sudo-password\n"+credential+"\n" {
		t.Fatalf("sudo 与 registry 凭据的标准输入顺序错误: %q err=%v", combined, err)
	}
}

func TestDockerPullWithIdentityTokenUsesTemporaryConfig(t *testing.T) {
	const credential = "identity-token-value"
	command, input, err := dockerPullWithRegistryAuth("docker pull --quiet image", RegistryAuth{
		Host: "registry.example.com", Credential: credential,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(command, credential) || !strings.Contains(command, "config.json") || strings.Contains(command, "docker login") {
		t.Fatalf("identity token 远程命令不安全: %s", command)
	}
	payload, err := io.ReadAll(input)
	if err != nil || !strings.Contains(string(payload), credential) {
		t.Fatalf("identity token 没有通过标准输入配置传递: %q err=%v", payload, err)
	}
}

func TestSSHConnectionReusesSSHPasswordForSudo(t *testing.T) {
	address, fingerprint, commands := startDockerSSHPasswordSudoTestServer(t, "secret")
	service := &Service{config: config.Runtime{ConnectTimeout: time.Second, RequestTimeout: 2 * time.Second}}
	result, err := service.TestSSH(context.Background(), Input{
		Name: "测试 Docker", Host: "ssh://deploy@" + address,
		SSH: &SSHBundle{Password: "secret", UseSudo: true},
	})
	if err != nil {
		t.Fatalf("sudo 密码方式测试 Docker SSH 失败: %v", err)
	}
	if result.Fingerprint != fingerprint || result.DockerVersion != "27.1.1" {
		t.Fatalf("Docker SSH sudo 测试结果错误: %+v", result)
	}
	if command := <-commands; command != "sudo -n -- docker version --format '{{.Server.Version}}'" {
		t.Fatalf("连接测试没有先检测免密 sudo: %s", command)
	}
	if command := <-commands; command != "sudo -S -p '' -- docker version --format '{{.Server.Version}}'" {
		t.Fatalf("连接测试没有通过 sudo 执行 Docker: %s", command)
	}
}

func TestSSHAuthenticationAndDockerPullCommandValidation(t *testing.T) {
	if _, err := sshAuthMethods(SSHBundle{Password: "secret", PrivateKey: "key"}); !errors.Is(err, ErrInvalidSSH) {
		t.Fatalf("同时提供密码和私钥未被拒绝: %v", err)
	}
	if _, err := sshAuthMethods(SSHBundle{Password: "secret", UseSudo: true, SudoPassword: "bad\npassword"}); !errors.Is(err, ErrInvalidSSH) {
		t.Fatalf("包含换行的 sudo 密码未被拒绝: %v", err)
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
	sourceImageID := "sha256:" + strings.Repeat("b", 64)
	targetImageID := "sha256:" + strings.Repeat("a", 64)
	archive := "docker-save-archive"
	bundle := SSHBundle{Password: "secret", UseSudo: true}
	loadedImageID, err := loadImageWithSSH(context.Background(), connector, bundle, image, strings.NewReader(archive))
	if err != nil {
		t.Fatalf("通过 SSH 加载镜像失败: %v", err)
	}
	if loadedImageID != targetImageID || loadedImageID == sourceImageID {
		t.Fatalf("没有返回目标 Docker daemon 的镜像 ID: %s", loadedImageID)
	}
	if command := <-commands; command != "sudo -n -- docker version --format '{{.Server.Version}}'" {
		t.Fatalf("镜像导入前没有检测免密 sudo: %s", command)
	}
	if command := <-commands; command != "sudo -S -p '' -- docker image load --quiet" {
		t.Fatalf("镜像导入命令错误: %s", command)
	}
	if uploaded := <-uploads; string(uploaded) != archive {
		t.Fatalf("docker save 流没有完整传输: %q", uploaded)
	}
	expectedInspect := "sudo -S -p '' -- docker image inspect --format '{{.Id}}' '" + targetImageID + "'"
	if command := <-commands; command != expectedInspect {
		t.Fatalf("镜像校验命令错误: %s", command)
	}
	expectedTag := "sudo -S -p '' -- docker image tag '" + targetImageID + "' '" + image + "'"
	if command := <-commands; command != expectedTag {
		t.Fatalf("镜像受控标签命令错误: %s", command)
	}
}

func TestParseDockerLoadImageIDRejectsMissingOrAmbiguousResult(t *testing.T) {
	first := "sha256:" + strings.Repeat("a", 64)
	second := "sha256:" + strings.Repeat("b", 64)
	if actual, err := parseDockerLoadImageID("warning\nLoaded image ID: " + first + "\n"); err != nil || actual != first {
		t.Fatalf("没有解析本次 docker load 返回的 Image ID: actual=%q err=%v", actual, err)
	}
	for _, output := range []string{
		"Loaded image: zrt.local/app:mutable\n",
		"Loaded image ID: sha256:short\n",
		"Loaded image ID: " + first + "\nLoaded image ID: " + second + "\n",
	} {
		if _, err := parseDockerLoadImageID(output); err == nil {
			t.Fatalf("无法确认的 docker load 输出未被拒绝: %q", output)
		}
	}
}

func TestDockerSSHCommandSupportsPasswordlessSudo(t *testing.T) {
	mode := dockerSSHCommandMode{prefix: "sudo -n -- "}
	command, input := mode.prepare("docker info", nil)
	if command != "sudo -n -- docker info" || input != nil {
		t.Fatalf("免密 sudo 命令错误: command=%q input=%v", command, input)
	}
}

func startDockerSSHTestServer(t *testing.T, password string) (string, string, <-chan string) {
	address, fingerprint, commands, _ := startDockerSSHTestServerWithUploads(t, password, false)
	return address, fingerprint, commands
}

func startDockerSSHPasswordSudoTestServer(t *testing.T, password string) (string, string, <-chan string) {
	address, fingerprint, commands, _ := startDockerSSHTestServerWithUploads(t, password, true)
	return address, fingerprint, commands
}

func startDockerSSHImageLoadTestServer(t *testing.T, password string) (string, string, <-chan string, <-chan []byte) {
	return startDockerSSHTestServerWithUploads(t, password, true)
}

func startDockerSSHTestServerWithUploads(t *testing.T, password string, sudoPasswordRequired bool) (string, string, <-chan string, <-chan []byte) {
	return startDockerSSHTestServerWithCapabilities(t, password, sudoPasswordRequired, true)
}

func startDockerSSHTestServerWithCapabilities(
	t *testing.T,
	password string,
	sudoPasswordRequired bool,
	composeAvailable bool,
) (string, string, <-chan string, <-chan []byte) {
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
			go serveDockerSSHTestConnection(connection, serverConfig, password, sudoPasswordRequired, composeAvailable, commands, uploads)
		}
	}()
	return listener.Addr().String(), ssh.FingerprintSHA256(signer.PublicKey()), commands, uploads
}

func serveDockerSSHTestConnection(
	connection net.Conn,
	config *ssh.ServerConfig,
	sudoPassword string,
	sudoPasswordRequired bool,
	composeAvailable bool,
	commands chan<- string,
	uploads chan<- []byte,
) {
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
				command := payload.Command
				var commandInput []byte
				inputRead := false
				if strings.HasPrefix(command, "sudo -S -p '' -- ") {
					value, readErr := io.ReadAll(channel)
					passwordLine := sudoPassword + "\n"
					if readErr != nil || !strings.HasPrefix(string(value), passwordLine) {
						_, _ = io.WriteString(channel.Stderr(), "sudo 认证失败")
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
						return
					}
					commandInput = value[len(passwordLine):]
					inputRead = true
					command = strings.TrimPrefix(command, "sudo -S -p '' -- ")
				} else if strings.HasPrefix(command, "sudo -n -- ") {
					if sudoPasswordRequired {
						_, _ = io.WriteString(channel.Stderr(), "sudo 需要密码")
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
						return
					}
					command = strings.TrimPrefix(command, "sudo -n -- ")
				}
				switch {
				case command == "docker version --format '{{.Server.Version}}'":
					_, _ = io.WriteString(channel, "27.1.1\n")
					status = 0
				case command == "docker image load --quiet":
					if !inputRead {
						var readErr error
						commandInput, readErr = io.ReadAll(channel)
						if readErr != nil {
							break
						}
					}
					if commandInput != nil {
						uploads <- commandInput
						_, _ = io.WriteString(channel, "Loaded image ID: sha256:"+strings.Repeat("a", 64)+"\n")
						status = 0
					}
				case strings.HasPrefix(command, "docker image inspect --format '{{.Id}}' "):
					_, _ = io.WriteString(channel, "sha256:"+strings.Repeat("a", 64)+"\n")
					status = 0
				case strings.HasPrefix(command, "docker image tag "):
					status = 0
				case command == "docker compose version --short":
					if composeAvailable {
						_, _ = io.WriteString(channel, "2.39.2\n")
						status = 0
					}
				case strings.HasPrefix(command, "env ZRT_IMAGE=") && strings.Contains(command, " docker 'compose' "):
					if !inputRead {
						var readErr error
						commandInput, readErr = io.ReadAll(channel)
						if readErr != nil {
							break
						}
					}
					uploads <- commandInput
					_, _ = io.WriteString(channel, "Compose deployment accepted\n")
					status = 0
				default:
					_, _ = io.WriteString(channel.Stderr(), fmt.Sprintf("不支持的命令: %s", strings.TrimSpace(command)))
				}
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
				return
			}
		}()
	}
}
