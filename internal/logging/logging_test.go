package logging

import (
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
