package sshdeploy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentInputFilesAreAtomicPrivateAndConfined(t *testing.T) {
	directory, resolveErr := filepath.EvalSymlinks(t.TempDir())
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	content := "binary\x00payload\n${IMAGE_TAG}"
	sum := sha256.Sum256([]byte(content))
	file := func(path string) FileInput {
		return FileInput{Directory: directory, Path: path, Digest: fmt.Sprintf("sha256:%x", sum), Content: strings.NewReader(content)}
	}
	if err := stageInputFiles(context.Background(), nil, []FileInput{file("nested/output.tar.gz")}); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(directory, "nested/output.tar.gz")
	data, err := os.ReadFile(name)
	if err != nil || string(data) != content {
		t.Fatal("文件内容不一致", err)
	}
	info, _ := os.Stat(name)
	if info.Mode().Perm() != 0600 {
		t.Fatal("文件权限不是 0600")
	}
	wrong := file("nested/output.tar.gz")
	wrong.Digest = "sha256:" + strings.Repeat("a", 64)
	if err := stageInputFiles(context.Background(), nil, []FileInput{wrong}); err == nil {
		t.Fatal("接受错误摘要")
	}
	data, _ = os.ReadFile(name)
	if string(data) != content {
		t.Fatal("失败覆盖了原文件")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(directory, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../escape", "/tmp/outside", "escape/file"} {
		if err := stageInputFiles(context.Background(), nil, []FileInput{file(path)}); err == nil {
			t.Fatalf("未拒绝越界或链接: %s", path)
		}
	}
}
