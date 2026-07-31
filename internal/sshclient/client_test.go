package sshclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestConnectPinnedRetriesTransientHandshakeEOF(t *testing.T) {
	address, fingerprint, accepted := startConnectorTestServer(t, "secret", true)
	connector, err := NewConnector(
		"ssh://deploy@"+address,
		Bundle{Password: "secret"},
		fingerprint,
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := connector.ConnectPinned(ctx)
	if err != nil {
		t.Fatalf("瞬时 SSH 握手 EOF 后没有重连成功: %v", err)
	}
	_ = client.Close()
	if attempts := accepted.Load(); attempts != 2 {
		t.Fatalf("SSH 握手重连次数错误: %d", attempts)
	}
}

func TestConnectPinnedDoesNotRetryAuthenticationFailure(t *testing.T) {
	address, fingerprint, accepted := startConnectorTestServer(t, "secret", false)
	connector, err := NewConnector(
		"ssh://deploy@"+address,
		Bundle{Password: "wrong-password"},
		fingerprint,
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connector.ConnectPinned(context.Background()); err == nil {
		t.Fatal("错误 SSH 密码没有被拒绝")
	}
	if attempts := accepted.Load(); attempts != 1 {
		t.Fatalf("认证失败被错误重试: %d", attempts)
	}
}

func TestConnectPinnedDoesNotRetryHostKeyMismatch(t *testing.T) {
	address, _, accepted := startConnectorTestServer(t, "secret", false)
	_, otherPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherSigner, err := ssh.NewSignerFromKey(otherPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	connector, err := NewConnector(
		"ssh://deploy@"+address,
		Bundle{Password: "secret"},
		ssh.FingerprintSHA256(otherSigner.PublicKey()),
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connector.ConnectPinned(context.Background()); err == nil {
		t.Fatal("错误 SSH 主机指纹没有被拒绝")
	}
	if attempts := accepted.Load(); attempts != 1 {
		t.Fatalf("主机指纹错误被错误重试: %d", attempts)
	}
}

func TestConnectPinnedStopsAfterTransientHandshakeEOFLimit(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			accepted.Add(1)
			_ = connection.Close()
		}
	}()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	connector, err := NewConnector(
		"ssh://deploy@"+listener.Addr().String(),
		Bundle{Password: "secret"},
		ssh.FingerprintSHA256(signer.PublicKey()),
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = connector.ConnectPinned(context.Background())
	if err == nil || !retryablePinnedConnectError(err) {
		t.Fatalf("重试耗尽后没有保留原始瞬时传输错误: %v", err)
	}
	if attempts := accepted.Load(); attempts != maxPinnedConnectAttempts {
		t.Fatalf("SSH 握手重试没有按上限停止: %d", attempts)
	}
}

func startConnectorTestServer(
	t *testing.T,
	password string,
	closeFirst bool,
) (string, string, *atomic.Int32) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			attempt := accepted.Add(1)
			if closeFirst && attempt == 1 {
				_ = connection.Close()
				continue
			}
			go serveConnectorTestConnection(connection, serverConfig)
		}
	}()
	return listener.Addr().String(), ssh.FingerprintSHA256(signer.PublicKey()), &accepted
}

func serveConnectorTestConnection(connection net.Conn, config *ssh.ServerConfig) {
	serverConnection, channels, requests, err := ssh.NewServerConn(connection, config)
	if err != nil {
		_ = connection.Close()
		return
	}
	defer serverConnection.Close()
	go ssh.DiscardRequests(requests)
	for channel := range channels {
		_ = channel.Reject(ssh.Prohibited, "测试服务不接受通道")
	}
}
