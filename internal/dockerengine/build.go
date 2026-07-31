package dockerengine

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	containerderrdefs "github.com/containerd/errdefs"
	distributionreference "github.com/distribution/reference"
	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

const maximumBuildContextSize int64 = 1024 * 1024 * 1024

type buildExecutionError struct {
	cause     error
	retryable bool
}

func (e *buildExecutionError) Error() string { return e.cause.Error() }
func (e *buildExecutionError) Unwrap() error { return e.cause }

// IsRetryableBuildError 只识别 Docker/BuildKit 边界产生的类型化临时故障。
// 业务层不解析错误文案，Dockerfile 或参数错误仍会立即终止。
func IsRetryableBuildError(err error) bool {
	var buildError *buildExecutionError
	if errors.As(err, &buildError) {
		return buildError.retryable
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		containerderrdefs.IsUnavailable(err) || containerderrdefs.IsResourceExhausted(err) ||
		containerderrdefs.IsDeadlineExceeded(err) || containerderrdefs.IsAborted(err) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

var transientRegistryStatusPattern = regexp.MustCompile(`(?i)(?:http(?:/\d(?:\.\d)?)?|status(?:\s+code)?|response)[^\r\n]{0,80}\b(?:429|5\d\d)\b|\b(?:429|5\d\d)\b[^\r\n]{0,80}(?:too many requests|internal server error|bad gateway|service unavailable|gateway timeout)`)
var transientRegistryNetworkPattern = regexp.MustCompile(`(?i)(?:failed to (?:do request|fetch(?: anonymous)? token|authorize)|(?:head|get|put|post) "https?://)[^\r\n]{0,512}(?:\bEOF\b|TLS handshake timeout|i/o timeout|connection reset by peer|connection refused|temporary failure in name resolution|no such host|context deadline exceeded)`)
var dockerBuildArgNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type RegistryAuth struct {
	ServerAddress string
	Host          string
	Username      string
	Credential    string
}

type BuildOptions struct {
	Pull         bool
	CacheEnabled bool
	TargetStage  string
	Platform     string
	BuildArgs    map[string]string
	Labels       map[string]string
}

func (s *Service) BuildAndPush(
	ctx context.Context,
	contextDirectory, dockerfile, image string,
	registry RegistryAuth,
	timeout time.Duration,
	output io.Writer,
) (string, error) {
	return s.BuildAndPushWithOptions(
		ctx, contextDirectory, dockerfile, image, registry,
		defaultBuildOptions(), timeout, output,
	)
}

// BuildAndPushWithOptions 在当前构建运行时中按指定选项构建镜像并推送到镜像仓库。
func (s *Service) BuildAndPushWithOptions(
	ctx context.Context,
	contextDirectory, dockerfile, image string,
	registry RegistryAuth,
	options BuildOptions,
	timeout time.Duration,
	output io.Writer,
) (string, error) {
	apiClient, err := s.BuilderClient()
	if err != nil {
		return "", err
	}
	defer apiClient.Close()
	buildContext, err := createBuildContext(contextDirectory, dockerfile)
	if err != nil {
		return "", err
	}
	defer func() {
		name := buildContext.Name()
		_ = buildContext.Close()
		_ = os.Remove(name)
	}()

	authConfig := registryAuthConfig(registry)
	encodedAuth, err := encodeRegistryAuth(authConfig)
	if err != nil {
		return "", err
	}
	buildContextTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := s.runBuildx(buildContextTimeout, buildContext, dockerfile, image, registry, options, output); err != nil {
		return "", err
	}

	push, err := apiClient.ImagePush(buildContextTimeout, image, client.ImagePushOptions{RegistryAuth: encodedAuth})
	if err != nil {
		return "", fmt.Errorf("提交 Docker 镜像推送失败: %w", err)
	}
	if err := push.Wait(buildContextTimeout); err != nil {
		return "", fmt.Errorf("等待 Docker 镜像推送完成失败: %w", err)
	}
	inspect, err := apiClient.ImageInspect(buildContextTimeout, image)
	if err != nil {
		return "", fmt.Errorf("读取已推送镜像摘要失败: %w", err)
	}
	digest := matchingRepoDigest(image, inspect.RepoDigests)
	if digest == "" {
		return "", errors.New("镜像仓库没有返回可验证的镜像摘要")
	}
	return digest, nil
}

func matchingRepoDigest(image string, repoDigests []string) string {
	expected, err := distributionreference.ParseNormalizedNamed(strings.TrimSpace(image))
	if err != nil {
		return ""
	}
	expectedRepository := distributionreference.TrimNamed(expected).String()
	for _, value := range repoDigests {
		candidate, err := distributionreference.ParseNormalizedNamed(strings.TrimSpace(value))
		if err != nil || distributionreference.TrimNamed(candidate).String() != expectedRepository {
			continue
		}
		digested, ok := candidate.(distributionreference.Digested)
		if ok && digested.Digest().Algorithm() == "sha256" {
			return value
		}
	}
	return ""
}

// BuildLocal 在当前构建运行时中构建镜像，并返回内容寻址的镜像 ID。
func (s *Service) BuildLocal(
	ctx context.Context,
	contextDirectory, dockerfile, image string,
	timeout time.Duration,
	output io.Writer,
) (string, error) {
	return s.BuildLocalWithOptions(
		ctx, contextDirectory, dockerfile, image,
		defaultBuildOptions(), timeout, output,
	)
}

// BuildLocalWithOptions 在当前构建运行时中按指定选项构建镜像，并返回内容寻址的镜像 ID。
func (s *Service) BuildLocalWithOptions(
	ctx context.Context,
	contextDirectory, dockerfile, image string,
	options BuildOptions,
	timeout time.Duration,
	output io.Writer,
) (string, error) {
	if !IsEDOLocalImage(image) {
		return "", errors.New("本地构建镜像名称无效")
	}
	apiClient, err := s.BuilderClient()
	if err != nil {
		return "", err
	}
	defer apiClient.Close()
	buildContext, err := createBuildContext(contextDirectory, dockerfile)
	if err != nil {
		return "", err
	}
	defer func() {
		name := buildContext.Name()
		_ = buildContext.Close()
		_ = os.Remove(name)
	}()

	buildContextTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := s.runBuildx(buildContextTimeout, buildContext, dockerfile, image, RegistryAuth{}, options, output); err != nil {
		return "", err
	}
	inspect, err := apiClient.ImageInspect(buildContextTimeout, image)
	if err != nil {
		return "", fmt.Errorf("读取本地构建镜像失败: %w", err)
	}
	if !IsValidImageID(inspect.ID) {
		return "", errors.New("Docker 没有返回可验证的本地镜像 ID")
	}
	return inspect.ID, nil
}

// TransferImageToSSH 将构建运行时中的镜像以 docker save 流传给目标 SSH 主机的 docker load。
// 导出前校验源镜像 ID，加载后返回目标 Docker daemon 分配的镜像 ID，供部署前再次校验。
// 不同 Docker 版本可能会规范化镜像配置中的空字段，因此两端镜像 ID 不保证相同。
func (s *Service) TransferImageToSSH(
	ctx context.Context,
	endpointID, image, sourceImageID string,
	timeout time.Duration,
) (string, error) {
	if !IsEDOLocalImage(image) || !IsValidImageID(sourceImageID) {
		return "", errors.New("待传输的本地 Docker 镜像无效")
	}
	apiClient, err := s.BuilderClient()
	if err != nil {
		return "", err
	}
	defer apiClient.Close()
	transferContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	archive, err := exportImageByID(transferContext, apiClient, sourceImageID)
	if err != nil {
		return "", err
	}
	defer archive.Close()
	targetImageID, err := s.loadImageToSSH(transferContext, endpointID, image, archive)
	if err != nil {
		return "", err
	}
	return targetImageID, nil
}

type imageArchiveExporter interface {
	ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error)
	ImageSave(context.Context, []string, ...client.ImageSaveOption) (client.ImageSaveResult, error)
}

// exportImageByID 只按构建完成时固定的内容寻址 ID 导出。若先校验标签再按标签
// ImageSave，标签可能在两次请求间被替换，最终传输的就不是流水线登记的镜像。
func exportImageByID(ctx context.Context, exporter imageArchiveExporter, sourceImageID string) (client.ImageSaveResult, error) {
	if exporter == nil || !IsValidImageID(sourceImageID) {
		return nil, errors.New("待传输的本地 Docker 镜像无效")
	}
	inspect, err := exporter.ImageInspect(ctx, sourceImageID)
	if err != nil {
		return nil, fmt.Errorf("读取待传输的本地 Docker 镜像失败: %w", err)
	}
	if inspect.ID != sourceImageID {
		return nil, errors.New("待传输的本地 Docker 镜像身份校验失败")
	}
	archive, err := exporter.ImageSave(ctx, []string{sourceImageID})
	if err != nil {
		return nil, fmt.Errorf("从 Docker 构建运行时导出镜像失败: %w", err)
	}
	return archive, nil
}

// IsEDOLocalImage 判断镜像是否属于本地 Docker 或 Docker SSH 目标保存的本地命名空间。
func IsEDOLocalImage(value string) bool {
	named, err := distributionreference.ParseNormalizedNamed(strings.TrimSpace(value))
	return err == nil && distributionreference.Domain(named) == "edo.local"
}

// IsValidImageID 校验 Docker 返回的内容寻址镜像 ID，防止只凭可变标签发布。
func IsValidImageID(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "sha256:") {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func registryAuthConfig(input RegistryAuth) registrytypes.AuthConfig {
	result := registrytypes.AuthConfig{Username: input.Username, ServerAddress: input.ServerAddress}
	if input.Username == "" {
		result.IdentityToken = input.Credential
	} else {
		result.Password = input.Credential
	}
	return result
}

func encodeRegistryAuth(config registrytypes.AuthConfig) (string, error) {
	payload, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("编码镜像仓库认证信息失败: %w", err)
	}
	return base64.URLEncoding.EncodeToString(payload), nil
}

func (s *Service) runBuildx(
	ctx context.Context,
	buildContext io.Reader,
	dockerfile string,
	image string,
	registry RegistryAuth,
	options BuildOptions,
	output io.Writer,
) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("Docker CLI 未安装，无法执行 BuildKit 构建")
	}
	if !ValidBuildArgs(options.BuildArgs) {
		return errors.New("Docker 构建参数无效或与构建运行时变量冲突")
	}
	// 每次构建都使用独立 Docker 配置。即使是本地或匿名构建，
	// 也不得继承 EDO 进程所在账户的镜像仓库凭据。
	configDirectory, err := writeDockerCLIConfig(registry)
	if err != nil {
		return err
	}
	defer os.RemoveAll(configDirectory)

	builder := ""
	if strings.TrimSpace(s.config.DockerBuilderHost) != "" {
		builder = "default"
	}
	arguments := dockerBuildxArgumentsWithOptions(dockerfile, image, builder, options)
	// 每个任务都有独立检出目录、构建上下文、认证目录和不可重复镜像标签；
	// 这里不设置进程级锁，让 BuildKit 按 Worker 并发数并行调度构建。
	command := exec.CommandContext(ctx, "docker", arguments...)
	command.Env = dockerBuildEnvironment(
		s.config.DockerBuilderHost,
		s.config.DockerBuilderTLSCertPath,
		configDirectory,
		options.BuildArgs,
	)
	command.Stdin = buildContext
	diagnostic := &tailBuffer{limit: 16 * 1024}
	commandOutput := io.Writer(diagnostic)
	if output != nil {
		commandOutput = io.MultiWriter(diagnostic, output)
	}
	command.Stdout = commandOutput
	command.Stderr = commandOutput
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return &buildExecutionError{
				cause:     fmt.Errorf("Docker 镜像构建超时或被取消: %w", ctxErr),
				retryable: errors.Is(ctxErr, context.DeadlineExceeded),
			}
		}
		message := strings.TrimSpace(diagnostic.String())
		// 原始 BuildKit 输出已经写入受权限保护的流水线日志；错误值不得再次携带原始输出，
		// 否则统一错误日志可能把构建参数或仓库内容复制到系统日志。
		return &buildExecutionError{
			cause: fmt.Errorf("Docker 镜像构建失败: %w", err),
			retryable: transientRegistryStatusPattern.MatchString(message) ||
				transientRegistryNetworkPattern.MatchString(message),
		}
	}
	return nil
}

