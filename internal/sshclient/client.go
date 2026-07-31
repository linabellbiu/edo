package sshclient

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

var ErrInvalidConfiguration = errors.New("SSH 连接配置无效")

const maxPinnedConnectAttempts = 3

var pinnedConnectRetryDelays = [...]time.Duration{
	100 * time.Millisecond,
	300 * time.Millisecond,
}

type Bundle struct {
	PrivateKey   string `json:"private_key"`
	Passphrase   string `json:"passphrase,omitempty"`
	Password     string `json:"password,omitempty"`
	UseSudo      bool   `json:"use_sudo,omitempty"`
	SudoPassword string `json:"sudo_password,omitempty"`
}

type Connector struct {
	address     string
	username    string
	auth        []ssh.AuthMethod
	fingerprint string
	timeout     time.Duration
}

func NewConnector(host string, bundle Bundle, fingerprint string, timeout time.Duration) (*Connector, error) {
	parsed, err := url.Parse(host)
	if err != nil || parsed.Scheme != "ssh" || parsed.User == nil || parsed.User.Username() == "" || parsed.Hostname() == "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, ErrInvalidConfiguration
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		return nil, ErrInvalidConfiguration
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			return nil, ErrInvalidConfiguration
		}
	}
	auth, err := AuthMethods(bundle)
	if err != nil {
		return nil, err
	}
	address := parsed.Host
	if parsed.Port() == "" {
		address = net.JoinHostPort(parsed.Hostname(), "22")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint != "" && !ValidFingerprint(fingerprint) {
		return nil, ErrInvalidConfiguration
	}
	return &Connector{
		address: address, username: parsed.User.Username(), auth: auth,
		fingerprint: fingerprint, timeout: timeout,
	}, nil
}

func AuthMethods(bundle Bundle) ([]ssh.AuthMethod, error) {
	if (!bundle.UseSudo && bundle.SudoPassword != "") ||
		len(bundle.SudoPassword) > 4096 ||
		strings.ContainsAny(bundle.SudoPassword, "\r\n\x00") ||
		(bundle.UseSudo && bundle.SudoPassword == "" && strings.ContainsAny(bundle.Password, "\r\n\x00")) {
		return nil, ErrInvalidConfiguration
	}
	hasPassword := bundle.Password != ""
	hasPrivateKey := strings.TrimSpace(bundle.PrivateKey) != ""
	if hasPassword == hasPrivateKey {
		return nil, ErrInvalidConfiguration
	}
	if hasPassword {
		if bundle.Passphrase != "" {
			return nil, ErrInvalidConfiguration
		}
		return []ssh.AuthMethod{ssh.Password(bundle.Password)}, nil
	}
	signer, err := ParseSigner(bundle)
	if err != nil {
		return nil, err
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
}

func ParseSigner(bundle Bundle) (ssh.Signer, error) {
	privateKey := []byte(strings.TrimSpace(bundle.PrivateKey))
	if len(privateKey) == 0 {
		return nil, ErrInvalidConfiguration
	}
	var (
		signer ssh.Signer
		err    error
	)
	if bundle.Passphrase == "" {
		signer, err = ssh.ParsePrivateKey(privateKey)
	} else {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(privateKey, []byte(bundle.Passphrase))
	}
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	return signer, nil
}

func ValidFingerprint(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "SHA256:") || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	digest, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "SHA256:"))
	return err == nil && len(digest) == 32
}

func (c *Connector) Connect(ctx context.Context, callback ssh.HostKeyCallback) (*ssh.Client, error) {
	return c.connectOnce(ctx, callback)
}

func (c *Connector) connectOnce(ctx context.Context, callback ssh.HostKeyCallback) (*ssh.Client, error) {
	raw, err := (&net.Dialer{Timeout: c.timeout, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", c.address)
	if err != nil {
		return nil, err
	}
	handshakeDeadline := time.Now().Add(c.timeout)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(handshakeDeadline) {
		handshakeDeadline = deadline
	}
	_ = raw.SetDeadline(handshakeDeadline)
	config := &ssh.ClientConfig{
		User: c.username, Auth: c.auth, HostKeyCallback: callback, Timeout: c.timeout,
	}
	clientConnection, channels, requests, err := ssh.NewClientConn(raw, c.address, config)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return ssh.NewClient(clientConnection, channels, requests), nil
}

func (c *Connector) ConnectPinned(ctx context.Context) (*ssh.Client, error) {
	if !ValidFingerprint(c.fingerprint) {
		return nil, ErrInvalidConfiguration
	}
	callback := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if ssh.FingerprintSHA256(key) != c.fingerprint {
			return errors.New("SSH 主机指纹不匹配")
		}
		return nil
	}
	var lastErr error
	for attempt := 0; attempt < maxPinnedConnectAttempts; attempt++ {
		client, err := c.connectOnce(ctx, callback)
		if err == nil {
			return client, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt == maxPinnedConnectAttempts-1 || !retryablePinnedConnectError(err) {
			return nil, err
		}
		timer := time.NewTimer(pinnedConnectRetryDelays[attempt])
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

// 只重试已固定主机指纹且尚未创建 SSH Client 的瞬时传输中断。
// 认证、算法协商和主机指纹错误都不会命中这些条件。
func retryablePinnedConnectError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkError *net.OpError
	if !errors.As(err, &networkError) {
		return false
	}
	return networkError.Op == "read" || networkError.Op == "write"
}
