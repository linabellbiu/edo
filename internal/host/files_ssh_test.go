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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"edo/internal/dockerengine"
	"edo/internal/hostcredential"
	"edo/internal/model"
	"edo/internal/terminal"
	"edo/internal/terminalfiles"
)

type hostInteractiveSSHFixture struct {
	address, fingerprint string
	auth                 atomic.Int32
	commands             chan string
	pty                  chan terminal.Size
	resizes              chan terminal.Size
	mu                   sync.Mutex
	connections          map[net.Conn]bool
}

func newHostInteractiveSSHFixture(t *testing.T) *hostInteractiveSSHFixture {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &hostInteractiveSSHFixture{fingerprint: ssh.FingerprintSHA256(signer.PublicKey()), commands: make(chan string, 16), pty: make(chan terminal.Size, 16), resizes: make(chan terminal.Size, 16), connections: map[net.Conn]bool{}}
	config := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		fixture.auth.Add(1)
		if string(password) != "isolated-test-password" {
			return nil, errors.New("测试认证失败")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture.address = listener.Addr().String()
	handlers := sftp.InMemHandler()
	t.Cleanup(func() {
		_ = listener.Close()
		fixture.mu.Lock()
		for connection := range fixture.connections {
			_ = connection.Close()
		}
		fixture.mu.Unlock()
	})
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			fixture.mu.Lock()
			fixture.connections[connection] = true
			fixture.mu.Unlock()
			go func() {
				defer func() {
					_ = connection.Close()
					fixture.mu.Lock()
					delete(fixture.connections, connection)
					fixture.mu.Unlock()
				}()
				server, channels, requests, err := ssh.NewServerConn(connection, config)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						_ = incoming.Reject(ssh.UnknownChannelType, "仅支持隔离测试会话")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					go func() {
						defer channel.Close()
						for request := range requests {
							switch request.Type {
							case "subsystem":
								var payload struct{ Name string }
								if ssh.Unmarshal(request.Payload, &payload) != nil || payload.Name != "sftp" {
									_ = request.Reply(false, nil)
									return
								}
								_ = request.Reply(true, nil)
								requestServer := sftp.NewRequestServer(channel, handlers)
								defer requestServer.Close()
								_ = requestServer.Serve()
								return
							case "pty-req":
								var payload struct {
									Term                         string
									Columns, Rows, Width, Height uint32
									Modes                        string
								}
								if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
									_ = request.Reply(false, nil)
									return
								}
								fixture.pty <- terminal.Size{Columns: uint16(payload.Columns), Rows: uint16(payload.Rows)}
								_ = request.Reply(true, nil)
							case "window-change":
								var payload struct{ Columns, Rows, Width, Height uint32 }
								if err := ssh.Unmarshal(request.Payload, &payload); err == nil {
									fixture.resizes <- terminal.Size{Columns: uint16(payload.Columns), Rows: uint16(payload.Rows)}
								}
							case "exec":
								var payload struct{ Command string }
								if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
									_ = request.Reply(false, nil)
									return
								}
								fixture.commands <- payload.Command
								_ = request.Reply(true, nil)
								go func() { _, _ = io.Copy(channel, channel) }()
							case "signal":
								return
							default:
								_ = request.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	return fixture
}

func remoteHostFilesFixture(t *testing.T) (*Service, context.Context, string, *hostInteractiveSSHFixture) {
	t.Helper()
	service, db, ctx, id := hostInteractionFixture(t, model.HostModeSSH)
	server := newHostInteractiveSSHFixture(t)
	address, port, err := net.SplitHostPort(server.address)
	if err != nil {
		t.Fatal(err)
	}
	sshPort, _ := strconv.Atoi(port)
	plain, _ := json.Marshal(dockerengine.SSHBundle{Password: "isolated-test-password", UseSudo: true, SudoPassword: "must-not-be-used"})
	ciphertext, err := service.secrets.Encrypt(string(plain), hostcredential.AAD(id))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Host{}).Where("uuid = ?", id).Updates(map[string]any{"address": address, "ssh_port": sshPort, "ssh_username": "isolated-user", "ssh_host_key_fingerprint": server.fingerprint, "ssh_credential_ciphertext": ciphertext}).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	t.Cleanup(cancel)
	return service, ctx, id, server
}

func (f *hostInteractiveSSHFixture) adminClient(t *testing.T) *sftp.Client {
	t.Helper()
	sshClient, err := ssh.Dial("tcp", f.address, &ssh.ClientConfig{User: "isolated-user", Auth: []ssh.AuthMethod{ssh.Password("isolated-test-password")}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if ssh.FingerprintSHA256(key) != f.fingerprint {
			return errors.New("隔离夹具身份不符")
		}
		return nil
	}, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sshClient.Close(); _ = client.Close() })
	return client
}

func TestHostTerminalRealIsolatedSSHPtyInputResizeAndNoSudo(t *testing.T) {
	service, ctx, id, server := remoteHostFilesFixture(t)
	session, err := service.OpenTerminal(ctx, id, "sh", terminal.Size{Columns: 90, Rows: 25})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if command := <-server.commands; command != "/bin/sh -i" || strings.Contains(command, "sudo") || strings.Contains(command, "must-not-be-used") {
		t.Fatalf("未使用固定非提权交互入口: %q", command)
	}
	if size := <-server.pty; size.Columns != 90 || size.Rows != 25 {
		t.Fatalf("SSH PTY 尺寸错误: %+v", size)
	}
	if err := session.Resize(ctx, terminal.Size{Columns: 110, Rows: 40}); err != nil {
		t.Fatal(err)
	}
	select {
	case size := <-server.resizes:
		if size.Columns != 110 || size.Rows != 40 {
			t.Fatalf("SSH resize 错误: %+v", size)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSH resize 超时")
	}
	if _, err := session.Write([]byte("isolated-input\n")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len("isolated-input\n"))
	if _, err := io.ReadFull(session, buffer); err != nil || string(buffer) != "isolated-input\n" {
		t.Fatalf("SSH 双向数据错误: %q %v", buffer, err)
	}
}

func TestHostFilesRealSFTPAtomicReadUploadEditAndStrictPin(t *testing.T) {
	service, ctx, id, server := remoteHostFilesFixture(t)
	admin := server.adminClient(t)
	if err := admin.Mkdir("/workspace"); err != nil {
		t.Fatal(err)
	}
	backend, err := service.OpenFiles(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	location := "/workspace/中文 空格\n文件.txt"
	if _, err := backend.Put(ctx, location, strings.NewReader("original"), 8, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Put(ctx, location, strings.NewReader("candidate"), 9, hostTestDigest("wrong")); !errors.Is(err, terminalfiles.ErrConflict) {
		t.Fatalf("SFTP 旧版本可覆盖: %v", err)
	}
	if _, err := backend.Put(ctx, location, strings.NewReader("candidate"), 9, hostTestDigest("original")); err != nil {
		t.Fatal(err)
	}
	reader, metadata, err := backend.Open(ctx, location)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(data) != "candidate" || metadata.Size != 9 {
		t.Fatalf("SFTP 内容错误: %q %+v %v", data, metadata, err)
	}
	directory, err := backend.List(ctx, "/workspace")
	if err != nil || len(directory.Entries) != 1 || directory.Entries[0].Name != "中文 空格\n文件.txt" {
		t.Fatalf("SFTP 单层目录错误: %+v %v", directory, err)
	}
	if err := admin.Symlink(location, "/workspace/link"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := backend.Open(ctx, "/workspace/link"); !errors.Is(err, terminalfiles.ErrNotRegular) {
		t.Fatalf("SFTP 符号链接被当成普通文件: %v", err)
	}
	before := server.auth.Load()
	if err := service.db.Model(&model.Host{}).Where("uuid = ?", id).Update("ssh_host_key_fingerprint", "SHA256:"+strings.Repeat("A", 43)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenFiles(ctx, id); err == nil || server.auth.Load() != before {
		t.Fatalf("SFTP 指纹改变后仍认证: %v", err)
	}
}

func TestHostFilesSFTPDirectoryBoundedAndBudgetDoesNotAffectNewDownload(t *testing.T) {
	service, ctx, id, server := remoteHostFilesFixture(t)
	admin := server.adminClient(t)
	for _, name := range []string{"/normal", "/large"} {
		if err := admin.Mkdir(name); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < terminalfiles.MaxEntries+2; index++ {
		for _, name := range []string{fmt.Sprintf("/normal/%04d", index), fmt.Sprintf("/large/%04d-%s", index, strings.Repeat("x", 230))} {
			file, err := admin.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	backend, err := service.OpenFiles(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := backend.List(ctx, "/normal")
	if err != nil || len(directory.Entries) != terminalfiles.MaxEntries || !directory.Truncated {
		t.Fatalf("SFTP 未限制目录条数: %d %v %v", len(directory.Entries), directory.Truncated, err)
	}
	_ = backend.Close()
	backend, err = service.OpenFiles(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	directory, err = backend.List(ctx, "/large")
	if err == nil && (!directory.Truncated || len(directory.Entries) > terminalfiles.MaxEntries) {
		t.Fatalf("SFTP 大名称目录未明确截断: %d %v", len(directory.Entries), directory.Truncated)
	}
	if err != nil && !errors.Is(err, terminalfiles.ErrTooLarge) {
		t.Fatalf("目录预算应返回稳定超限: %v", err)
	}
	_ = backend.Close()
	backend, err = service.OpenFiles(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	if _, err := backend.Put(ctx, "/download.bin", io.LimitReader(hostZeroReader{}, 512*1024), 512*1024, ""); err != nil {
		t.Fatalf("目录预算污染新传输: %v", err)
	}
	reader, _, err := backend.Open(ctx, "/download.bin")
	if err != nil {
		t.Fatal(err)
	}
	count, err := io.Copy(io.Discard, reader)
	_ = reader.Close()
	if err != nil || count != 512*1024 {
		t.Fatalf("SFTP 下载未恢复正常预算: %d %v", count, err)
	}
}

func TestHostFilesSFTPCancelRemovesOnlyOwnTemporaryFile(t *testing.T) {
	service, ctx, id, server := remoteHostFilesFixture(t)
	admin := server.adminClient(t)
	if err := admin.Mkdir("/workspace"); err != nil {
		t.Fatal(err)
	}
	backend, err := service.OpenFiles(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	canceled, cancel := context.WithCancel(ctx)
	input := &hostMutationReader{input: strings.NewReader("candidate"), onRead: cancel}
	if _, err := backend.Put(canceled, "/workspace/canceled.txt", input, 9, ""); err == nil {
		t.Fatal("取消后的 SFTP 写入仍成功")
	}
	entries, err := admin.ReadDir("/workspace")
	if err != nil || len(entries) != 0 {
		t.Fatalf("取消后残留目标或临时文件: %v %v", entries, err)
	}
}

func TestHostFilesSFTPNeverOverwritesConcurrentCreate(t *testing.T) {
	service, ctx, id, server := remoteHostFilesFixture(t)
	admin := server.adminClient(t)
	backend, err := service.OpenFiles(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	input := &hostMutationReader{input: strings.NewReader("candidate"), onRead: func() {
		file, err := admin.Create("/concurrent.txt")
		if err == nil {
			_, err = file.Write([]byte("external"))
			_ = file.Close()
		}
		if err != nil {
			t.Error(err)
		}
	}}
	if _, err := backend.Put(ctx, "/concurrent.txt", input, 9, ""); !errors.Is(err, terminalfiles.ErrExists) {
		t.Fatalf("SFTP 原子新建未拒绝并发文件: %v", err)
	}
	file, err := admin.Open("/concurrent.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "external" {
		t.Fatalf("SFTP 并发新建被覆盖: %q %v", data, err)
	}
}

func TestHostTerminalSSHCancelClosesLiveConnection(t *testing.T) {
	service, ctx, id, _ := remoteHostFilesFixture(t)
	ctx, cancel := context.WithCancel(ctx)
	session, err := service.OpenTerminal(ctx, id, "sh", terminal.Size{Columns: 80, Rows: 24})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer session.Close()
	done := make(chan error, 1)
	go func() { _, err := session.Read(make([]byte, 32)); done <- err }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("取消后 SSH 输出未结束")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消未中断 SSH 终端")
	}
}