// ValidBuildArgs 拒绝会改写 Docker CLI/BuildKit 自身运行配置的参数名。
// 参数值通过子进程环境传递，避免出现在进程命令行中。
func ValidBuildArgs(values map[string]string) bool {
	for name, value := range values {
		upper := strings.ToUpper(name)
		if !dockerBuildArgNamePattern.MatchString(name) || len(value) > 16*1024 ||
			strings.HasPrefix(upper, "DOCKER_") || strings.HasPrefix(upper, "BUILDX_") ||
			strings.HasPrefix(upper, "BUILDKIT_") || upper == "HOME" || upper == "PATH" ||
			upper == "XDG_CONFIG_HOME" {
			return false
		}
	}
	return true
}

func dockerBuildxArguments(dockerfile, image, builder string) []string {
	return dockerBuildxArgumentsWithOptions(dockerfile, image, builder, defaultBuildOptions())
}

func dockerBuildxArgumentsWithOptions(
	dockerfile string,
	image, builder string,
	options BuildOptions,
) []string {
	arguments := []string{
		"buildx", "build", "--progress", "plain",
	}
	if options.Pull {
		arguments = append(arguments, "--pull")
	}
	if !options.CacheEnabled {
		arguments = append(arguments, "--no-cache")
	}
	arguments = append(arguments, "--load")
	if builder != "" {
		arguments = append(arguments, "--builder", builder)
	}
	arguments = append(arguments, "--file", filepath.ToSlash(dockerfile))
	if targetStage := strings.TrimSpace(options.TargetStage); targetStage != "" {
		arguments = append(arguments, "--target", targetStage)
	}
	if platform := strings.TrimSpace(options.Platform); platform != "" {
		arguments = append(arguments, "--platform", platform)
	}
	buildArgs := make(map[string]string, len(options.BuildArgs))
	for name, value := range options.BuildArgs {
		buildArgs[name] = value
	}
	arguments = appendSortedBuildArgNames(arguments, buildArgs)
	arguments = appendSortedBuildValues(arguments, "--label", options.Labels)
	arguments = append(arguments, "--tag", image)
	return append(arguments, "-")
}

