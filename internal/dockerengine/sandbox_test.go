package dockerengine

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"
)

func TestNormalizeScriptContainerInputRequiresPinnedImageAndEmptyOutput(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	valid := ScriptContainerInput{
		Image: "alpine:3.22", Script: "echo ok", SourceDirectory: source,
		WorkingDirectory: "cmd", ArtifactPath: "dist", OutputDirectory: output,
		Environment:       map[string]string{"BUILD_MODE": "release"},
		SystemEnvironment: map[string]string{"EDO_PIPELINE_RUN_ID": "run-1"}, Timeout: 30 * time.Second,
	}
	normalized, err := normalizeScriptContainerInput(valid)
	if err != nil {
		t.Fatalf("合法脚本容器配置被拒绝: %v", err)
	}
	if normalized.SourceDirectory == "" || normalized.OutputDirectory == "" {
		t.Fatalf("脚本容器目录没有规范化: %+v", normalized)
	}
	withoutArtifact := valid
	withoutArtifact.ArtifactPath = ""
	withoutArtifact.OutputDirectory = ""
	normalized, err = normalizeScriptContainerInput(withoutArtifact)
	if err != nil {
		t.Fatalf("不保存制品的合法脚本容器配置被拒绝: %v", err)
	}
	if normalized.ArtifactPath != "" || normalized.OutputDirectory != "" {
		t.Fatalf("不保存制品时不应生成输出目录: %+v", normalized)
	}

	invalid := []ScriptContainerInput{
		func() ScriptContainerInput { value := valid; value.Image = "alpine:latest"; return value }(),
		func() ScriptContainerInput { value := valid; value.Image = "alpine"; return value }(),
		func() ScriptContainerInput { value := valid; value.WorkingDirectory = "../outside"; return value }(),
		func() ScriptContainerInput { value := valid; value.ArtifactPath = "/etc"; return value }(),
		func() ScriptContainerInput { value := valid; value.OutputDirectory = ""; return value }(),
		func() ScriptContainerInput {
			value := valid
			value.Environment = map[string]string{"HOME": "/root"}
			return value
		}(),
		func() ScriptContainerInput {
			value := valid
			value.SystemEnvironment = map[string]string{"BUILD_MODE": "release"}
			return value
		}(),
	}
	for _, input := range invalid {
		if _, err := normalizeScriptContainerInput(input); !errors.Is(err, ErrInvalidScriptContainer) {
			t.Fatalf("无效脚本容器配置未被拒绝: input=%+v err=%v", input, err)
		}
	}
}

func TestScriptContainerCreateOptionsHaveNoHostOrDockerMounts(t *testing.T) {
	options := scriptContainerCreateOptions(ScriptContainerInput{
		Image: "alpine:3.22", WorkingDirectory: ".",
		Environment: map[string]string{"VALUE": "configured"},
		Labels:      map[string]string{"io.edo.pipeline.run.id": "run-1"},
	}, "sha256:image")
	if options.Config.User != scriptContainerUser || options.Config.WorkingDir != "/workspace/src/." {
		t.Fatalf("脚本容器没有使用受限用户或工作目录: %+v", options.Config)
	}
	if !options.HostConfig.ReadonlyRootfs || options.HostConfig.Privileged ||
		len(options.HostConfig.CapDrop) != 1 || options.HostConfig.CapDrop[0] != "ALL" ||
		len(options.HostConfig.SecurityOpt) != 1 || options.HostConfig.SecurityOpt[0] != "no-new-privileges:true" ||
		options.HostConfig.NetworkMode.IsHost() {
		t.Fatalf("脚本容器安全边界不完整: %+v", options.HostConfig)
	}
	if len(options.HostConfig.Binds) != 0 || len(options.HostConfig.Mounts) != 1 {
		t.Fatalf("脚本容器不得挂载宿主机目录: %+v", options.HostConfig.Mounts)
	}
	workspace := options.HostConfig.Mounts[0]
	if workspace.Type != mount.TypeVolume || workspace.Source != "" || workspace.Target != scriptWorkspace ||
		workspace.VolumeOptions == nil || !workspace.VolumeOptions.NoCopy {
		t.Fatalf("源码必须使用无宿主机路径的匿名卷: %+v", workspace)
	}
	for _, value := range options.Config.Env {
		if value == "DOCKER_HOST=tcp://docker-builder:2376" || value == "DOCKER_CERT_PATH=/certs/client" {
			t.Fatalf("脚本容器泄露了 Docker 连接配置: %q", value)
		}
	}
	if options.Config.Labels["io.edo.managed"] != "script" || options.HostConfig.PidsLimit == nil ||
		*options.HostConfig.PidsLimit != 512 || options.HostConfig.Memory != 2*1024*1024*1024 ||
		options.HostConfig.NanoCPUs != 2_000_000_000 {
		t.Fatalf("脚本容器标签或资源限制不完整: labels=%v resources=%+v", options.Config.Labels, options.HostConfig.Resources)
	}
}

