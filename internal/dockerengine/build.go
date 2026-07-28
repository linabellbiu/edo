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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	distributionreference "github.com/distribution/reference"
	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

const maximumBuildContextSize int64 = 1024 * 1024 * 1024

type RegistryAuth struct {
	ServerAddress string
	Host          string
	Username      string
	Credential    string
}

func (s *Service) BuildAndPush(
	ctx context.Context,
	contextDirectory, dockerfile, image string,
	registry RegistryAuth,
	timeout time.Duration,
	output io.Writer,
) (string, error, error) {
	apiClient, err := s.BuilderClient()
	if err != nil {
		return "", nil, err
	}
	defer apiClient.Close()
	buildContext, err := createBuildContext(contextDirectory, dockerfile)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		name := buildContext.Name()
		_ = buildContext.Close()
		_ = os.Remove(name)
	}()

	buildContextTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	authConfig := registryAuthConfig(registry)
	encodedAuth, err := encodeRegistryAuth(authConfig)
	if err != nil {
		return "", nil, err
	}
	cacheImage, err := cacheImageName(image)
	if err != nil {
		return "", nil, err
	}
	var cacheWarning error
	cachePull, err := apiClient.ImagePull(buildContextTimeout, cacheImage, client.ImagePullOptions{RegistryAuth: encodedAuth})
	if err != nil {
		cacheWarning = fmt.Errorf("拉取远程构建缓存失败: %w", err)
	} else if err := cachePull.Wait(buildContextTimeout); err != nil {
		cacheWarning = fmt.Errorf("拉取远程构建缓存失败: %w", err)
	}
	if err := s.runBuildx(buildContextTimeout, buildContext, dockerfile, []string{image, cacheImage}, cacheImage, registry, output); err != nil {
		return "", cacheWarning, err
	}

	push, err := apiClient.ImagePush(buildContextTimeout, image, client.ImagePushOptions{RegistryAuth: encodedAuth})
	if err != nil {
		return "", cacheWarning, fmt.Errorf("提交 Docker 镜像推送失败: %w", err)
	}
	if err := push.Wait(buildContextTimeout); err != nil {
		return "", cacheWarning, fmt.Errorf("等待 Docker 镜像推送完成失败: %w", err)
	}
	cachePush, err := apiClient.ImagePush(buildContextTimeout, cacheImage, client.ImagePushOptions{RegistryAuth: encodedAuth})
	if err != nil {
		cacheWarning = errors.Join(cacheWarning, fmt.Errorf("提交远程构建缓存失败: %w", err))
	} else if err := cachePush.Wait(buildContextTimeout); err != nil {
		cacheWarning = errors.Join(cacheWarning, fmt.Errorf("等待远程构建缓存更新完成失败: %w", err))
	}
	inspect, err := apiClient.ImageInspect(buildContextTimeout, image)
	if err != nil {
		return "", cacheWarning, fmt.Errorf("读取已推送镜像摘要失败: %w", err)
	}
	for _, digest := range inspect.RepoDigests {
		if strings.Contains(digest, "@sha256:") {
			return digest, cacheWarning, nil
		}
	}
	return "", cacheWarning, errors.New("镜像仓库没有返回可验证的镜像摘要")
}