func defaultBuildOptions() BuildOptions {
	return BuildOptions{Pull: true, CacheEnabled: true}
}

func appendSortedBuildValues(arguments []string, flag string, values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		arguments = append(arguments, flag, key+"="+values[key])
	}
	return arguments
}

func appendSortedBuildArgNames(arguments []string, values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		arguments = append(arguments, "--build-arg", key)
	}
	return arguments
}

func dockerBuildEnvironment(host, tlsCertPath, configDirectory string, buildArgs ...map[string]string) []string {
	configured := map[string]string{}
	if len(buildArgs) > 0 {
		configured = buildArgs[0]
	}
	environment := make([]string, 0, len(os.Environ())+len(configured)+3)
	host = strings.TrimSpace(host)
	tlsCertPath = strings.TrimSpace(tlsCertPath)
	configDirectory = strings.TrimSpace(configDirectory)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if _, overridden := configured[name]; overridden {
			continue
		}
		switch strings.ToUpper(name) {
		case "DOCKER_BUILDKIT":
			continue
		case "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH":
			if host != "" {
				continue
			}
		case "DOCKER_CONFIG":
			if configDirectory != "" {
				continue
			}
		}
		environment = append(environment, value)
	}
	if host != "" {
		environment = append(environment, "DOCKER_HOST="+host)
		if tlsCertPath != "" {
			environment = append(environment, "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH="+tlsCertPath)
		}
	}
	if configDirectory != "" {
		environment = append(environment, "DOCKER_CONFIG="+configDirectory)
	}
	environment = append(environment, "DOCKER_BUILDKIT=1")
	keys := make([]string, 0, len(configured))
	for key := range configured {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		environment = append(environment, key+"="+configured[key])
	}
	return environment
}

