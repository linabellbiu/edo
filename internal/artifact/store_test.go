package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestLocalStorePutUsesContentAddressAndDeduplicates(t *testing.T) {
	store, err := NewLocalStore(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("初始化本地制品存储失败: %v", err)
	}
	content := []byte("immutable artifact")
	expectedHash := sha256.Sum256(content)
	expectedDigest := "sha256:" + hex.EncodeToString(expectedHash[:])

	first, err := store.Put(context.Background(), strings.NewReader(string(content)))
	if err != nil {
		t.Fatalf("保存制品失败: %v", err)
	}
	if first.Digest != expectedDigest || first.SizeBytes != int64(len(content)) || !first.Created {
		t.Fatalf("首次保存结果错误: %+v", first)
	}
	second, err := store.Put(context.Background(), strings.NewReader(string(content)))
	if err != nil {
		t.Fatalf("重复保存制品失败: %v", err)
	}
	if second.StorageKey != first.StorageKey || second.Digest != first.Digest || second.Created {
		t.Fatalf("同摘要内容未被复用: first=%+v second=%+v", first, second)
	}
	file, err := store.Open(first.StorageKey)
	if err != nil {
		t.Fatalf("打开制品失败: %v", err)
	}
	defer file.Close()
	actual, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("读取制品失败: %v", err)
	}
	if string(actual) != string(content) {
		t.Fatalf("制品内容错误: %q", actual)
	}
	if runtime.GOOS != "windows" {
		info, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o400 {
			t.Fatalf("内容寻址对象创建后仍可被普通写入: mode=%o", info.Mode().Perm())
		}
	}
}

func TestLocalStoreRejectsOversizedAndUnsafeInput(t *testing.T) {
	store, err := NewLocalStore(t.TempDir(), 4)
	if err != nil {
		t.Fatalf("初始化本地制品存储失败: %v", err)
	}
	if _, err := store.Put(context.Background(), strings.NewReader("12345")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("超限制品未被拒绝: %v", err)
	}
	temporaryEntries, err := os.ReadDir(store.temp)
	if err != nil {
		t.Fatalf("读取临时目录失败: %v", err)
	}
	if len(temporaryEntries) != 0 {
		t.Fatalf("超限制品遗留临时文件: %+v", temporaryEntries)
	}
	if _, err := store.Open("../../etc/passwd"); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("目录穿越存储键未被拒绝: %v", err)
	}
}

func TestLocalStoreRejectsCorruptObjectAtExistingDigest(t *testing.T) {
	store, err := NewLocalStore(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("初始化本地制品存储失败: %v", err)
	}
	first, err := store.Put(context.Background(), strings.NewReader("expected"))
	if err != nil {
		t.Fatalf("保存初始制品失败: %v", err)
	}
	path, err := store.resolve(first.StorageKey)
	if err != nil {
		t.Fatalf("解析制品存储路径失败: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("准备模拟同 UID 篡改失败: %v", err)
	}
	if err := os.WriteFile(path, []byte("corrupt!"), 0o600); err != nil {
		t.Fatalf("准备损坏制品失败: %v", err)
	}
	if _, err := store.Put(context.Background(), strings.NewReader("expected")); err == nil {
		t.Fatal("同摘要路径中的损坏内容未被拒绝")
	}
}

func TestLocalStoreStopsOnCanceledContext(t *testing.T) {
	store, err := NewLocalStore(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("初始化本地制品存储失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Put(ctx, strings.NewReader("content")); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消信号未终止制品写入: %v", err)
	}
}
