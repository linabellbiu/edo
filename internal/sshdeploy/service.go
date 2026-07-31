package sshdeploy

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/hostcredential"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/sshclient"
)

var (
	ErrHostUnavailable      = errors.New("命令发布主机不可用")
	ErrLocalExecUnsupported = errors.New("当前运行方式不支持本地终端执行")
	ErrInvalidScript        = errors.New("命令部署脚本配置无效")
	ErrCommandFailed        = errors.New("部署命令执行失败")
	ErrEnvironmentChanged   = errors.New("命令发布主机所属环境已变化")
)

var environmentKeyPattern = regexp.MustCompile(`^EDO_[A-Z0-9_]{1,60}$`)

type Input struct {
	HostID           string
	EnvironmentID    string
	WorkingDirectory string
	Script           string
	Timeout          time.Duration
	Environment      map[string]string
	Artifact         io.Reader
	ArtifactName     string
	ArtifactDigest   string
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
	if (input.Artifact == nil) != (strings.TrimSpace(input.ArtifactName) == "" && strings.TrimSpace(input.ArtifactDigest) == "") {
		return Result{ExitCode: -1}, ErrInvalidScript
	}

	host, bundle, err := s.loadHost(ctx, input)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	if host.Mode == model.HostModeLocal {
		if input.Artifact != nil {
			artifactPath, err := stageLocalArtifact(ctx, input)
			if err != nil {
				return Result{ExitCode: -1}, err
			}
			input.Environment = withArtifactEnvironment(input.Environment, artifactPath, input.ArtifactDigest)
		}
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
	if input.Artifact != nil {
		artifactPath, err := stageRemoteArtifact(commandContext, client, input)
		if err != nil {
			return Result{ExitCode: -1}, err
		}
		input.Environment = withArtifactEnvironment(input.Environment, artifactPath, input.ArtifactDigest)
	}
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

const maximumStagedArtifactBytes int64 = 1024*1024*1024 + 1

func stageLocalArtifact(ctx context.Context, input Input) (string, error) {
	destination, temporary, err := stagedArtifactPaths(input.WorkingDirectory, input.ArtifactName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return "", ErrHostUnavailable
	}
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", ErrHostUnavailable
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(
		io.MultiWriter(file, hasher),
		io.LimitReader(&contextArtifactReader{ctx: ctx, source: input.Artifact}, maximumStagedArtifactBytes),
	)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written >= maximumStagedArtifactBytes || !artifactDigestMatches(input.ArtifactDigest, hasher.Sum(nil)) {
		_ = os.Remove(temporary)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrInvalidScript
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return "", ErrHostUnavailable
	}
	return destination, nil
}

func stageRemoteArtifact(ctx context.Context, client *ssh.Client, input Input) (string, error) {
	destination, temporary, err := stagedArtifactPaths(input.WorkingDirectory, input.ArtifactName)
	if err != nil {
		return "", err
	}
	session, err := client.NewSession()
	if err != nil {
		return "", ErrHostUnavailable
	}
	hasher := sha256.New()
	limited := io.LimitReader(input.Artifact, maximumStagedArtifactBytes)
	counting := &countingReader{reader: io.TeeReader(limited, hasher)}
	session.Stdin = counting
	command := "umask 077; mkdir -p " + shellQuote(path.Dir(destination)) + "; cat > " + shellQuote(temporary)
	runDone := make(chan error, 1)
	go func() { runDone <- session.Run(command) }()
	var runErr error
	select {
	case runErr = <-runDone:
	case <-ctx.Done():
		_ = session.Close()
		_ = client.Close()
		return "", ctx.Err()
	}
	_ = session.Close()
	if runErr != nil || counting.total >= maximumStagedArtifactBytes || !artifactDigestMatches(input.ArtifactDigest, hasher.Sum(nil)) {
		cleanup, cleanupErr := client.NewSession()
		if cleanupErr == nil {
			_ = cleanup.Run("rm -f -- " + shellQuote(temporary))
			_ = cleanup.Close()
		}
		return "", ErrInvalidScript
	}
	commit, err := client.NewSession()
	if err != nil {
		return "", ErrHostUnavailable
	}
	defer commit.Close()
	done := make(chan error, 1)
	go func() { done <- commit.Run("mv -f -- " + shellQuote(temporary) + " " + shellQuote(destination)) }()
	select {
	case err := <-done:
		if err != nil {
			return "", ErrHostUnavailable
		}
		return destination, nil
	case <-ctx.Done():
		_ = client.Close()
		return "", ctx.Err()
	}
}

type countingReader struct {
	reader io.Reader
	total  int64
}

type contextArtifactReader struct {
	ctx    context.Context
	source io.Reader
}

func (r *contextArtifactReader) Read(payload []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.source.Read(payload)
	}
}

func (r *countingReader) Read(payload []byte) (int, error) {
	count, err := r.reader.Read(payload)
	r.total += int64(count)
	return count, err
}

func stagedArtifactPaths(workingDirectory, name string) (string, string, error) {
	workingDirectory = normalizeWorkingDirectory(workingDirectory)
	name = safeArtifactName(name)
	if workingDirectory == "" || name == "" {
		return "", "", ErrInvalidScript
	}
	destination := path.Join(workingDirectory, ".edo", "artifacts", name)
	temporary := destination + ".tmp-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	return destination, temporary, nil
}

func safeArtifactName(value string) string {
	value = path.Base(strings.TrimSpace(value))
	if value == "." || value == "" {
		return ""
	}
	var result strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			result.WriteRune(character)
		} else {
			result.WriteByte('_')
		}
		if result.Len() >= 200 {
			break
		}
	}
	return strings.Trim(result.String(), ".")
}

func artifactDigestMatches(expected string, actual []byte) bool {
	expected = strings.TrimSpace(expected)
	if !strings.HasPrefix(expected, "sha256:") || len(expected) != len("sha256:")+64 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(expected, "sha256:"))
	return err == nil && len(decoded) == sha256.Size && subtle.ConstantTimeCompare(decoded, actual) == 1
}

func withArtifactEnvironment(environment map[string]string, artifactPath, digest string) map[string]string {
	result := make(map[string]string, len(environment)+2)
	for key, value := range environment {
		result[key] = value
	}
	result["EDO_ARTIFACT_PATH"] = artifactPath
	result["EDO_ARTIFACT_DIGEST"] = digest
	return result
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
	shell, err := localShellPath(exec.LookPath)
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

func localShellPath(lookPath func(string) (string, error)) (string, error) {
	if lookPath == nil {
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
