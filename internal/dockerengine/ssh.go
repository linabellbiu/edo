package dockerengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/distribution/reference"
	"golang.org/x/crypto/ssh"

	"zrt/internal/sshclient"
)

type SSHTestResult struct {
	Fingerprint   string `json:"fingerprint"`
	DockerVersion string `json:"docker_version"`
}

type SSHConnectionTestResult struct {
	Fingerprint string `json:"fingerprint"`
}

type sshConnector struct {
	inner *sshclient.Connector
}

type sshDockerDialer struct {
	connector *sshConnector
	bundle    SSHBundle
}

type dockerSSHCommandMode struct {
	prefix   string
	password string
}

func newSSHConnector(host string, bundle SSHBundle, fingerprint string, timeout time.Duration) (*sshConnector, error) {
	connector, err := sshclient.NewConnector(host, sshClientBundle(bundle), fingerprint, timeout)
	if err != nil {
		return nil, ErrInvalidSSH
	}
	return &sshConnector{inner: connector}, nil
}

func newSSHDockerDialer(host string, bundle SSHBundle, fingerprint string, timeout time.Duration) (*sshDockerDialer, error) {
	if !validSSHFingerprint(fingerprint) {
		return nil, ErrInvalidSSH
	}
	connector, err := newSSHConnector(host, bundle, fingerprint, timeout)
	if err != nil {
		return nil, err
	}
	return &sshDockerDialer{connector: connector, bundle: bundle}, nil
}

func sshAuthMethods(bundle SSHBundle) ([]ssh.AuthMethod, error) {
	methods, err := sshclient.AuthMethods(sshClientBundle(bundle))
	if err != nil {
		return nil, ErrInvalidSSH
	}
	return methods, nil
}

func parseSSHSigner(bundle SSHBundle) (ssh.Signer, error) {
	signer, err := sshclient.ParseSigner(sshClientBundle(bundle))
	if err != nil {
		return nil, ErrInvalidSSH
	}
	return signer, nil
}

func validSSHFingerprint(value string) bool {
	return sshclient.ValidFingerprint(value)
}

func (c *sshConnector) connect(ctx context.Context, callback ssh.HostKeyCallback) (*ssh.Client, error) {
	return c.inner.Connect(ctx, callback)
}

func (c *sshConnector) connectPinned(ctx context.Context) (*ssh.Client, error) {
	return c.inner.ConnectPinned(ctx)
}

func sshClientBundle(bundle SSHBundle) sshclient.Bundle {
	return sshclient.Bundle{
		PrivateKey: bundle.PrivateKey, Passphrase: bundle.Passphrase, Password: bundle.Password,
		UseSudo: bundle.UseSudo, SudoPassword: bundle.SudoPassword,
	}
}

func (s *Service) TestSSH(ctx context.Context, input Input) (SSHTestResult, error) {
	if input.SSH == nil {
		return SSHTestResult{}, ErrInvalidSSH
	}
	fingerprint := strings.TrimSpace(input.SSHHostKeyFingerprint)
	connector, err := newSSHConnector(strings.TrimSpace(input.Host), *input.SSH, fingerprint, s.config.ConnectTimeout)
	if err != nil {
		return SSHTestResult{}, err
	}
	testContext, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	defer cancel()
	client, err := connectForSSHTest(testContext, connector, &fingerprint)
	if err != nil {
		return SSHTestResult{}, fmt.Errorf("%w: %v", ErrSSHUnreachable, err)
	}
	defer client.Close()
	output := &limitedOutput{remaining: 256}
	commandError := &limitedOutput{remaining: 512}
	mode, err := resolveDockerSSHCommandMode(testContext, client, *input.SSH)
	if err != nil {
		return SSHTestResult{}, fmt.Errorf("%w: %v", ErrSSHDockerDenied, err)
	}
	command, commandInput := mode.prepare("docker version --format '{{.Server.Version}}'", nil)
	if err := runSSHCommandWithStreams(testContext, client, command, commandInput, output, commandError); err != nil {
		if detail := strings.TrimSpace(commandError.String()); detail != "" {
			return SSHTestResult{}, fmt.Errorf("%w: %v: %s", ErrSSHDockerDenied, err, detail)
		}
		return SSHTestResult{}, fmt.Errorf("%w: %v", ErrSSHDockerDenied, err)
	}
	dockerVersion := strings.TrimSpace(output.String())
	if !validSSHFingerprint(fingerprint) || dockerVersion == "" {
		return SSHTestResult{}, ErrSSHDockerDenied
	}
	return SSHTestResult{Fingerprint: fingerprint, DockerVersion: dockerVersion}, nil
}

