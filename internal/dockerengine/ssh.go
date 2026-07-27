package dockerengine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/distribution/reference"
	"golang.org/x/crypto/ssh"
)

type SSHTestResult struct {
	Fingerprint   string `json:"fingerprint"`
	DockerVersion string `json:"docker_version"`
}

type sshConnector struct {
	address     string
	username    string
	auth        []ssh.AuthMethod
	fingerprint string
	timeout     time.Duration
}

type sshDockerDialer struct {
	connector *sshConnector
}

func newSSHConnector(host string, bundle SSHBundle, fingerprint string, timeout time.Duration) (*sshConnector, error) {
	parsed, err := url.Parse(host)
	if err != nil || parsed.Scheme != "ssh" || parsed.User == nil || parsed.User.Username() == "" || parsed.Hostname() == "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, ErrInvalidSSH
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		return nil, ErrInvalidSSH
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			return nil, ErrInvalidSSH
		}
	}
	auth, err := sshAuthMethods(bundle)
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
	if fingerprint != "" && !validSSHFingerprint(fingerprint) {
		return nil, ErrInvalidSSH
	}
	return &sshConnector{
		address: address, username: parsed.User.Username(), auth: auth,
		fingerprint: fingerprint, timeout: timeout,
	}, nil
}

func newSSHDockerDialer(host string, bundle SSHBundle, fingerprint string, timeout time.Duration) (*sshDockerDialer, error) {
	if !validSSHFingerprint(fingerprint) {
		return nil, ErrInvalidSSH
	}
	connector, err := newSSHConnector(host, bundle, fingerprint, timeout)
	if err != nil {
		return nil, err
	}
	return &sshDockerDialer{connector: connector}, nil
}

func sshAuthMethods(bundle SSHBundle) ([]ssh.AuthMethod, error) {
	hasPassword := bundle.Password != ""
	hasPrivateKey := strings.TrimSpace(bundle.PrivateKey) != ""
	if hasPassword == hasPrivateKey {
		return nil, ErrInvalidSSH
	}
	if hasPassword {
		if bundle.Passphrase != "" {
			return nil, ErrInvalidSSH
		}
		return []ssh.AuthMethod{ssh.Password(bundle.Password)}, nil
	}
	signer, err := parseSSHSigner(bundle)
	if err != nil {
		return nil, err
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
}

func parseSSHSigner(bundle SSHBundle) (ssh.Signer, error) {
	privateKey := []byte(strings.TrimSpace(bundle.PrivateKey))
	if len(privateKey) == 0 {
		return nil, ErrInvalidSSH
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
		return nil, ErrInvalidSSH
	}
	return signer, nil
}

func validSSHFingerprint(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "SHA256:") || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	digest, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "SHA256:"))
	return err == nil && len(digest) == 32
}

func (c *sshConnector) connect(ctx context.Context, callback ssh.HostKeyCallback) (*ssh.Client, error) {
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

func (c *sshConnector) connectPinned(ctx context.Context) (*ssh.Client, error) {
	if !validSSHFingerprint(c.fingerprint) {
		return nil, ErrInvalidSSH
	}
	return c.connect(ctx, func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if ssh.FingerprintSHA256(key) != c.fingerprint {
			return errors.New("SSH 主机指纹不匹配")
		}
		return nil
	})
}

func (s *Service) TestSSH(ctx context.Context, input Input) (SSHTestResult, error) {
	if input.SSH == nil {
		return SSHTestResult{}, ErrInvalidSSH
	}
	connector, err := newSSHConnector(strings.TrimSpace(input.Host), *input.SSH, "", s.config.ConnectTimeout)
	if err != nil {
		return SSHTestResult{}, err
	}
	testContext, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	defer cancel()
	fingerprint := ""
	client, err := connector.connect(testContext, func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fingerprint = ssh.FingerprintSHA256(key)
		return nil
	})
	if err != nil {
		return SSHTestResult{}, fmt.Errorf("%w: %v", ErrSSHUnreachable, err)
	}
	defer client.Close()
	output := &limitedOutput{remaining: 256}
	if err := runSSHCommand(testContext, client, "docker version --format '{{.Server.Version}}'", output); err != nil {
		return SSHTestResult{}, fmt.Errorf("%w: %v", ErrSSHUnreachable, err)
	}
	dockerVersion := strings.TrimSpace(output.String())
	if !validSSHFingerprint(fingerprint) || dockerVersion == "" {
		return SSHTestResult{}, ErrSSHUnreachable
	}
	return SSHTestResult{Fingerprint: fingerprint, DockerVersion: dockerVersion}, nil
}

func (s *Service) pullImageWithSSH(ctx context.Context, endpointID, image string) (bool, error) {
	endpoint, err := s.Find(ctx, endpointID)
	if err != nil {
		return false, err
	}
	parsed, err := url.Parse(endpoint.Host)
	if err != nil || parsed.Scheme != "ssh" {
		return false, nil
	}
	value, err := s.secrets.Decrypt(endpoint.SSHCredentialCiphertext, sshAAD(endpoint.ID))
	if err != nil {
		return true, fmt.Errorf("解密 Docker SSH 凭据失败: %w", err)
	}
	var bundle SSHBundle
	if err := json.Unmarshal([]byte(value), &bundle); err != nil {
		return true, fmt.Errorf("解析 Docker SSH 凭据失败: %w", err)
	}
	connector, err := newSSHConnector(endpoint.Host, bundle, endpoint.SSHHostKeyFingerprint, s.config.ConnectTimeout)
	if err != nil {
		return true, err
	}
	client, err := connector.connectPinned(ctx)
	if err != nil {
		return true, fmt.Errorf("连接 Docker SSH 主机失败: %w", err)
	}
	defer client.Close()
	command, err := dockerPullCommand(image)
	if err != nil {
		return true, err
	}
	if err := runSSHCommand(ctx, client, command, io.Discard); err != nil {
		return true, fmt.Errorf("远程执行 docker pull 失败: %w", err)
	}
	return true, nil
}

