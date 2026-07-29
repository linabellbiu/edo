package sshdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/hostcredential"
	"zrt/internal/model"
	"zrt/internal/secret"
	"zrt/internal/sshclient"
)

var (
	ErrHostUnavailable      = errors.New("命令发布主机不可用")
	ErrLocalExecUnsupported = errors.New("当前运行方式不支持本地终端执行")
	ErrInvalidScript        = errors.New("命令部署脚本配置无效")
	ErrCommandFailed        = errors.New("部署命令执行失败")
	ErrEnvironmentChanged   = errors.New("命令发布主机所属环境已变化")
)

var environmentKeyPattern = regexp.MustCompile(`^ZRT_[A-Z0-9_]{1,60}$`)

type Input struct {
	HostID           string
	EnvironmentID    string
	WorkingDirectory string
	Script           string
	Timeout          time.Duration
	Environment      map[string]string
	Stdout           io.Writer
	Stderr           io.Writer
}

type Result struct {
	ExitCode int
	Started  bool
}

type CommandError struct {
	ExitCode int
	cause    error
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("%s，退出码 %d", ErrCommandFailed.Error(), e.ExitCode)
}

func (e *CommandError) Unwrap() error { return e.cause }

func (e *CommandError) Is(target error) bool { return target == ErrCommandFailed }

type Service struct {
	db      *gorm.DB
	secrets *secret.Manager
	config  config.Runtime
}

func NewService(db *gorm.DB, secrets *secret.Manager, runtimeConfig config.Runtime) *Service {
	return &Service{db: db, secrets: secrets, config: runtimeConfig}
}

// RunHostDeploymentScript 只执行已经进入流水线快照的部署脚本。本地和远端都固定使用 sh -se，
// 脚本通过标准输入传递且不申请 PTY，不能被复用为浏览器交互终端或临时命令接口。
func (s *Service) RunHostDeploymentScript(ctx context.Context, input Input) (Result, error) {
	input.HostID = strings.TrimSpace(input.HostID)
	input.EnvironmentID = strings.TrimSpace(input.EnvironmentID)
	requestedWorkingDirectory := strings.TrimSpace(input.WorkingDirectory)
	input.WorkingDirectory = normalizeWorkingDirectory(input.WorkingDirectory)
	if s == nil || s.db == nil || input.HostID == "" || input.EnvironmentID == "" ||
		(requestedWorkingDirectory != "" && input.WorkingDirectory == "") ||
		strings.TrimSpace(input.Script) == "" || len(input.Script) > 256*1024 ||
		input.Timeout < 30*time.Second || input.Timeout > time.Hour {
		return Result{ExitCode: -1}, ErrInvalidScript
	}

	host, bundle, err := s.loadHost(ctx, input)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	if host.Mode == model.HostModeLocal {
		return runLocalDeploymentScript(ctx, input, s.config.DockerBuilderHost)
	}
	hostURL := (&url.URL{
		Scheme: "ssh", User: url.User(host.SSHUsername),
		Host: net.JoinHostPort(host.Address, strconv.Itoa(host.SSHPort)),
	}).String()
	connector, err := sshclient.NewConnector(hostURL, bundle, host.SSHHostKeyFingerprint, s.config.ConnectTimeout)
	if err != nil {
		return Result{ExitCode: -1}, ErrHostUnavailable
	}

	commandContext, cancel := context.WithTimeout(ctx, input.Timeout)
	defer cancel()
	client, err := connector.ConnectPinned(commandContext)
	if err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("%w: %v", ErrHostUnavailable, err)
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("%w: %v", ErrHostUnavailable, err)
	}
	defer session.Close()

	stdout, stderr := input.Stdout, input.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	session.Stdout, session.Stderr = stdout, stderr
	body, err := deploymentScriptInput(input)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	session.Stdin = strings.NewReader(body)
	if err := session.Start("sh -se"); err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("%w: %v", ErrHostUnavailable, err)
	}
	result := Result{ExitCode: -1, Started: true}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			result.ExitCode = 0
			return result, nil
		}
		var exitError *ssh.ExitError
		if errors.As(err, &exitError) {
			result.ExitCode = exitError.ExitStatus()
			return result, &CommandError{ExitCode: result.ExitCode, cause: err}
		}
		return result, fmt.Errorf("%w: %v", ErrCommandFailed, err)
	case <-commandContext.Done():
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		return result, commandContext.Err()
	}
}