// TestSSHConnection 只验证 SSH 登录并读取主机指纹，不开放任何宿主机命令。
// Docker 能力仍必须通过 TestSSH 的固定 docker version 命令单独验证。
func (s *Service) TestSSHConnection(ctx context.Context, input Input) (SSHConnectionTestResult, error) {
	if input.SSH == nil {
		return SSHConnectionTestResult{}, ErrInvalidSSH
	}
	fingerprint := strings.TrimSpace(input.SSHHostKeyFingerprint)
	connector, err := newSSHConnector(strings.TrimSpace(input.Host), *input.SSH, fingerprint, s.config.ConnectTimeout)
	if err != nil {
		return SSHConnectionTestResult{}, err
	}
	testContext, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	defer cancel()
	client, err := connectForSSHTest(testContext, connector, &fingerprint)
	if err != nil {
		return SSHConnectionTestResult{}, fmt.Errorf("%w: %v", ErrSSHUnreachable, err)
	}
	_ = client.Close()
	if !validSSHFingerprint(fingerprint) {
		return SSHConnectionTestResult{}, ErrSSHUnreachable
	}
	return SSHConnectionTestResult{Fingerprint: fingerprint}, nil
}

func connectForSSHTest(ctx context.Context, connector *sshConnector, fingerprint *string) (*ssh.Client, error) {
	if connector == nil || fingerprint == nil {
		return nil, ErrInvalidSSH
	}
	if *fingerprint != "" {
		return connector.connectPinned(ctx)
	}
	return connector.connect(ctx, func(_ string, _ net.Addr, key ssh.PublicKey) error {
		*fingerprint = ssh.FingerprintSHA256(key)
		return nil
	})
}

func (s *Service) pullImageWithSSH(ctx context.Context, endpointID, image string, registry RegistryAuth) (bool, error) {
	endpoint, err := s.Find(ctx, endpointID)
	if err != nil {
		return false, err
	}
	parsed, err := url.Parse(endpoint.Host)
	if err != nil || parsed.Scheme != "ssh" {
		return false, nil
	}
	host, bundle, fingerprint, err := s.sshConfiguration(ctx, endpoint)
	if err != nil {
		return true, err
	}
	connector, err := newSSHConnector(host, bundle, fingerprint, s.config.ConnectTimeout)
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
	mode, err := resolveDockerSSHCommandMode(ctx, client, bundle)
	if err != nil {
		return true, fmt.Errorf("检查远程 Docker sudo 权限失败: %w", err)
	}
	command, registryInput, err := dockerPullWithRegistryAuth(command, registry)
	if err != nil {
		return true, err
	}
	command, commandInput := mode.prepare(command, registryInput)
	if err := runSSHCommandWithInput(ctx, client, command, commandInput, io.Discard); err != nil {
		return true, fmt.Errorf("远程执行 docker pull 失败: %w", err)
	}
	return true, nil
}

func dockerPullWithRegistryAuth(pullCommand string, registry RegistryAuth) (string, io.Reader, error) {
	if strings.TrimSpace(registry.Credential) == "" {
		return pullCommand, nil, nil
	}
	host := strings.TrimSpace(registry.Host)
	if host == "" {
		parsed, err := url.Parse(strings.TrimSpace(registry.ServerAddress))
		if err != nil || parsed.Host == "" {
			return "", nil, errors.New("镜像仓库认证地址无效")
		}
		host = parsed.Host
	}
	script := "set -eu; umask 077; config=$(mktemp -d /tmp/zrt-docker-config.XXXXXX); " +
		"trap 'rm -rf \"$config\"' EXIT; export DOCKER_CONFIG=\"$config\"; "
	var input io.Reader
	if username := strings.TrimSpace(registry.Username); username != "" {
		script += "docker login --username " + shellQuote(username) + " --password-stdin " + shellQuote(host) + " >/dev/null; "
		input = strings.NewReader(registry.Credential + "\n")
	} else {
		type authEntry struct {
			IdentityToken string `json:"identitytoken"`
			Auth          string `json:"auth,omitempty"`
		}
		payload, err := json.Marshal(struct {
			Auths map[string]authEntry `json:"auths"`
		}{Auths: map[string]authEntry{
			host: {IdentityToken: registry.Credential},
		}})
		if err != nil {
			return "", nil, errors.New("编码镜像仓库认证信息失败")
		}
		// token 认证没有通用的 docker login 参数，以只存在于本次命令生命周期内的配置文件传入。
		script += "cat >\"$DOCKER_CONFIG/config.json\"; "
		input = bytes.NewReader(payload)
	}
	script += pullCommand
	return "sh -c " + shellQuote(script), input, nil
}

func (s *Service) loadImageToSSH(ctx context.Context, endpointID, image string, archive io.Reader) (string, error) {
	endpoint, err := s.Find(ctx, endpointID)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(endpoint.Host)
	if err != nil || parsed.Scheme != "ssh" {
		return "", errors.New("本地镜像只能传输到 Docker SSH 主机")
	}
	host, bundle, fingerprint, err := s.sshConfiguration(ctx, endpoint)
	if err != nil {
		return "", err
	}
	connector, err := newSSHConnector(host, bundle, fingerprint, s.config.ConnectTimeout)
	if err != nil {
		return "", err
	}
	return loadImageWithSSH(ctx, connector, bundle, image, archive)
}

