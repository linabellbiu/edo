package dockerengine

import (
	"archive/tar"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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

func TestCacheImageNameUsesStableRepositoryTag(t *testing.T) {
	cache, err := cacheImageName("registry.example.com/team/api:abcdef123456")
	if err != nil {
		t.Fatal(err)
	}
	if cache != "registry.example.com/team/api:zrt-cache" {
		t.Fatalf("缓存标签不正确: %s", cache)
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

func TestDockerBuildxArgumentsUseSessionCapableBuilder(t *testing.T) {
	arguments := dockerBuildxArguments("deploy/Dockerfile", []string{"zrt.local/app:commit", "zrt.local/app:zrt-cache"}, "", "default")
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

	local := dockerBuildxArguments("Dockerfile", []string{"zrt.local/app:local"}, "", "")
	if slices.Contains(local, "--builder") {
		t.Fatalf("本地构建不应覆盖当前 Docker Context/Builder: %v", local)
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

	local := environmentValues(dockerBuildEnvironment("", ""))
	if local["DOCKER_HOST"] != "unix:///host/docker.sock" || local["DOCKER_CONTEXT"] != "desktop-linux" ||
		local["DOCKER_CONFIG"] != "/host/docker-config" {
		t.Fatalf("本地构建没有保留宿主机 Docker 环境: %+v", local)
	}

	container := environmentValues(dockerBuildEnvironment("tcp://docker-builder:2375", "/tmp/zrt-docker-config"))
	if container["DOCKER_HOST"] != "tcp://docker-builder:2375" || container["DOCKER_CONFIG"] != "/tmp/zrt-docker-config" {
		t.Fatalf("容器构建没有使用显式 DinD: %+v", container)
	}
	if _, exists := container["DOCKER_CONTEXT"]; exists {
		t.Fatalf("显式 DinD 不应继承宿主机 Docker Context: %+v", container)
	}
	if container["DOCKER_BUILDKIT"] != "1" {
		t.Fatalf("构建必须启用 BuildKit: %+v", container)
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