func TestCreateScriptSourceArchivePreservesSafeSymlinkAndExcludesGit(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "input.txt"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".git", "config"), []byte("credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("input.txt", filepath.Join(source, "input-link")); err != nil {
		t.Skipf("当前文件系统不支持符号链接: %v", err)
	}
	archive, err := createScriptSourceArchive(context.Background(), source)
	if err != nil {
		t.Fatalf("打包脚本源码失败: %v", err)
	}
	defer func() {
		name := archive.Name()
		_ = archive.Close()
		_ = os.Remove(name)
	}()
	reader := tar.NewReader(archive)
	entries := make(map[string]*tar.Header)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		copy := *header
		entries[header.Name] = &copy
	}
	if _, exists := entries["src/input.txt"]; !exists {
		t.Fatal("源码归档缺少普通文件")
	}
	link := entries["src/input-link"]
	if link == nil || link.Typeflag != tar.TypeSymlink || link.Linkname != "input.txt" {
		t.Fatalf("源码符号链接未按本体归档: %+v", link)
	}
	if _, exists := entries["src/.git/config"]; exists {
		t.Fatal("源码归档泄露了 .git 内容")
	}
}

func TestCreateScriptSourceArchiveRejectsEscapingSymlink(t *testing.T) {
	source := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "escape")); err != nil {
		t.Skipf("当前文件系统不支持符号链接: %v", err)
	}
	if _, err := createScriptSourceArchive(context.Background(), source); !errors.Is(err, ErrInvalidScriptContainer) {
		t.Fatalf("指向工作区外的符号链接未被拒绝: %v", err)
	}
}

func TestCreateScriptSourceArchiveEnforcesFileCount(t *testing.T) {
	source := t.TempDir()
	for _, name := range []string{"one", "two", "three"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := createScriptSourceArchiveWithLimits(context.Background(), source, 1024, 2); !errors.Is(err, ErrInvalidScriptContainer) {
		t.Fatalf("超多源码条目未被拒绝: %v", err)
	}
}

func TestExtractScriptArtifactAcceptsRegularFilesAndDirectories(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "dist", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	content := []byte("binary")
	if err := writer.WriteHeader(&tar.Header{Name: "dist/app", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	result, err := extractScriptArtifact(bytes.NewReader(archive.Bytes()), output, "dist", 1024)
	if err != nil {
		t.Fatalf("解包合法脚本构建产物失败: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(result, "app"))
	if err != nil || string(stored) != "binary" {
		t.Fatalf("脚本构建产物内容不正确: content=%q err=%v", stored, err)
	}
}

func TestExtractScriptArtifactRejectsUnsafeArchiveEntries(t *testing.T) {
	tests := []tar.Header{
		{Name: "/absolute", Typeflag: tar.TypeReg},
		{Name: "../escape", Typeflag: tar.TypeReg},
		{Name: "dist/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "dist/hard", Typeflag: tar.TypeLink, Linkname: "dist/file"},
		{Name: "dist/device", Typeflag: tar.TypeChar},
		{Name: "dist/fifo", Typeflag: tar.TypeFifo},
	}
	for _, header := range tests {
		t.Run(header.Name+string(rune(header.Typeflag)), func(t *testing.T) {
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			if err := writer.WriteHeader(&header); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := extractScriptArtifact(bytes.NewReader(archive.Bytes()), t.TempDir(), "dist", 1024); !errors.Is(err, ErrScriptArtifact) {
				t.Fatalf("危险归档条目未被拒绝: header=%+v err=%v", header, err)
			}
		})
	}
}

func TestExtractScriptArtifactEnforcesTotalSize(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	content := []byte("too-large")
	if err := writer.WriteHeader(&tar.Header{Name: "result.bin", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := extractScriptArtifact(bytes.NewReader(archive.Bytes()), t.TempDir(), "result.bin", 4); !errors.Is(err, ErrScriptArtifact) {
		t.Fatalf("超限脚本构建产物未被拒绝: %v", err)
	}
}