func loadImageWithSSH(ctx context.Context, connector *sshConnector, bundle SSHBundle, image string, archive io.Reader) (string, error) {
	if connector == nil || archive == nil || !IsZRTLocalImage(image) {
		return "", errors.New("待加载的 Docker 镜像无效")
	}
	client, err := connector.connectPinned(ctx)
	if err != nil {
		return "", fmt.Errorf("连接 Docker SSH 主机失败: %w", err)
	}
	defer client.Close()
	mode, err := resolveDockerSSHCommandMode(ctx, client, bundle)
	if err != nil {
		return "", fmt.Errorf("检查远程 Docker sudo 权限失败: %w", err)
	}
	loadOutput := &limitedOutput{remaining: 4096}
	loadCommand, loadInput := mode.prepare("docker image load --quiet", archive)
	if err := runSSHCommandWithInput(ctx, client, loadCommand, loadInput, loadOutput); err != nil {
		return "", fmt.Errorf("通过 SSH 加载 Docker 镜像失败: %w", err)
	}
	targetImageID, err := parseDockerLoadImageID(loadOutput.String())
	if err != nil {
		return "", err
	}
	inspectOutput := &limitedOutput{remaining: 256}
	inspectCommand := "docker image inspect --format '{{.Id}}' " + shellQuote(targetImageID)
	inspectCommand, inspectInput := mode.prepare(inspectCommand, nil)
	if err := runSSHCommandWithInput(ctx, client, inspectCommand, inspectInput, inspectOutput); err != nil {
		return "", fmt.Errorf("校验 SSH 主机上的 Docker 镜像失败: %w", err)
	}
	if strings.TrimSpace(inspectOutput.String()) != targetImageID {
		return "", errors.New("Docker SSH 主机没有返回可验证的镜像 ID")
	}
	// ImageSave(sourceImageID) 生成不携带 RepoTag 的归档。目标端按固定 ID
	// 复验后再恢复展示用标签；返回值和实际部署始终使用 targetImageID。
	tagCommand := "docker image tag " + shellQuote(targetImageID) + " " + shellQuote(image)
	tagCommand, tagInput := mode.prepare(tagCommand, nil)
	if err := runSSHCommandWithInput(ctx, client, tagCommand, tagInput, io.Discard); err != nil {
		return "", fmt.Errorf("为 SSH 主机上的 Docker 镜像设置受控标签失败: %w", err)
	}
	return targetImageID, nil
}

func parseDockerLoadImageID(output string) (string, error) {
	const prefix = "Loaded image ID:"
	loadedImageID := ""
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		candidate := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if !IsValidImageID(candidate) || (loadedImageID != "" && candidate != loadedImageID) {
			return "", errors.New("Docker SSH 主机返回了无法确认的镜像加载结果")
		}
		loadedImageID = candidate
	}
	if loadedImageID == "" {
		return "", errors.New("Docker SSH 主机没有返回本次加载的镜像 ID")
	}
	return loadedImageID, nil
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

func resolveDockerSSHCommandMode(ctx context.Context, client *ssh.Client, bundle SSHBundle) (dockerSSHCommandMode, error) {
	if !bundle.UseSudo {
		return dockerSSHCommandMode{}, nil
	}
	probe := "sudo -n -- docker version --format '{{.Server.Version}}'"
	if err := runSSHCommand(ctx, client, probe, io.Discard); err == nil {
		return dockerSSHCommandMode{prefix: "sudo -n -- "}, nil
	}
	password := bundle.SudoPassword
	if password == "" {
		password = bundle.Password
	}
	if password == "" {
		return dockerSSHCommandMode{}, errors.New("远程用户未配置免密 sudo")
	}
	return dockerSSHCommandMode{prefix: "sudo -S -p '' -- ", password: password}, nil
}

func (mode dockerSSHCommandMode) prepare(command string, input io.Reader) (string, io.Reader) {
	if mode.prefix == "" {
		return command, input
	}
	if mode.password == "" {
		return mode.prefix + command, input
	}
	password := strings.NewReader(mode.password + "\n")
	if input == nil {
		return mode.prefix + command, password
	}
	return mode.prefix + command, io.MultiReader(password, input)
}

func runSSHCommand(ctx context.Context, client *ssh.Client, command string, output io.Writer) error {
	return runSSHCommandWithInput(ctx, client, command, nil, output)
}

func runSSHCommandWithInput(ctx context.Context, client *ssh.Client, command string, input io.Reader, output io.Writer) error {
	return runSSHCommandWithStreams(ctx, client, command, input, output, output)
}

func runSSHCommandWithStreams(
	ctx context.Context,
	client *ssh.Client,
	command string,
	input io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdout = stdout
	session.Stderr = stderr
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
	mode, err := resolveDockerSSHCommandMode(ctx, client, d.bundle)
	if err != nil {
		_ = client.Close()
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
	command, commandInput := mode.prepare("docker system dial-stdio", nil)
	if err := session.Start(command); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, err
	}
	if commandInput != nil {
		if _, err := io.Copy(stdin, commandInput); err != nil {
			_ = session.Close()
			_ = client.Close()
			return nil, err
		}
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