func (s *Service) loadImageToSSH(ctx context.Context, endpointID, image, expectedImageID string, archive io.Reader) error {
	endpoint, err := s.Find(ctx, endpointID)
	if err != nil {
		return err
	}
	parsed, err := url.Parse(endpoint.Host)
	if err != nil || parsed.Scheme != "ssh" {
		return errors.New("本地镜像只能传输到 Docker SSH 主机")
	}
	value, err := s.secrets.Decrypt(endpoint.SSHCredentialCiphertext, sshAAD(endpoint.ID))
	if err != nil {
		return fmt.Errorf("解密 Docker SSH 凭据失败: %w", err)
	}
	var bundle SSHBundle
	if err := json.Unmarshal([]byte(value), &bundle); err != nil {
		return fmt.Errorf("解析 Docker SSH 凭据失败: %w", err)
	}
	connector, err := newSSHConnector(endpoint.Host, bundle, endpoint.SSHHostKeyFingerprint, s.config.ConnectTimeout)
	if err != nil {
		return err
	}
	return loadImageWithSSH(ctx, connector, image, expectedImageID, archive)
}

func loadImageWithSSH(ctx context.Context, connector *sshConnector, image, expectedImageID string, archive io.Reader) error {
	if connector == nil || archive == nil || !IsZRTLocalImage(image) || !IsValidImageID(expectedImageID) {
		return errors.New("待加载的 Docker 镜像无效")
	}
	client, err := connector.connectPinned(ctx)
	if err != nil {
		return fmt.Errorf("连接 Docker SSH 主机失败: %w", err)
	}
	defer client.Close()
	loadOutput := &limitedOutput{remaining: 1024}
	if err := runSSHCommandWithInput(ctx, client, "docker image load --quiet", archive, loadOutput); err != nil {
		return fmt.Errorf("通过 SSH 加载 Docker 镜像失败: %w", err)
	}
	inspectOutput := &limitedOutput{remaining: 256}
	inspectCommand := "docker image inspect --format '{{.Id}}' " + shellQuote(image)
	if err := runSSHCommand(ctx, client, inspectCommand, inspectOutput); err != nil {
		return fmt.Errorf("校验 SSH 主机上的 Docker 镜像失败: %w", err)
	}
	if strings.TrimSpace(inspectOutput.String()) != expectedImageID {
		return errors.New("SSH 主机加载的镜像与 Docker-in-Docker 构建结果不一致")
	}
	return nil
}

func dockerPullCommand(image string) (string, error) {
	named, err := reference.ParseNormalizedNamed(strings.TrimSpace(image))
	if err != nil {
		return "", errors.New("Docker 镜像地址无效")
	}
	return "docker pull --quiet " + shellQuote(reference.FamiliarString(named)), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func runSSHCommand(ctx context.Context, client *ssh.Client, command string, output io.Writer) error {
	return runSSHCommandWithInput(ctx, client, command, nil, output)
}

func runSSHCommandWithInput(ctx context.Context, client *ssh.Client, command string, input io.Reader, output io.Writer) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdout = output
	session.Stderr = output
	if input != nil {
		session.Stdin = input
	}
	if err := session.Start(command); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = client.Close()
		<-done
		return ctx.Err()
	}
}

func (d *sshDockerDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	client, err := d.connector.connectPinned(ctx)
	if err != nil {
		return nil, err
	}
	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, err
	}
	session.Stderr = io.Discard
	if err := session.Start("docker system dial-stdio"); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, err
	}
	return &sshDockerConn{client: client, session: session, reader: stdout, writer: stdin}, nil
}

type sshDockerConn struct {
	client  *ssh.Client
	session *ssh.Session
	reader  io.Reader
	writer  io.WriteCloser
	once    sync.Once
}

func (c *sshDockerConn) Read(buffer []byte) (int, error)  { return c.reader.Read(buffer) }
func (c *sshDockerConn) Write(buffer []byte) (int, error) { return c.writer.Write(buffer) }

func (c *sshDockerConn) Close() error {
	var closeErr error
	c.once.Do(func() {
		_ = c.writer.Close()
		_ = c.session.Close()
		closeErr = c.client.Close()
	})
	return closeErr
}

func (c *sshDockerConn) LocalAddr() net.Addr              { return c.client.LocalAddr() }
func (c *sshDockerConn) RemoteAddr() net.Addr             { return c.client.RemoteAddr() }
func (c *sshDockerConn) SetDeadline(time.Time) error      { return nil }
func (c *sshDockerConn) SetReadDeadline(time.Time) error  { return nil }
func (c *sshDockerConn) SetWriteDeadline(time.Time) error { return nil }

type limitedOutput struct {
	buffer    bytes.Buffer
	remaining int
}

func (w *limitedOutput) Write(value []byte) (int, error) {
	written := len(value)
	if w.remaining > 0 {
		chunk := value
		if len(chunk) > w.remaining {
			chunk = chunk[:w.remaining]
		}
		_, _ = w.buffer.Write(chunk)
		w.remaining -= len(chunk)
	}
	return written, nil
}

func (w *limitedOutput) String() string { return w.buffer.String() }
