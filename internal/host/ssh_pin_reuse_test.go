package host

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"edo/internal/config"
	"edo/internal/dockerengine"
	"edo/internal/hostcredential"
	"edo/internal/model"
)

func createReuseFixture(t *testing.T, service *Service, input Input) *Detail {
	t.Helper()
	tested, err := service.Test(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.TestToken = tested.Token
	created, err := service.Create(context.Background(), "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestExistingCredentialReusePinsBothProbesAndPreservesSavedState(t *testing.T) {
	for _, docker := range []bool{false, true} {
		t.Run(fmt.Sprint("docker=", docker), func(t *testing.T) {
			service, db, secrets := newHostTestService(t)
			recorder := &recordingDockerProbe{fingerprint: "SHA256:" + strings.Repeat("A", 43)}
			service.docker = recorder
			input := hostTestInput()
			input.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilitySSH}
			if docker {
				input.CapabilityKinds = append(input.CapabilityKinds, model.HostCapabilityDocker)
			}
			input.SSH.UseSudo = false
			created := createReuseFixture(t, service, input)
			if recorder.input.SSHHostKeyFingerprint != "" {
				t.Fatal("新建主机首次测试不应凭空固定指纹")
			}
			useSudo := true
			input.SSH = nil
			input.ReuseCredential = true
			input.UseSudo = &useSudo
			tested, err := service.TestExisting(context.Background(), created.Host.ID, input)
			if err != nil || tested.Token == "" || recorder.input.SSHHostKeyFingerprint != created.Host.SSHHostKeyFingerprint || recorder.input.SSH == nil || recorder.input.SSH.Password != "test-password" || !recorder.input.SSH.UseSudo {
				t.Fatalf("未固定原指纹并复用凭据: %v", err)
			}
			input.TestToken = tested.Token
			updated, err := service.Update(context.Background(), created.Host.ID, input)
			if err != nil || !updated.UseSudo {
				t.Fatalf("未保存真实sudo状态: %v", err)
			}
			plain, err := secrets.Decrypt(updated.Host.SSHCredentialCiphertext, hostcredential.AAD(updated.Host.ID))
			if err != nil {
				t.Fatal(err)
			}
			var bundle dockerengine.SSHBundle
			if err := json.Unmarshal([]byte(plain), &bundle); err != nil || bundle.Password != "test-password" || !bundle.UseSudo {
				t.Fatal("sudo更新丢失凭据")
			}
			kinds := make([]model.HostCapabilityKind, 0, len(updated.Capabilities))
			for _, capability := range updated.Capabilities {
				kinds = append(kinds, capability.Kind)
				if capability.Kind == model.HostCapabilitySSH && capability.UseSudo {
					t.Fatal("不应混淆能力标记与实际授权摘要")
				}
			}
			if !slices.Equal(kinds, recorder.inputCapabilityKindsForTest(input)) {
				t.Fatal("更新sudo改变了原能力")
			}
			listed, err := service.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range listed {
				if item.Host.ID == updated.Host.ID {
					found = item.UseSudo
				}
			}
			if !found {
				t.Fatal("列表摘要未读取真实bundle授权")
			}
			tested, err = service.TestExisting(context.Background(), created.Host.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&model.Host{}).Where(&model.Host{ID: created.Host.ID}).Update("ssh_host_key_fingerprint", "SHA256:"+strings.Repeat("B", 43)).Error; err != nil {
				t.Fatal(err)
			}
			input.TestToken = tested.Token
			if _, err := service.Update(context.Background(), created.Host.ID, input); !errors.Is(err, ErrHostTestRequired) {
				t.Fatalf("保存指纹已变化仍可消费旧测试令牌: %v", err)
			}
		})
	}
}

func (*recordingDockerProbe) inputCapabilityKindsForTest(input Input) []model.HostCapabilityKind {
	kinds := append([]model.HostCapabilityKind(nil), input.CapabilityKinds...)
	slices.Sort(kinds)
	return kinds
}

func TestExistingCredentialReuseRejectsMissingPinBeforeAnyProbe(t *testing.T) {
	service, db, _ := newHostTestService(t)
	input := hostTestInput()
	created := createReuseFixture(t, service, input)
	if err := db.Model(&model.Host{}).Where(&model.Host{ID: created.Host.ID}).Update("ssh_host_key_fingerprint", "").Error; err != nil {
		t.Fatal(err)
	}
	recorder := &recordingDockerProbe{}
	service.docker = recorder
	input.SSH = nil
	input.ReuseCredential = true
	tested, err := service.TestExisting(context.Background(), created.Host.ID, input)
	if !errors.Is(err, ErrHostFingerprintRequired) || tested.Token != "" || recorder.input.SSH != nil || len(service.tests) != 0 {
		t.Fatalf("缺失pin仍测试或发放令牌: %v", err)
	}
}

type pinnedSSHFixture struct {
	address, fingerprint string
	auth                 atomic.Int32
	mu                   sync.Mutex
	commands             []string
}

func newPinnedSSHFixture(t *testing.T, mode string) *pinnedSSHFixture {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &pinnedSSHFixture{fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		fixture.auth.Add(1)
		if string(password) != "test-password" {
			return nil, errors.New("拒绝认证")
		}
		return nil, nil
	}}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture.address = listener.Addr().String()
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
				server, channels, requests, err := ssh.NewServerConn(connection, serverConfig)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for request := range channels {
					channel, requests, err := request.Accept()
					if err != nil {
						return
					}
					go func() {
						defer channel.Close()
						for request := range requests {
							if request.Type != "exec" {
								_ = request.Reply(false, nil)
								continue
							}
							var payload struct{ Command string }
							if ssh.Unmarshal(request.Payload, &payload) != nil {
								return
							}
							fixture.mu.Lock()
							fixture.commands = append(fixture.commands, payload.Command)
							fixture.mu.Unlock()
							_ = request.Reply(true, nil)
							status := uint32(0)
							switch payload.Command {
							case "uname -m":
								_, _ = io.WriteString(channel, "x86_64\n")
							case "id -u":
								if mode == "wrong-uid" {
									_, _ = io.WriteString(channel, "1000\n")
								} else {
									_, _ = io.WriteString(channel, "0\n")
								}
							case "sudo -n -- id -u":
								if mode == "password" || mode == "denied" {
									status = 1
								} else if mode == "wrong-uid" {
									_, _ = io.WriteString(channel, "1000\n")
								} else {
									_, _ = io.WriteString(channel, "0\n")
								}
							case "sudo -S -p '' -- id -u":
								var password []byte
								for len(password) < 100 {
									one := make([]byte, 1)
									if _, err := channel.Read(one); err != nil {
										break
									}
									password = append(password, one...)
									if one[0] == '\n' {
										break
									}
								}
								if string(password) != "test-password\n" || mode == "denied" {
									status = 1
								} else if mode == "wrong-uid" {
									_, _ = io.WriteString(channel, "1000\n")
								} else {
									_, _ = io.WriteString(channel, "0\n")
								}
							default:
								status = 1
							}
							_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
							return
						}
					}()
				}
			}()
		}
	}()
	return fixture
}

