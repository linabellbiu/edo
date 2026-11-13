package terminal

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// 仅为隔离测试转发固定 Docker API 字节流，不执行任何宿主机 Shell。
func terminalDockerSSHProxy(t *testing.T, dockerHost string) (string, string) {
	t.Helper()
	target, err := url.Parse(dockerHost)
	if err != nil || (target.Scheme != "unix" && target.Scheme != "tcp") {
		t.Fatal("SSH 隔离测试需要本地 Docker Unix 或 TCP 连接")
	}
	network, address := "tcp", target.Host
	if target.Scheme == "unix" {
		network, address = "unix", target.Path
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	configuration := &ssh.ServerConfig{PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if meta.User() != "fixture" || string(password) != "isolated-test" {
			return nil, errors.New("隔离 SSH 认证失败")
		}
		return nil, nil
	}}
	configuration.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	connections := make(map[net.Conn]struct{})
	var workers sync.WaitGroup
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		for connection := range connections {
			_ = connection.Close()
		}
		mu.Unlock()
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("隔离 SSH 字节流未完成回收")
		}
	})
	workers.Go(func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections[raw] = struct{}{}
			mu.Unlock()
			workers.Go(func() {
				defer func() { _ = raw.Close(); mu.Lock(); delete(connections, raw); mu.Unlock() }()
				connection, channels, requests, err := ssh.NewServerConn(raw, configuration)
				if err != nil {
					return
				}
				defer connection.Close()
				go ssh.DiscardRequests(requests)
				for request := range channels {
					if request.ChannelType() != "session" {
						_ = request.Reject(ssh.UnknownChannelType, "只支持隔离会话")
						continue
					}
					channel, events, err := request.Accept()
					if err != nil {
						return
					}
					workers.Go(func() {
						defer channel.Close()
						for event := range events {
							var command struct{ Command string }
							if event.Type != "exec" || ssh.Unmarshal(event.Payload, &command) != nil || command.Command != "docker system dial-stdio" {
								_ = event.Reply(false, nil)
								continue
							}
							socket, err := net.DialTimeout(network, address, 3*time.Second)
							if err != nil {
								_ = event.Reply(false, nil)
								return
							}
							defer socket.Close()
							_ = event.Reply(true, nil)
							copied := make(chan struct{})
							go func() { _, _ = io.Copy(socket, channel); _ = socket.Close(); close(copied) }()
							_, _ = io.Copy(channel, socket)
							_ = channel.Close()
							_ = socket.Close()
							<-copied
							return
						}
					})
				}
			})
		}
	})
	return listener.Addr().String(), ssh.FingerprintSHA256(signer.PublicKey())
}
