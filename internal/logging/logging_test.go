package logging

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeControllerAppliesLevelAndHTTPAccessImmediately(t *testing.T) {
	logger, controller := NewRuntime("info")
	if !logger.Enabled(context.Background(), slog.LevelInfo) || logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("初始日志级别不是 info")
	}
	if err := controller.Apply("error", false); err != nil {
		t.Fatalf("热更新日志设置失败: %v", err)
	}
	if controller.Level() != "error" || controller.HTTPAccessEnabled() {
		t.Fatalf("运行日志控制器状态错误: level=%s http=%v", controller.Level(), controller.HTTPAccessEnabled())
	}
	if logger.Enabled(context.Background(), slog.LevelWarn) || !logger.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("现有 Logger 没有立即应用新的日志级别")
	}
	if err := controller.Apply("verbose", true); !errors.Is(err, ErrInvalidLevel) {
		t.Fatalf("无效日志级别未被拒绝: %v", err)
	}
}

func TestRuntimeLogsCaptureCurrentProcessOutput(t *testing.T) {
	var output bytes.Buffer
	logger, controller := newRuntime("debug", &output, 10)
	logger.Debug("准备执行任务", "operation", "task_prepare", "task_id", "task-1")
	logger.Warn("消息队列响应缓慢", "operation", "nats_health", "elapsed", "2s")

	entries, hasMore, err := controller.List(Query{Limit: 10, Text: "task-1"})
	if err != nil {
		t.Fatalf("查询系统日志失败: %v", err)
	}
	if hasMore || len(entries) != 1 {
		t.Fatalf("系统日志查询结果错误: entries=%+v has_more=%v", entries, hasMore)
	}
	entry := entries[0]
	if entry.Level != "debug" || entry.Operation != "task_prepare" || entry.Fields["task_id"] != "task-1" {
		t.Fatalf("系统日志结构错误: %+v", entry)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"operation":"task_prepare"`)) {
		t.Fatalf("结构化日志没有继续写入标准输出: %s", output.String())
	}
}

func TestRuntimeLogsUseBoundedNewestFirstPagination(t *testing.T) {
	logger, controller := newRuntime("info", &bytes.Buffer{}, 2)
	logger.Info("first")
	logger.Info("second")
	logger.Error("third")

	entries, hasMore, err := controller.List(Query{Limit: 1})
	if err != nil || !hasMore || len(entries) != 1 || entries[0].Message != "third" {
		t.Fatalf("系统日志首批分页错误: entries=%+v has_more=%v err=%v", entries, hasMore, err)
	}
	older, hasMore, err := controller.List(Query{BeforeID: entries[0].ID, Limit: 10})
	if err != nil || hasMore || len(older) != 1 || older[0].Message != "second" {
		t.Fatalf("系统日志后续分页或容量限制错误: entries=%+v has_more=%v err=%v", older, hasMore, err)
	}
}

func TestRuntimeLogsHonorHotUpdatedLevel(t *testing.T) {
	logger, controller := newRuntime("error", &bytes.Buffer{}, 10)
	logger.Info("不会进入缓冲")
	logger.Error("错误输出")

	entries, _, err := controller.List(Query{Limit: 10})
	if err != nil || len(entries) != 1 || entries[0].Message != "错误输出" {
		t.Fatalf("系统日志没有遵循热更新级别: entries=%+v err=%v", entries, err)
	}
	if _, _, err := controller.List(Query{Limit: 0}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("无效查询条件未被拒绝: %v", err)
	}
}

func TestRuntimeFileSettingsHotUpdateSwitchesDirectory(t *testing.T) {
	firstDirectory := filepath.Join(t.TempDir(), "first")
	secondDirectory := filepath.Join(t.TempDir(), "second")
	logger, controller, err := newRuntimeWithFile("info", io.Discard, 10, FileSettings{
		Enabled: true, Directory: firstDirectory, MaxFileSizeMB: 1, CompressAfterDays: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })

	logger.Info("写入第一个目录", "operation", "file_log_first")
	if err := controller.ApplySettings("debug", false, FileSettings{
		Enabled: true, Directory: secondDirectory, MaxFileSizeMB: 2, CompressAfterDays: 7,
	}); err != nil {
		t.Fatalf("热更新文件日志失败: %v", err)
	}
	logger.Debug("写入第二个目录", "operation", "file_log_second")

	first, err := os.ReadFile(filepath.Join(firstDirectory, activeLogFilename))
	if err != nil || !bytes.Contains(first, []byte("file_log_first")) {
		t.Fatalf("第一个日志目录内容错误: content=%s err=%v", first, err)
	}
	second, err := os.ReadFile(filepath.Join(secondDirectory, activeLogFilename))
	if err != nil || !bytes.Contains(second, []byte("file_log_second")) {
		t.Fatalf("热更新后日志目录内容错误: content=%s err=%v", second, err)
	}
	settings := controller.FileSettings()
	if settings.Directory != secondDirectory || settings.MaxFileSizeMB != 2 || settings.CompressAfterDays != 7 {
		t.Fatalf("热更新后的文件设置错误: %+v", settings)
	}
}

func TestRuntimeFileRotatesDailyAndAtSizeLimit(t *testing.T) {
	directory := t.TempDir()
	activePath := filepath.Join(directory, activeLogFilename)
	if err := os.WriteFile(activePath, []byte("yesterday\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	yesterday := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(activePath, yesterday, yesterday); err != nil {
		t.Fatal(err)
	}
	logger, controller, err := newRuntimeWithFile("info", io.Discard, 10, FileSettings{
		Enabled: true, Directory: directory, MaxFileSizeMB: 1, CompressAfterDays: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })

	large := strings.Repeat("x", 600*1024)
	logger.Info("large-one", "payload", large)
	logger.Info("large-two", "payload", large)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	backups := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "edo-") && strings.HasSuffix(entry.Name(), ".log") {
			backups++
		}
	}
	if backups < 2 {
		t.Fatalf("按天和超过 1 MiB 均应生成历史日志，实际备份数=%d", backups)
	}
}

func TestRuntimeFileCompressesOnlyLogsOlderThanConfiguredDays(t *testing.T) {
	directory := t.TempDir()
	oldPath := filepath.Join(directory, "edo-2026-07-01T00-00-00.000.log")
	recentPath := filepath.Join(directory, "edo-2026-07-04T00-00-00.000.log")
	if err := os.WriteFile(oldPath, []byte("old log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recentPath, []byte("recent log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 5, 12, 0, 0, 0, time.Local)
	oldTime := now.Add(-4 * 24 * time.Hour)
	recentTime := now.Add(-24 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(recentPath, recentTime, recentTime); err != nil {
		t.Fatal(err)
	}

	output := newRuntimeOutput(io.Discard)
	if err := output.configure(FileSettings{
		Enabled: true, Directory: directory, MaxFileSizeMB: 100, CompressAfterDays: 3,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.close() })
	if err := output.compressOldLogs(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("3 天前的原日志仍存在: %v", err)
	}
	compressed, err := os.Open(oldPath + ".gz")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(compressed)
	if err != nil {
		_ = compressed.Close()
		t.Fatal(err)
	}
	content, err := io.ReadAll(reader)
	_ = reader.Close()
	_ = compressed.Close()
	if err != nil || string(content) != "old log\n" {
		t.Fatalf("gzip 日志内容错误: content=%q err=%v", content, err)
	}
	if _, err := os.Stat(recentPath); err != nil {
		t.Fatalf("未满 3 天的日志不应压缩: %v", err)
	}
}
