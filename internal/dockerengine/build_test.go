package dockerengine

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

func TestCreateBuildContextAppliesDockerIgnore(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"Dockerfile":    "FROM scratch\nCOPY kept.txt /\n",
		".dockerignore": "ignored.txt\n",
		"kept.txt":      "kept",
		"ignored.txt":   "ignored",
		".git/config":   "secret repository metadata",
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := createBuildContext(root, "Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		name := archive.Name()
		_ = archive.Close()
		_ = os.Remove(name)
	}()
	found := map[string]bool{}
	reader := tar.NewReader(archive)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		found[header.Name] = true
	}
	for _, expected := range []string{"Dockerfile", ".dockerignore", "kept.txt"} {
		if !found[expected] {
			t.Fatalf("构建上下文缺少 %s: %+v", expected, found)
		}
	}
	for _, excluded := range []string{"ignored.txt", ".git", ".git/config"} {
		if found[excluded] {
			t.Fatalf("构建上下文不应包含 %s", excluded)
		}
	}
}

func TestCreateBuildContextRejectsDockerfileOutsideContext(t *testing.T) {
	root := t.TempDir()
	if _, err := createBuildContext(root, "../Dockerfile"); err == nil {
		t.Fatal("未拒绝构建上下文外的 Dockerfile")
	}
}

func TestMatchingRepoDigestUsesPushedRepository(t *testing.T) {
	expected := "registry.example.com/team/api@sha256:" + strings.Repeat("b", 64)
	other := "mirror.example.com/team/api@sha256:" + strings.Repeat("a", 64)
	if actual := matchingRepoDigest("registry.example.com/team/api:commit", []string{other, expected}); actual != expected {
		t.Fatalf("没有按刚推送的仓库筛选摘要: %q", actual)
	}
	if actual := matchingRepoDigest("registry.example.com/team/api:commit", []string{other}); actual != "" {
		t.Fatalf("不得返回其他仓库的摘要: %q", actual)
	}
}

func TestRetryableBuildErrorUsesDockerBoundaryClassification(t *testing.T) {
	transient := &buildExecutionError{cause: errors.New("registry unavailable"), retryable: true}
	permanent := &buildExecutionError{cause: errors.New("Dockerfile invalid")}
	if !IsRetryableBuildError(transient) || IsRetryableBuildError(permanent) {
		t.Fatal("Docker 构建错误类型的重试分类错误")
	}
	if !IsRetryableBuildError(context.DeadlineExceeded) || IsRetryableBuildError(context.Canceled) {
		t.Fatal("构建超时可以重试，但主动取消不得自动重试")
	}
	for _, message := range []string{
		"unexpected status from POST request: 429 Too Many Requests",
		"server returned HTTP 503 Service Unavailable",
	} {
		if !transientRegistryStatusPattern.MatchString(message) {
			t.Fatalf("没有识别镜像仓库临时状态: %q", message)
		}
	}
	if transientRegistryStatusPattern.MatchString("Dockerfile parse error at line 500") {
		t.Fatal("Dockerfile 错误不得因普通数字被误判为临时故障")
	}
	for _, message := range []string{
		`failed to do request: Head "https://registry-1.docker.io/v2/library/alpine/manifests/3.21": EOF`,
		`failed to authorize: failed to fetch anonymous token: Get "https://auth.docker.io/token": net/http: TLS handshake timeout`,
	} {
		if !transientRegistryNetworkPattern.MatchString(message) {
			t.Fatalf("没有识别镜像仓库瞬时网络故障: %q", message)
		}
	}
	if transientRegistryNetworkPattern.MatchString("Dockerfile heredoc parse error: unexpected EOF") {
		t.Fatal("Dockerfile EOF 不得被误判为镜像仓库网络故障")
	}
}

func TestZRTLocalImageAndImageIDValidation(t *testing.T) {
	if !IsZRTLocalImage("zrt.local/order-api:abcdef-12345678") {
		t.Fatal("合法的 ZRT 本地镜像没有被识别")
	}
	if IsZRTLocalImage("registry.example.com/zrt/order-api:latest") || IsZRTLocalImage("zrt.local.invalid/order-api:latest") {
		t.Fatal("镜像仓库地址不应被识别为 ZRT 本地镜像")
	}
	validID := "sha256:" + strings.Repeat("a", 64)
	if !IsValidImageID(validID) || IsValidImageID("sha256:"+strings.Repeat("g", 64)) || IsValidImageID("sha256:short") {
		t.Fatal("Docker 镜像 ID 校验结果错误")
	}
}

func TestExportImageByIDNeverFallsBackToMutableTag(t *testing.T) {
	sourceImageID := "sha256:" + strings.Repeat("b", 64)
	exporter := &recordingImageArchiveExporter{
		inspectID: sourceImageID,
		archive:   "fixed-image-archive",
	}
	archive, err := exportImageByID(context.Background(), exporter, sourceImageID)
	if err != nil {
		t.Fatalf("按固定 Image ID 导出失败: %v", err)
	}
	defer archive.Close()
	payload, err := io.ReadAll(archive)
	if err != nil || string(payload) != exporter.archive {
		t.Fatalf("镜像归档内容不正确: %q err=%v", payload, err)
	}
	if exporter.inspected != sourceImageID || len(exporter.saved) != 1 || exporter.saved[0] != sourceImageID {
		t.Fatalf("镜像导出重新使用了可变标签: inspected=%q saved=%v", exporter.inspected, exporter.saved)
	}
}

