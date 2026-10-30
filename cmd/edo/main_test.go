package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRuntimeEnvironmentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("EDO_RUNTIME_ENV_TEST=loaded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Unsetenv("EDO_RUNTIME_ENV_TEST")
	t.Cleanup(func() { _ = os.Unsetenv("EDO_RUNTIME_ENV_TEST") })
	if err := loadRuntimeEnvironmentFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("EDO_RUNTIME_ENV_TEST"); got != "loaded" {
		t.Fatalf("本地二进制未读取 .env: %q", got)
	}
	if err := loadRuntimeEnvironmentFile(filepath.Join(t.TempDir(), ".env")); err != nil {
		t.Fatalf("容器环境缺少 .env 时应继续读取进程环境变量: %v", err)
	}
}

func TestLoadRuntimeEnvironmentFileRejectsInvalidFormatWithoutLeakingValues(t *testing.T) {
	const canary = "EDO_RUNTIME_PUBLIC_SYNTHETIC_CONFIG_VALUE"
	for _, content := range []string{
		"; invalid comment\nEDO_DATABASE_DSN=" + canary + "\n",
		"EDO_DATABASE_DSN=\"" + canary + "\n",
		"INVALID-KEY=" + canary + "\n",
	} {
		t.Run("无效配置", func(t *testing.T) {
			file := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
				t.Fatal("写入临时配置失败")
			}
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			err := loadRuntimeEnvironmentFile(file)
			if err == nil {
				t.Fatal("无效环境文件应被拒绝")
			}
			if strings.Contains(err.Error(), canary) || strings.Contains(logs.String(), canary) || strings.Contains(err.Error(), "EDO_DATABASE_DSN=") {
				t.Fatal("环境解析错误不得向终端或日志回显配置原文")
			}
			if err.Error() != "环境配置文件格式无效，请检查 .env 中的变量和注释" {
				t.Fatal("环境解析错误应返回稳定中文提示")
			}
			for _, field := range []string{"operation=runtime_environment_load", "file=", "error_kind=invalid_format"} {
				if !strings.Contains(logs.String(), field) {
					t.Fatalf("诊断缺少安全上下文：%s", field)
				}
			}
		})
	}
}

func TestLoadRuntimeEnvironmentFileReportsFileAccessFailure(t *testing.T) {
	file := t.TempDir()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	err := loadRuntimeEnvironmentFile(file)
	if err == nil || err.Error() != "无法读取环境配置文件，请检查 .env 的访问权限" {
		t.Fatal("读取失败应返回稳定的权限提示")
	}
	if !strings.Contains(logs.String(), "error_kind=file_access") || strings.Contains(logs.String(), "error_kind=invalid_format") {
		t.Fatal("文件读取失败不应被当成解析错误")
	}
}

func TestSameDatabaseRecognizesEquivalentSQLiteDSNs(t *testing.T) {
	if !sameDatabase("sqlite", "data/legacy.db", "sqlite", "file:data/legacy.db?_busy_timeout=5000") {
		t.Fatal("等价 SQLite 路径未被识别")
	}
	if sameDatabase("sqlite", "data/source.db", "sqlite", "data/destination.db") {
		t.Fatal("不同 SQLite 路径被错误识别为同一数据库")
	}
}

func TestReadOnlySQLiteDSN(t *testing.T) {
	dsn, err := readOnlySQLiteDSN("data/legacy.db")
	if err != nil || dsn != "file:data/legacy.db?mode=ro" {
		t.Fatalf("生成只读 SQLite DSN 失败: dsn=%q err=%v", dsn, err)
	}
	if _, err := readOnlySQLiteDSN(":memory:"); err == nil {
		t.Fatal("内存 SQLite 不应作为旧数据来源")
	}
	if _, err := readOnlySQLiteDSN("file:data/legacy.db?mode=rw"); err == nil {
		t.Fatal("可写 SQLite DSN 应被拒绝")
	}
}
