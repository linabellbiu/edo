package logging

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/libtnb/logrotate"
)

const (
	DefaultFileDirectory     = "logs"
	DefaultMaxFileSizeMB     = 100
	DefaultCompressAfterDays = 3
	maxFileSizeMB            = 10 * 1024
	maxCompressAfterDays     = 3650
	activeLogFilename        = "edo.log"
)

var ErrInvalidFileSettings = errors.New("文件日志设置无效")

// FileSettings 控制运行日志文件输出。按自然日和大小的切分由 logrotate 完成，
// CompressAfterDays 只影响已切分文件，不会压缩当前正在写入的 edo.log。
type FileSettings struct {
	Enabled           bool   `json:"file_enabled"`
	Directory         string `json:"file_directory"`
	MaxFileSizeMB     int    `json:"max_file_size_mb"`
	CompressAfterDays int    `json:"compress_after_days"`
}

func DefaultFileSettings() FileSettings {
	return FileSettings{
		Enabled:           true,
		Directory:         DefaultFileDirectory,
		MaxFileSizeMB:     DefaultMaxFileSizeMB,
		CompressAfterDays: DefaultCompressAfterDays,
	}
}

func NormalizeFileSettings(settings FileSettings) (FileSettings, error) {
	settings.Directory = strings.TrimSpace(settings.Directory)
	if settings.Directory == "" || len(settings.Directory) > 1024 || strings.ContainsRune(settings.Directory, 0) ||
		settings.MaxFileSizeMB < 1 || settings.MaxFileSizeMB > maxFileSizeMB ||
		settings.CompressAfterDays < 1 || settings.CompressAfterDays > maxCompressAfterDays {
		return FileSettings{}, ErrInvalidFileSettings
	}
	settings.Directory = filepath.Clean(settings.Directory)
	return settings, nil
}

type runtimeOutput struct {
	mu       sync.Mutex
	console  io.Writer
	file     *logrotate.Writer
	settings FileSettings
}

func newRuntimeOutput(console io.Writer) *runtimeOutput {
	settings := DefaultFileSettings()
	settings.Enabled = false
	return &runtimeOutput{console: console, settings: settings}
}

func (w *runtimeOutput) Write(content []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	written := len(content)
	var result error
	if w.console != nil {
		n, err := w.console.Write(content)
		if n < written {
			written = n
		}
		result = errors.Join(result, err)
	}
	if w.file != nil {
		n, err := w.file.Write(content)
		if n < written {
			written = n
		}
		result = errors.Join(result, err)
	}
	return written, result
}

func (w *runtimeOutput) configure(settings FileSettings) error {
	normalized, err := NormalizeFileSettings(settings)
	if err != nil {
		return err
	}

	// Keep writes paused while a replacement writer inspects or rotates the
	// active file. This prevents the old descriptor from writing into a file
	// that the new writer has just renamed during a hot update.
	w.mu.Lock()
	var next *logrotate.Writer
	if normalized.Enabled {
		next, err = logrotate.New(
			filepath.Join(normalized.Directory, activeLogFilename),
			logrotate.WithMaxSize(int64(normalized.MaxFileSizeMB)*logrotate.MB),
			logrotate.WithRotateEvery(24*time.Hour),
			logrotate.WithLocation(time.Local),
			logrotate.WithFileMode(0o600),
			logrotate.WithErrorHandler(func(err error) {
				_, _ = fmt.Fprintf(os.Stderr, "EDO 日志切分失败: %v\n", err)
			}),
		)
		if err != nil {
			w.mu.Unlock()
			return fmt.Errorf("初始化滚动日志文件失败: %w", err)
		}
	}

	previous := w.file
	w.file = next
	w.settings = normalized
	w.mu.Unlock()
	if previous != nil {
		if err := previous.Close(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "EDO 关闭旧日志文件失败: %v\n", err)
		}
	}
	return nil
}

func (w *runtimeOutput) fileSettings() FileSettings {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.settings
}

func (w *runtimeOutput) close() error {
	w.mu.Lock()
	current := w.file
	w.file = nil
	w.mu.Unlock()
	if current == nil {
		return nil
	}
	return current.Close()
}

func (w *runtimeOutput) compressOldLogs(now time.Time) error {
	settings := w.fileSettings()
	if !settings.Enabled {
		return nil
	}
	entries, err := os.ReadDir(settings.Directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取运行日志目录失败: %w", err)
	}
	cutoff := now.Add(-time.Duration(settings.CompressAfterDays) * 24 * time.Hour)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "edo-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("检查历史日志 %s 失败: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
			continue
		}
		if err := gzipLogFile(filepath.Join(settings.Directory, name)); err != nil {
			return err
		}
	}
	return nil
}

func gzipLogFile(path string) (result error) {
	destination := path + ".gz"
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查压缩日志 %s 失败: %w", destination, err)
	}

	source, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开待压缩日志 %s 失败: %w", path, err)
	}
	defer source.Close()

	target, err := os.CreateTemp(filepath.Dir(path), ".edo-compress-*.tmp")
	if err != nil {
		return fmt.Errorf("创建日志压缩临时文件失败: %w", err)
	}
	temporary := target.Name()
	if err := target.Chmod(0o600); err != nil {
		_ = target.Close()
		_ = os.Remove(temporary)
		return fmt.Errorf("设置日志压缩临时文件权限失败: %w", err)
	}
	defer func() {
		_ = target.Close()
		if result != nil {
			_ = os.Remove(temporary)
		}
	}()

	compressor := gzip.NewWriter(target)
	if _, err := io.Copy(compressor, source); err != nil {
		_ = compressor.Close()
		return fmt.Errorf("压缩日志 %s 失败: %w", path, err)
	}
	if err := compressor.Close(); err != nil {
		return fmt.Errorf("完成日志压缩失败: %w", err)
	}
	if err := target.Sync(); err != nil {
		return fmt.Errorf("同步压缩日志失败: %w", err)
	}
	if err := target.Close(); err != nil {
		return fmt.Errorf("关闭压缩日志失败: %w", err)
	}
	if err := os.Rename(temporary, destination); err != nil {
		return fmt.Errorf("发布压缩日志失败: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("删除已压缩原日志失败: %w", err)
	}
	return nil
}

func (c *RuntimeController) RunFileMaintenance(ctx context.Context) error {
	if c == nil || c.output == nil {
		return nil
	}
	run := func() {
		if err := c.output.compressOldLogs(time.Now()); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "EDO 历史日志压缩失败: %v\n", err)
		}
	}
	run()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.maintenanceWake:
			run()
		case <-ticker.C:
			run()
		}
	}
}
