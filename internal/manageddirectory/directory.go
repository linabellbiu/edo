package manageddirectory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrInvalidDirectory  = errors.New("目录配置无效")
	ErrDirectoryNotEmpty = errors.New("目录必须为空或已经由 EDO 管理")
	ErrDirectoryOverlap  = errors.New("工作区、构建、缓存和本地产物目录不能相同或互相包含")
)

const markerName = ".edo-managed-directory"

type CleanupReport struct {
	FilesDeleted  int64 `json:"files_deleted"`
	BytesReleased int64 `json:"bytes_released"`
}

type UsageReport struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

// Prepare 将路径解析为真实绝对路径，并用用途标记限制后续清理范围。
// adopt 只用于启动配置，以兼容升级前已经存在的数据目录；界面新选目录必须为空。
func Prepare(directory, kind string, adopt bool) (string, error) {
	directory, kind = strings.TrimSpace(directory), strings.TrimSpace(kind)
	if directory == "" || kind == "" || strings.ContainsRune(directory, 0) || strings.ContainsRune(kind, 0) {
		return "", ErrInvalidDirectory
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("解析目录失败: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if err := rejectProtectedPath(absolute); err != nil {
		return "", err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("解析目录真实路径失败: %w", err)
	}
	resolved = filepath.Clean(resolved)
	if err := rejectProtectedPath(resolved); err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("读取目录失败: %w", err)
	}
	if !info.IsDir() {
		return "", ErrInvalidDirectory
	}

	marker := filepath.Join(resolved, markerName)
	content, err := os.ReadFile(marker)
	if err == nil {
		if strings.TrimSpace(string(content)) != kind {
			return "", ErrInvalidDirectory
		}
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("读取目录用途标记失败: %w", err)
	}
	if !adopt {
		entries, readErr := os.ReadDir(resolved)
		if readErr != nil {
			return "", fmt.Errorf("检查目录内容失败: %w", readErr)
		}
		if len(entries) != 0 {
			return "", ErrDirectoryNotEmpty
		}
	}
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return Prepare(resolved, kind, adopt)
		}
		return "", fmt.Errorf("创建目录用途标记失败: %w", err)
	}
	if _, err := file.WriteString(kind + "\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(marker)
		return "", fmt.Errorf("写入目录用途标记失败: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(marker)
		return "", fmt.Errorf("关闭目录用途标记失败: %w", err)
	}
	return resolved, nil
}

func ValidateSeparate(directories ...string) error {
	for index := range directories {
		for other := index + 1; other < len(directories); other++ {
			if overlaps(directories[index], directories[other]) {
				return ErrDirectoryOverlap
			}
		}
	}
	return nil
}

func ClearContents(directory, kind string) (CleanupReport, error) {
	resolved, err := verify(directory, kind)
	if err != nil {
		return CleanupReport{}, err
	}
	usage, err := inspect(resolved, markerName)
	if err != nil {
		return CleanupReport{}, err
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return CleanupReport{}, fmt.Errorf("打开受管目录失败: %w", err)
	}
	defer root.Close()
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return CleanupReport{}, fmt.Errorf("读取受管目录失败: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() == markerName {
			continue
		}
		if err := root.RemoveAll(entry.Name()); err != nil {
			return CleanupReport{}, fmt.Errorf("清理受管目录失败: %w", err)
		}
	}
	return CleanupReport{FilesDeleted: usage.Files, BytesReleased: usage.Bytes}, nil
}

func ClearSubdirectory(directory, kind, relative string) (CleanupReport, error) {
	resolved, err := verify(directory, kind)
	if err != nil {
		return CleanupReport{}, err
	}
	relative = filepath.Clean(relative)
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return CleanupReport{}, ErrInvalidDirectory
	}
	target := filepath.Join(resolved, relative)
	usage, err := inspect(target, "")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return CleanupReport{}, err
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return CleanupReport{}, fmt.Errorf("打开受管目录失败: %w", err)
	}
	defer root.Close()
	if err := root.RemoveAll(relative); err != nil {
		return CleanupReport{}, fmt.Errorf("清理受管子目录失败: %w", err)
	}
	if err := root.MkdirAll(relative, 0o700); err != nil {
		return CleanupReport{}, fmt.Errorf("重建受管子目录失败: %w", err)
	}
	return CleanupReport{FilesDeleted: usage.Files, BytesReleased: usage.Bytes}, nil
}

func InspectContents(directory, kind string) (UsageReport, error) {
	resolved, err := verify(directory, kind)
	if err != nil {
		return UsageReport{}, err
	}
	return inspect(resolved, markerName)
}

func InspectSubdirectory(directory, kind, relative string) (UsageReport, error) {
	resolved, err := verify(directory, kind)
	if err != nil {
		return UsageReport{}, err
	}
	relative = filepath.Clean(relative)
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return UsageReport{}, ErrInvalidDirectory
	}
	usage, err := inspect(filepath.Join(resolved, relative), "")
	if errors.Is(err, os.ErrNotExist) {
		return UsageReport{}, nil
	}
	return usage, err
}

func verify(directory, kind string) (string, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Clean(directory))
	if err != nil {
		return "", fmt.Errorf("解析受管目录失败: %w", err)
	}
	if err := rejectProtectedPath(resolved); err != nil {
		return "", err
	}
	content, err := os.ReadFile(filepath.Join(resolved, markerName))
	if err != nil || strings.TrimSpace(string(content)) != strings.TrimSpace(kind) {
		return "", ErrInvalidDirectory
	}
	return resolved, nil
}

func inspect(root, skipName string) (UsageReport, error) {
	report := UsageReport{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || (skipName != "" && entry.Name() == skipName) || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		report.Files++
		report.Bytes += info.Size()
		return nil
	})
	if err != nil {
		return UsageReport{}, fmt.Errorf("统计受管目录失败: %w", err)
	}
	return report, nil
}

func overlaps(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if left == right {
		return true
	}
	relative, err := filepath.Rel(left, right)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return true
	}
	relative, err = filepath.Rel(right, left)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func rejectProtectedPath(path string) error {
	path = filepath.Clean(path)
	if path == "." || path == string(filepath.Separator) || filepath.Dir(path) == path {
		return ErrInvalidDirectory
	}
	protected := make([]string, 0, 3)
	if home, err := os.UserHomeDir(); err == nil {
		protected = append(protected, filepath.Clean(home))
	}
	if working, err := os.Getwd(); err == nil {
		protected = append(protected, filepath.Clean(working))
	}
	protected = append(protected, filepath.Clean(os.TempDir()))
	for _, current := range protected {
		if path == current {
			return ErrInvalidDirectory
		}
	}
	return nil
}
