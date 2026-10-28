package terminalfiles

import (
	"io"
	"path"
	"strings"
	"unicode/utf8"
)

// CleanPath 只接受目标文件系统内的绝对路径，不把路径解释为命令或通配符。
func CleanPath(value string) (string, error) {
	if value == "" || len(value) > 4096 || !strings.HasPrefix(value, "/") ||
		strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
		return "", ErrInvalidPath
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return "", ErrInvalidPath
		}
	}
	return path.Clean(value), nil
}

// CopyBounded 不缓存整个文件；第 limit+1 个字节用于识别超限输入。
func CopyBounded(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, ErrTooLarge
	}
	return n, nil
}
