// Package configfile 定义部署配置文件的格式与安全相对路径，不负责解释文件用途。
package configfile

import (
	"crypto/sha256"
	"fmt"
	"path"
	"strings"
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
func TransportKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("EDO_CONFIG_FILE_%x", sum[:12])
}
func Quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func ValidDirectory(value string) bool {
	if strings.HasPrefix(value, "~/") {
		return ValidPath(strings.TrimPrefix(value, "~/"))
	}
	return strings.HasPrefix(value, "/") && ValidPath(strings.TrimPrefix(value, "/"))
}

// PrepareDirectoryScript 在目标执行用户的 HOME 下展开路径，逐级拒绝符号链接。
func PrepareDirectoryScript(directory string) (string, error) {
	if !ValidDirectory(directory) {
		return "", fmt.Errorf("部署目录无效")
	}
	base := "/"
	relative := strings.TrimPrefix(directory, "/")
	if strings.HasPrefix(directory, "~/") {
		base = ""
		relative = strings.TrimPrefix(directory, "~/")
	}
	var script strings.Builder
	if base == "" {
		script.WriteString("edo_directory=${HOME:?无法取得目标用户目录}\n")
	} else {
		script.WriteString("edo_directory=/\n")
	}
	script.WriteString("[ -d \"$edo_directory\" ] && [ ! -L \"$edo_directory\" ] || { echo '部署根目录不可用' >&2; exit 1; }\n")
	for _, part := range strings.Split(relative, "/") {
		fmt.Fprintf(&script, "edo_directory=\"${edo_directory%%/}/\"%s\n[ ! -L \"$edo_directory\" ] || { echo '部署目录不能是符号链接' >&2; exit 1; }\nmkdir -p \"$edo_directory\"\n[ -d \"$edo_directory\" ] || exit 1\n", Quote(part))
	}
	script.WriteString("cd \"$edo_directory\"\nunset edo_directory\n")
	return script.String(), nil
}
