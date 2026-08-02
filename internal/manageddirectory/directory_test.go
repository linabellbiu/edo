package manageddirectory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareRequiresEmptyDirectoryForRuntimeChange(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "user.txt"), []byte("保留"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(directory, "cache", false); !errors.Is(err, ErrDirectoryNotEmpty) {
		t.Fatalf("非空普通目录必须拒绝接管: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "user.txt")); err != nil {
		t.Fatalf("用户文件不能被修改: %v", err)
	}
}

func TestClearContentsRequiresMatchingMarker(t *testing.T) {
	directory := t.TempDir()
	resolved, err := Prepare(directory, "build", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resolved, "workspace.txt"), []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ClearContents(resolved, "cache"); !errors.Is(err, ErrInvalidDirectory) {
		t.Fatalf("用途不匹配时必须拒绝清理: %v", err)
	}
	report, err := ClearContents(resolved, "build")
	if err != nil {
		t.Fatal(err)
	}
	if report.FilesDeleted != 1 || report.BytesReleased != 4 {
		t.Fatalf("清理统计错误: %+v", report)
	}
	if _, err := os.Stat(filepath.Join(resolved, markerName)); err != nil {
		t.Fatalf("清理后必须保留用途标记: %v", err)
	}
}

func TestValidateSeparateRejectsNestedDirectories(t *testing.T) {
	root := t.TempDir()
	if err := ValidateSeparate(filepath.Join(root, "build"), filepath.Join(root, "build", "cache")); !errors.Is(err, ErrDirectoryOverlap) {
		t.Fatalf("嵌套目录必须拒绝: %v", err)
	}
}