type recordingImageArchiveExporter struct {
	inspectID string
	archive   string
	inspected string
	saved     []string
}

func (f *recordingImageArchiveExporter) ImageInspect(
	_ context.Context,
	image string,
	_ ...client.ImageInspectOption,
) (client.ImageInspectResult, error) {
	f.inspected = image
	result := client.ImageInspectResult{}
	result.ID = f.inspectID
	return result, nil
}

func (f *recordingImageArchiveExporter) ImageSave(
	_ context.Context,
	images []string,
	_ ...client.ImageSaveOption,
) (client.ImageSaveResult, error) {
	f.saved = append([]string(nil), images...)
	return io.NopCloser(strings.NewReader(f.archive)), nil
}

func TestDockerBuildxArgumentsUseSessionCapableBuilder(t *testing.T) {
	arguments := dockerBuildxArguments("deploy/Dockerfile", "zrt.local/app:commit", "default")
	for _, expected := range []string{"buildx", "build", "--load", "--file", "deploy/Dockerfile", "zrt.local/app:commit"} {
		if !slices.Contains(arguments, expected) {
			t.Fatalf("Buildx 参数缺少 %q: %v", expected, arguments)
		}
	}
	if arguments[len(arguments)-1] != "-" {
		t.Fatalf("构建上下文没有通过标准输入传入: %v", arguments)
	}
	if !slices.Contains(arguments, "default") {
		t.Fatalf("显式 Docker API 构建没有选择默认 Builder: %v", arguments)
	}

	local := dockerBuildxArguments("Dockerfile", "zrt.local/app:local", "")
	if slices.Contains(local, "--builder") {
		t.Fatalf("本地构建不应覆盖当前 Docker Context/Builder: %v", local)
	}
}

func TestDockerBuildxArgumentsWithOptionsAreStable(t *testing.T) {
	arguments := dockerBuildxArgumentsWithOptions(
		"deploy/Dockerfile",
		"registry.example.com/team/app:commit",
		"default",
		BuildOptions{
			Pull:         true,
			CacheEnabled: true,
			TargetStage:  "runtime",
			Platform:     "linux/amd64",
			BuildArgs: map[string]string{
				"ZETA":  "last",
				"ALPHA": "first",
			},
			Labels: map[string]string{
				"zrt.example/revision":           "abcdef",
				"org.opencontainers.image.title": "app",
			},
		},
	)
	expected := []string{
		"buildx", "build", "--progress", "plain", "--pull", "--load",
		"--builder", "default",
		"--file", "deploy/Dockerfile",
		"--target", "runtime",
		"--platform", "linux/amd64",
		"--build-arg", "ALPHA",
		"--build-arg", "ZETA",
		"--label", "org.opencontainers.image.title=app",
		"--label", "zrt.example/revision=abcdef",
		"--tag", "registry.example.com/team/app:commit",
		"-",
	}
	if !slices.Equal(arguments, expected) {
		t.Fatalf("Buildx 参数顺序不稳定:\n实际: %v\n期望: %v", arguments, expected)
	}
}

func TestDockerBuildxArgumentsCanDisablePullAndLocalCache(t *testing.T) {
	arguments := dockerBuildxArgumentsWithOptions(
		"Dockerfile",
		"zrt.local/app:commit",
		"",
		BuildOptions{BuildArgs: map[string]string{"VERSION": "1.2.3"}},
	)
	for _, unexpected := range []string{"--pull", "--cache-from", "BUILDKIT_INLINE_CACHE=1", "zrt-cache"} {
		if slices.Contains(arguments, unexpected) {
			t.Fatalf("禁用拉取或缓存后仍包含 %q: %v", unexpected, arguments)
		}
	}
	if !slices.Contains(arguments, "--no-cache") {
		t.Fatalf("禁用本地缓存后没有传递 --no-cache: %v", arguments)
	}
	if !slices.Contains(arguments, "VERSION") || slices.Contains(arguments, "VERSION=1.2.3") {
		t.Fatalf("禁用缓存不应丢弃用户构建参数: %v", arguments)
	}
}

func TestDockerBuildArgsStayOutOfProcessArguments(t *testing.T) {
	arguments := dockerBuildxArgumentsWithOptions(
		"Dockerfile", "zrt.local/app:commit", "",
		BuildOptions{BuildArgs: map[string]string{"API_TOKEN": "super-secret-value"}},
	)
	if slices.Contains(arguments, "API_TOKEN=super-secret-value") || !slices.Contains(arguments, "API_TOKEN") {
		t.Fatalf("构建参数值不应进入进程参数: %v", arguments)
	}
	environment := environmentValues(dockerBuildEnvironment("", "", "/tmp/zrt-build-config", map[string]string{
		"API_TOKEN": "super-secret-value",
	}))
	if environment["API_TOKEN"] != "super-secret-value" {
		t.Fatalf("构建参数没有通过子进程环境传给 Docker CLI: %+v", environment)
	}
}

