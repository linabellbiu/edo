package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
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
