package dockerengine

import (
	"archive/tar"
	"errors"
	"io"
	"os"
	"path/filepath"
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