// BuildLocal 在当前构建运行时中构建镜像，并返回内容寻址的镜像 ID。
func (s *Service) BuildLocal(
	ctx context.Context,
	contextDirectory, dockerfile, image string,
	timeout time.Duration,
	output io.Writer,
) (string, error) {
	if !IsZRTLocalImage(image) {
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
	cacheImage, err := cacheImageName(image)
	if err != nil {
		return "", err
	}
	if err := s.runBuildx(buildContextTimeout, buildContext, dockerfile, []string{image, cacheImage}, "", RegistryAuth{}, output); err != nil {
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
	if !IsZRTLocalImage(image) || !IsValidImageID(sourceImageID) {
		return "", errors.New("待传输的本地 Docker 镜像无效")
	}
	apiClient, err := s.BuilderClient()
	if err != nil {
		return "", err
	}
	defer apiClient.Close()
	transferContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	inspect, err := apiClient.ImageInspect(transferContext, image)
	if err != nil {
		return "", fmt.Errorf("读取待传输的本地 Docker 镜像失败: %w", err)
	}
	if inspect.ID != sourceImageID {
		return "", errors.New("待传输的本地 Docker 镜像已发生变化")
	}
	archive, err := apiClient.ImageSave(transferContext, []string{image})
	if err != nil {
		return "", fmt.Errorf("从 Docker 构建运行时导出镜像失败: %w", err)
	}
	defer archive.Close()
	targetImageID, err := s.loadImageToSSH(transferContext, endpointID, image, archive)
	if err != nil {
		return "", err
	}
	return targetImageID, nil
}

// IsZRTLocalImage 判断镜像是否属于本地 Docker 或 Docker SSH 目标保存的本地命名空间。
func IsZRTLocalImage(value string) bool {
	named, err := distributionreference.ParseNormalizedNamed(strings.TrimSpace(value))
	return err == nil && distributionreference.Domain(named) == "zrt.local"
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

func cacheImageName(image string) (string, error) {
	named, err := distributionreference.ParseNormalizedNamed(image)
	if err != nil {
		return "", fmt.Errorf("解析构建镜像名称失败: %w", err)
	}
	cache, err := distributionreference.WithTag(distributionreference.TrimNamed(named), "zrt-cache")
	if err != nil {
		return "", fmt.Errorf("生成构建缓存镜像名称失败: %w", err)
	}
	return distributionreference.FamiliarString(cache), nil
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
	tags []string,
	cacheFrom string,
	registry RegistryAuth,
	output io.Writer,
) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("Docker CLI 未安装，无法执行 BuildKit 构建")
	}
	configDirectory := ""
	if registry.Credential != "" {
		var err error
		configDirectory, err = writeDockerCLIConfig(registry)
		if err != nil {
			return err
		}
		defer os.RemoveAll(configDirectory)
	}

	builder := ""
	if strings.TrimSpace(s.config.DockerBuilderHost) != "" {
		builder = "default"
	}
	arguments := dockerBuildxArguments(dockerfile, tags, cacheFrom, builder)
	// 每个任务都有独立检出目录、构建上下文、认证目录和不可重复镜像标签；
	// 这里不设置进程级锁，让 BuildKit 按 Worker 并发数并行调度构建。
	command := exec.CommandContext(ctx, "docker", arguments...)
	command.Env = dockerBuildEnvironment(s.config.DockerBuilderHost, configDirectory)
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
			return fmt.Errorf("Docker 镜像构建超时或被取消: %w", ctxErr)
		}
		message := strings.TrimSpace(diagnostic.String())
		if message == "" {
			return fmt.Errorf("Docker 镜像构建失败: %w", err)
		}
		// BuildKit 输出只进入后端错误日志；流水线对外仍返回稳定、安全的中文提示。
		return fmt.Errorf("Docker 镜像构建失败: %w: %s", err, message)
	}
	return nil
}

func dockerBuildxArguments(dockerfile string, tags []string, cacheFrom, builder string) []string {
	arguments := []string{
		"buildx", "build", "--progress", "plain", "--pull", "--load",
	}
	if builder != "" {
		arguments = append(arguments, "--builder", builder)
	}
	arguments = append(arguments,
		"--file", filepath.ToSlash(dockerfile), "--build-arg", "BUILDKIT_INLINE_CACHE=1",
	)
	for _, tag := range tags {
		arguments = append(arguments, "--tag", tag)
	}
	if cacheFrom != "" {
		arguments = append(arguments, "--cache-from", cacheFrom)
	}
	return append(arguments, "-")
}

func dockerBuildEnvironment(host, configDirectory string) []string {
	environment := make([]string, 0, len(os.Environ())+3)
	host = strings.TrimSpace(host)
	configDirectory = strings.TrimSpace(configDirectory)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		switch strings.ToUpper(name) {
		case "DOCKER_BUILDKIT":
			continue
		case "DOCKER_HOST", "DOCKER_CONTEXT":
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
	}
	if configDirectory != "" {
		environment = append(environment, "DOCKER_CONFIG="+configDirectory)
	}
	return append(environment, "DOCKER_BUILDKIT=1")
}

func writeDockerCLIConfig(registry RegistryAuth) (string, error) {
	directory, err := os.MkdirTemp("", "zrt-docker-config-*")
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
	archive, err := os.CreateTemp("", "zrt-build-context-*.tar")
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