func writeDockerCLIConfig(registry RegistryAuth) (string, error) {
	directory, err := os.MkdirTemp("", "edo-docker-config-*")
	if err != nil {
		return "", fmt.Errorf("创建 Docker 临时认证目录失败: %w", err)
	}
	removeDirectory := true
	defer func() {
		if removeDirectory {
			_ = os.RemoveAll(directory)
		}
	}()
	type authEntry struct {
		Auth          string `json:"auth,omitempty"`
		IdentityToken string `json:"identitytoken,omitempty"`
	}
	configuration := struct {
		Auths               map[string]authEntry `json:"auths,omitempty"`
		CLIPluginsExtraDirs []string             `json:"cliPluginsExtraDirs,omitempty"`
	}{
		Auths:               make(map[string]authEntry),
		CLIPluginsExtraDirs: dockerCLIPluginDirectories(),
	}
	if registry.Credential != "" {
		entry := authEntry{}
		if registry.Username == "" {
			entry.IdentityToken = registry.Credential
		} else {
			entry.Auth = base64.StdEncoding.EncodeToString([]byte(registry.Username + ":" + registry.Credential))
		}
		for _, address := range []string{strings.TrimSpace(registry.ServerAddress), strings.TrimSpace(registry.Host)} {
			if address != "" {
				configuration.Auths[address] = entry
			}
		}
	}
	payload, err := json.Marshal(configuration)
	if err != nil {
		return "", fmt.Errorf("编码 Docker 临时认证配置失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "config.json"), payload, 0o600); err != nil {
		return "", fmt.Errorf("写入 Docker 临时认证配置失败: %w", err)
	}
	removeDirectory = false
	return directory, nil
}

func dockerCLIPluginDirectories() []string {
	candidates := []string{
		"/usr/local/libexec/docker/cli-plugins",
		"/usr/local/lib/docker/cli-plugins",
		"/usr/libexec/docker/cli-plugins",
		"/usr/lib/docker/cli-plugins",
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".docker", "cli-plugins"))
	}
	if executable, err := exec.LookPath("docker"); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "cli-plugins"))
	}
	result := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if _, exists := seen[candidate]; exists {
			continue
		}
		if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
			continue
		}
		seen[candidate] = struct{}{}
		result = append(result, candidate)
	}
	return result
}

