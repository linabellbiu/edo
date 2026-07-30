//go:build windows

package artifact

// Windows 不支持用普通文件句柄 fsync 目录；文件自身已经在重命名前同步。
func syncArtifactDirectory(string) error { return nil }