func (s *Service) loadHost(ctx context.Context, input Input) (model.Host, sshclient.Bundle, error) {
	var host model.Host
	if err := s.db.WithContext(ctx).First(&host, "id = ? AND is_active = ?", input.HostID, true).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return host, sshclient.Bundle{}, ErrHostUnavailable
		}
		return host, sshclient.Bundle{}, fmt.Errorf("查询命令发布主机失败: %w", err)
	}
	var environment model.Environment
	if err := s.db.WithContext(ctx).Select("environments.id").
		Joins("JOIN environment_hosts AS membership ON membership.environment_id = environments.id").
		First(&environment,
			"environments.id = ? AND environments.is_active = ? AND membership.host_id = ?",
			input.EnvironmentID, true, host.ID,
		).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return host, sshclient.Bundle{}, ErrEnvironmentChanged
		}
		return host, sshclient.Bundle{}, fmt.Errorf("查询命令发布环境失败: %w", err)
	}
	capabilityKind := model.HostCapabilitySSH
	switch host.Mode {
	case model.HostModeLocal:
		if !host.IsBuiltin || host.ID != model.BuiltinLocalHostID {
			return host, sshclient.Bundle{}, ErrHostUnavailable
		}
		capabilityKind = model.HostCapabilityLocalExec
	case model.HostModeSSH:
		if s.secrets == nil {
			return host, sshclient.Bundle{}, ErrHostUnavailable
		}
	default:
		return host, sshclient.Bundle{}, ErrHostUnavailable
	}
	var capability model.HostCapability
	if err := s.db.WithContext(ctx).First(&capability,
		"host_id = ? AND kind = ? AND status = ?", host.ID, capabilityKind, model.HostCapabilityReady,
	).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return host, sshclient.Bundle{}, ErrHostUnavailable
		}
		return host, sshclient.Bundle{}, fmt.Errorf("查询主机命令能力失败: %w", err)
	}
	if host.Mode == model.HostModeLocal {
		return host, sshclient.Bundle{}, nil
	}
	if host.SSHCredentialCiphertext == "" || !sshclient.ValidFingerprint(host.SSHHostKeyFingerprint) {
		return host, sshclient.Bundle{}, ErrHostUnavailable
	}
	plaintext, err := s.secrets.Decrypt(host.SSHCredentialCiphertext, hostcredential.AAD(host.ID))
	if err != nil {
		return host, sshclient.Bundle{}, fmt.Errorf("解密 SSH 主机凭据失败: %w", err)
	}
	var bundle sshclient.Bundle
	if err := json.Unmarshal([]byte(plaintext), &bundle); err != nil {
		return host, sshclient.Bundle{}, fmt.Errorf("解析 SSH 主机凭据失败: %w", err)
	}
	if _, err := sshclient.AuthMethods(bundle); err != nil {
		return host, sshclient.Bundle{}, ErrHostUnavailable
	}
	return host, bundle, nil
}

func runLocalDeploymentScript(ctx context.Context, input Input, dockerHost string) (Result, error) {
	shell, err := localShellPath(runtime.GOOS, exec.LookPath)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	body, err := deploymentScriptInput(input)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	stdout, stderr := input.Stdout, input.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	commandContext, cancel := context.WithTimeout(ctx, input.Timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, shell, "-se")
	command.Stdin = strings.NewReader(body)
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = localCommandEnvironment(dockerHost)
	command.WaitDelay = 5 * time.Second
	if err := command.Start(); err != nil {
		return Result{ExitCode: -1}, ErrHostUnavailable
	}
	result := Result{ExitCode: -1, Started: true}
	err = command.Wait()
	if commandContext.Err() != nil {
		return result, commandContext.Err()
	}
	if err == nil {
		result.ExitCode = 0
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, &CommandError{ExitCode: result.ExitCode, cause: err}
	}
	return result, fmt.Errorf("%w: %v", ErrCommandFailed, err)
}

func localCommandEnvironment(dockerHost string) []string {
	keys := []string{"HOME", "LANG", "LC_ALL", "PATH", "TMPDIR", "TZ"}
	environment := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok && !strings.ContainsRune(value, '\x00') {
			environment = append(environment, key+"="+value)
		}
	}
	dockerHost = strings.TrimSpace(dockerHost)
	if dockerHost == "" {
		dockerHost = strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	}
	if dockerHost != "" && !strings.ContainsRune(dockerHost, '\x00') {
		environment = append(environment, "DOCKER_HOST="+dockerHost)
	}
	return environment
}

func localShellPath(goos string, lookPath func(string) (string, error)) (string, error) {
	if goos == "windows" || lookPath == nil {
		return "", ErrLocalExecUnsupported
	}
	shell, err := lookPath("sh")
	if err != nil || strings.TrimSpace(shell) == "" {
		return "", ErrLocalExecUnsupported
	}
	return shell, nil
}

func deploymentScriptInput(input Input) (string, error) {
	keys := make([]string, 0, len(input.Environment))
	for key, value := range input.Environment {
		if !environmentKeyPattern.MatchString(key) || len(value) > 4096 || strings.ContainsRune(value, '\x00') {
			return "", ErrInvalidScript
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var body strings.Builder
	for _, key := range keys {
		body.WriteString("export ")
		body.WriteString(key)
		body.WriteByte('=')
		body.WriteString(shellQuote(input.Environment[key]))
		body.WriteByte('\n')
	}
	if input.WorkingDirectory != "" {
		body.WriteString("cd ")
		body.WriteString(shellQuote(input.WorkingDirectory))
		body.WriteByte('\n')
	}
	body.WriteString(input.Script)
	return body.String(), nil
}

func normalizeWorkingDirectory(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) > 1024 || strings.ContainsAny(value, "\r\n\x00") || !strings.HasPrefix(value, "/") {
		return ""
	}
	cleaned := path.Clean(value)
	if cleaned != value {
		return ""
	}
	return cleaned
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
