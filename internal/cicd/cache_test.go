package cicd

import (
	"archive/tar"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"edo/internal/model"
)

func TestDependencyCacheIsolationAndSafeRestore(t *testing.T) {
	source, store, restored := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "package-lock.json"), []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "init", source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("初始化仓库失败: %v %s", err, output)
	}
	config := model.WorkflowCacheConfig{Key: "deps", LockFiles: []string{"package-lock.json"}, Paths: []string{"node_modules"}}
	key, err := CacheKey(t.Context(), "project-a", "repo-a", "linux/amd64", "node:24", source, config)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := CacheKey(t.Context(), "project-b", "repo-a", "linux/amd64", "node:24", source, config)
	if key == other {
		t.Fatal("不同项目共享缓存")
	}
	other, _ = CacheKey(t.Context(), "project-a", "repo-a", "linux/arm64", "node:24", source, config)
	if key == other {
		t.Fatal("不同架构共享缓存")
	}
	deps := t.TempDir()
	if err = os.WriteFile(filepath.Join(deps, "dependency.js"), []byte("value"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = SaveCache(store, key, map[string]string{"node_modules": deps}); err != nil {
		t.Fatal(err)
	}
	hit, err := RestoreCache(store, key, restored, config)
	if err != nil || !hit {
		t.Fatalf("缓存没有恢复: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(restored, "node_modules/dependency.js"))
	if err != nil || string(data) != "value" {
		t.Fatal("依赖内容不一致")
	}
	// 缓存包中的越界和链接不能覆盖代码或宿主机文件。
	file, err := os.Create(filepath.Join(store, "unsafe.tar"))
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(file)
	_ = tw.WriteHeader(&tar.Header{Name: "../outside", Typeflag: tar.TypeReg, Mode: 0600, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = file.Close()
	if _, err = RestoreCache(store, "unsafe", restored, config); err == nil {
		t.Fatal("越界缓存被接受")
	}
	for _, name := range []string{"src", ".git", "../node_modules", "node_modules/../../src"} {
		bad := config
		bad.Paths = []string{name}
		if ValidateCache(&bad) == nil {
			t.Fatalf("危险路径被接受: %s", name)
		}
	}
}
