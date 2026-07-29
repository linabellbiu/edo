package logging

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
)

var ErrInvalidLevel = errors.New("日志级别无效")

// RuntimeController 保存可在进程运行期间安全修改的日志过滤设置。
// slog.LevelVar 由标准库 Handler 直接读取，不需要重建全局 Logger。
type RuntimeController struct {
	level      slog.LevelVar
	httpAccess atomic.Bool
}

func New(level string) *slog.Logger {
	logger, _ := NewRuntime(level)
	return logger
}

func NewRuntime(level string) (*slog.Logger, *RuntimeController) {
	controller := &RuntimeController{}
	if err := controller.Apply(level, true); err != nil {
		_ = controller.Apply("info", true)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: &controller.level}))
	return logger, controller
}

func (c *RuntimeController) Apply(level string, httpAccess bool) error {
	parsed, err := parseLevel(level)
	if err != nil {
		return err
	}
	c.level.Set(parsed)
	c.httpAccess.Store(httpAccess)
	return nil
}

func (c *RuntimeController) Level() string {
	switch c.level.Level() {
	case slog.LevelDebug:
		return "debug"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	default:
		return "info"
	}
}

func (c *RuntimeController) HTTPAccessEnabled() bool {
	return c == nil || c.httpAccess.Load()
}

func parseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, ErrInvalidLevel
	}
}
