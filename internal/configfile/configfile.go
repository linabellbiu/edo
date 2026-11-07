// Package configfile 定义部署配置文件的格式与安全路径，不负责解释文件用途。
package configfile

import (
	"crypto/sha256"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

func ValidKind(kind string) bool {
	switch kind {
	case "config_yaml", "config_toml", "config_env", "config_ini", "config_json", "config_text":
		return true
	}
	return false
}
func ValidPath(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) || path.IsAbs(value) || path.Clean(value) != value || value == "." {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "" || strings.HasPrefix(part, "-") || strings.HasPrefix(part, ".edo") {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
				return false
			}
		}
	}
	return true
}

// ValidFilePath 为历史运行保留绝对路径及主目录引用；新流程的用户输入使用 ValidPath。
func ValidFilePath(value string) bool {
	if len(value) > 512 {
		return false
	}
	if strings.HasPrefix(value, "~/") {
		return ValidPath(strings.TrimPrefix(value, "~/"))
	}
	return ValidPath(strings.TrimPrefix(value, "/"))
}
func TransportKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("EDO_CONFIG_FILE_%x", sum[:12])
}
func Quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

// ApplicationDirectory 固定应用配置目录；应用名称只能占用一个目录层级。
func ApplicationDirectory(name string) (string, error) {
	if name == "" || name == "." || name == ".." || len(name) > 512 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\") || strings.ContainsFunc(name, unicode.IsControl) {
		return "", fmt.Errorf("应用名称不能用于配置目录")
	}
	return "~/edo/" + name + "/config", nil
}

func ValidDirectory(value string) bool {
	if value == "~" || value == "/" {
		return true
	}
	if len(value) > 1024 || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		return false
	}
	relative := ""
	if strings.HasPrefix(value, "~/") {
		relative = strings.TrimPrefix(value, "~/")
	} else if strings.HasPrefix(value, "/") {
		relative = strings.TrimPrefix(value, "/")
	} else {
		return false
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// PrepareDirectoryScript 在目标执行用户的 HOME 下展开路径，逐级拒绝符号链接。
func PrepareDirectoryScript(directory string) (string, error) {
	if !ValidDirectory(directory) {
		return "", fmt.Errorf("部署目录无效")
	}
	base := "/"
	relative := strings.TrimPrefix(directory, "/")
	if directory == "~" || strings.HasPrefix(directory, "~/") {
		base = ""
		relative = strings.TrimPrefix(directory, "~/")
		if directory == "~" {
			relative = ""
		}
	}
	var script strings.Builder
	if base == "" {
		script.WriteString("edo_directory=${HOME:?无法取得目标用户目录}\n")
	} else {
		script.WriteString("edo_directory=/\n")
	}
	script.WriteString("[ -d \"$edo_directory\" ] && [ ! -L \"$edo_directory\" ] || { echo '部署根目录不可用' >&2; exit 1; }\n")
	for _, part := range strings.Split(relative, "/") {
		if part == "" {
			continue
		}
		fmt.Fprintf(&script, "edo_directory=\"${edo_directory%%/}/\"%s\n[ ! -L \"$edo_directory\" ] || { echo '部署目录不能是符号链接' >&2; exit 1; }\nmkdir -p \"$edo_directory\"\n[ -d \"$edo_directory\" ] || exit 1\n", Quote(part))
	}
	script.WriteString("cd \"$edo_directory\"\nunset edo_directory\n")
	return script.String(), nil
}