type tailBuffer struct {
	mutex sync.Mutex
	data  []byte
	limit int
}

func (b *tailBuffer) Write(value []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.data = append(b.data, value...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(value), nil
}

func (b *tailBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return string(b.data)
}

func createBuildContext(root, dockerfile string) (*os.File, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("解析构建上下文失败: %w", err)
	}
	dockerfile = filepath.Clean(dockerfile)
	if filepath.IsAbs(dockerfile) || dockerfile == ".." || strings.HasPrefix(dockerfile, ".."+string(filepath.Separator)) {
		return nil, errors.New("Dockerfile 必须位于构建上下文中")
	}
	if info, err := os.Stat(filepath.Join(root, dockerfile)); err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("构建上下文中找不到 Dockerfile")
	}
	matcher, err := dockerIgnoreMatcher(root)
	if err != nil {
		return nil, err
	}
	archive, err := os.CreateTemp("", "edo-build-context-*.tar")
	if err != nil {
		return nil, fmt.Errorf("创建临时构建上下文失败: %w", err)
	}
	removeArchive := true
	defer func() {
		if removeArchive {
			name := archive.Name()
			_ = archive.Close()
			_ = os.Remove(name)
		}
	}()
	writer := tar.NewWriter(archive)
	var totalSize int64
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if relative == ".git" || strings.HasPrefix(relative, ".git/") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		mustInclude := relative == filepath.ToSlash(dockerfile) || relative == ".dockerignore"
		if !mustInclude && matcher != nil {
			ignored, err := matcher.MatchesOrParentMatches(relative)
			if err != nil {
				return err
			}
			if ignored {
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeSocket|os.ModeDevice|os.ModeNamedPipe) != 0 {
			return nil
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = relative
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		totalSize += info.Size()
		if totalSize > maximumBuildContextSize {
			return errors.New("Docker 构建上下文超过 1 GiB 限制")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("打包 Docker 构建上下文失败: %w", err)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("读取 Docker 构建上下文失败: %w", err)
	}
	removeArchive = false
	return archive, nil
}

func dockerIgnoreMatcher(root string) (*patternmatcher.PatternMatcher, error) {
	file, err := os.Open(filepath.Join(root, ".dockerignore"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 .dockerignore 失败: %w", err)
	}
	defer file.Close()
	patterns, err := ignorefile.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("解析 .dockerignore 失败: %w", err)
	}
	matcher, err := patternmatcher.New(patterns)
	if err != nil {
		return nil, fmt.Errorf("解析 .dockerignore 规则失败: %w", err)
	}
	return matcher, nil
}