func TestDockerBuildArgsRejectRuntimeControlVariables(t *testing.T) {
	for _, name := range []string{"DOCKER_CONFIG", "docker_host", "BUILDX_BUILDER", "BUILDKIT_HOST", "PATH", "HOME"} {
		if ValidBuildArgs(map[string]string{name: "attacker-controlled"}) {
			t.Fatalf("Docker 客户端控制变量不应作为普通构建参数传入: %s", name)
		}
	}
	if !ValidBuildArgs(map[string]string{"APP_VERSION": "1.2.3"}) {
		t.Fatal("普通 Docker 构建参数被错误拒绝")
	}
}

func TestDockerBuildxArgumentsDefaultToLocalCacheOnly(t *testing.T) {
	arguments := dockerBuildxArguments(
		"Dockerfile",
		"registry.example.com/team/app:commit",
		"",
	)
	for _, expected := range []string{"--pull", "--tag", "registry.example.com/team/app:commit"} {
		if !slices.Contains(arguments, expected) {
			t.Fatalf("默认构建行为缺少 %q: %v", expected, arguments)
		}
	}
	for _, unexpected := range []string{"--no-cache", "--cache-from", "BUILDKIT_INLINE_CACHE=1", "zrt-cache"} {
		if slices.Contains(arguments, unexpected) {
			t.Fatalf("默认本地缓存构建不应包含 %q: %v", unexpected, arguments)
		}
	}
}

func TestWriteDockerCLIConfigProtectsRegistryCredential(t *testing.T) {
	directory, err := writeDockerCLIConfig(RegistryAuth{
		ServerAddress: "https://registry.example.com", Host: "registry.example.com",
		Username: "builder", Credential: "private-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "config.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("Docker 临时认证配置权限不安全: %o", info.Mode().Perm())
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "private-token") || !strings.Contains(string(payload), `"auths"`) {
		t.Fatalf("Docker 临时认证配置格式不正确: %s", payload)
	}
}

func TestDockerBuildEnvironmentSelectsLocalOrConfiguredRuntime(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///host/docker.sock")
	t.Setenv("DOCKER_CONTEXT", "desktop-linux")
	t.Setenv("DOCKER_CONFIG", "/host/docker-config")
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	t.Setenv("DOCKER_CERT_PATH", "/host/docker-certs")

	local := environmentValues(dockerBuildEnvironment("", "", "/tmp/zrt-local-docker-config"))
	if local["DOCKER_HOST"] != "unix:///host/docker.sock" || local["DOCKER_CONTEXT"] != "desktop-linux" ||
		local["DOCKER_CONFIG"] != "/tmp/zrt-local-docker-config" || local["DOCKER_TLS_VERIFY"] != "1" ||
		local["DOCKER_CERT_PATH"] != "/host/docker-certs" {
		t.Fatalf("本地构建没有隔离 Docker 认证配置: %+v", local)
	}

	container := environmentValues(dockerBuildEnvironment(
		"tcp://docker-builder:2376", "/certs/client", "/tmp/zrt-docker-config",
	))
	if container["DOCKER_HOST"] != "tcp://docker-builder:2376" || container["DOCKER_CONFIG"] != "/tmp/zrt-docker-config" ||
		container["DOCKER_TLS_VERIFY"] != "1" || container["DOCKER_CERT_PATH"] != "/certs/client" {
		t.Fatalf("容器构建没有使用显式 DinD: %+v", container)
	}
	if _, exists := container["DOCKER_CONTEXT"]; exists {
		t.Fatalf("显式 DinD 不应继承宿主机 Docker Context: %+v", container)
	}
	if container["DOCKER_BUILDKIT"] != "1" {
		t.Fatalf("构建必须启用 BuildKit: %+v", container)
	}
}

func TestAnonymousDockerConfigDoesNotInheritCredentials(t *testing.T) {
	directory, err := writeDockerCLIConfig(RegistryAuth{})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	payload, err := os.ReadFile(filepath.Join(directory, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "private-token") || strings.Contains(string(payload), "/host/docker-config") {
		t.Fatalf("匿名构建配置继承了宿主机凭据: %s", payload)
	}
}

func environmentValues(values []string) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		name, content, found := strings.Cut(value, "=")
		if found {
			result[name] = content
		}
	}
	return result
}

func TestTailBufferKeepsLatestBuildDiagnostic(t *testing.T) {
	buffer := &tailBuffer{limit: 12}
	_, _ = buffer.Write([]byte("old-output\n"))
	_, _ = buffer.Write([]byte("final-error"))
	if buffer.String() != "\nfinal-error" {
		t.Fatalf("没有保留最新构建诊断: %q", buffer.String())
	}
}