func TestExistingCredentialChangedKeyRejectsBeforeAuthenticationAndToken(t *testing.T) {
	for _, docker := range []bool{false, true} {
		t.Run(fmt.Sprint("docker=", docker), func(t *testing.T) {
			remote := newPinnedSSHFixture(t, "nopasswd")
			service, db, secrets := newHostTestService(t)
			service.docker = &recordingDockerProbe{fingerprint: "SHA256:" + strings.Repeat("A", 43)}
			address, port, _ := net.SplitHostPort(remote.address)
			input := hostTestInput()
			input.Address = address
			input.SSHPort, _ = strconv.Atoi(port)
			input.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilitySSH}
			if docker {
				input.CapabilityKinds = append(input.CapabilityKinds, model.HostCapabilityDocker)
			}
			created := createReuseFixture(t, service, input)
			service.docker = dockerengine.NewService(db, secrets, config.Runtime{ConnectTimeout: time.Second, RequestTimeout: 2 * time.Second})
			input.SSH = nil
			input.ReuseCredential = true
			tested, err := service.TestExisting(context.Background(), created.Host.ID, input)
			if err == nil || tested.Token != "" || remote.auth.Load() != 0 || len(service.tests) != 0 {
				t.Fatalf("变化key收到认证或产生令牌: err=%v auth=%d", err, remote.auth.Load())
			}
			remote.mu.Lock()
			defer remote.mu.Unlock()
			if len(remote.commands) != 0 {
				t.Fatal("指纹拒绝后仍执行远端命令")
			}
		})
	}
}

func TestSSHOnlyEnabledSudoIsActuallyVerifiedWithoutDocker(t *testing.T) {
	for _, mode := range []string{"nopasswd", "password", "denied", "wrong-uid", "disabled", "root"} {
		t.Run(mode, func(t *testing.T) {
			remote := newPinnedSSHFixture(t, mode)
			service, db, secrets := newHostTestService(t)
			service.docker = &recordingDockerProbe{fingerprint: remote.fingerprint}
			address, port, _ := net.SplitHostPort(remote.address)
			input := hostTestInput()
			input.Address = address
			input.SSHPort, _ = strconv.Atoi(port)
			input.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilitySSH}
			if mode == "root" {
				input.SSHUsername = "root"
			}
			created := createReuseFixture(t, service, input)
			service.docker = dockerengine.NewService(db, secrets, config.Runtime{ConnectTimeout: time.Second, RequestTimeout: 2 * time.Second})
			input.SSH = nil
			input.ReuseCredential = true
			useSudo := mode != "disabled"
			input.UseSudo = &useSudo
			tested, err := service.TestExisting(context.Background(), created.Host.ID, input)
			wantSuccess := mode != "denied" && mode != "wrong-uid"
			if (err == nil) != wantSuccess || (tested.Token != "") != wantSuccess {
				t.Fatalf("sudo验证结果错误: %v", err)
			}
			if !wantSuccess && !errors.Is(err, dockerengine.ErrSSHSudoDenied) {
				t.Fatalf("sudo拒绝没有安全边界: %v", err)
			}
			remote.mu.Lock()
			defer remote.mu.Unlock()
			joined := strings.Join(remote.commands, "\n")
			if strings.Contains(joined, "docker") || strings.Contains(joined, "sh -") || strings.Contains(joined, "test-password") {
				t.Fatal("SSH-only提权测试执行了Docker/脚本或把密码放入命令")
			}
			if mode == "disabled" && joined != "uname -m" {
				t.Fatal("未启用sudo仍尝试提权")
			}
			if mode == "root" && !strings.Contains(joined, "id -u") {
				t.Fatal("root未核验真实UID")
			}
			if mode == "password" && !strings.Contains(joined, "sudo -S -p '' -- id -u") {
				t.Fatal("未通过独立标准输入验证sudo密码")
			}
		})
	}
}
