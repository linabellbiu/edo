package envtext

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"edo/internal/variablecatalog"
)

var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

var envFileName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// SensitiveValues 识别可能含凭据的变量；普通镜像地址、名称和 Tag 不作为秘密。
// 用户明确标记的密钥由调用方另外加入，不因变量名普通而解除保护。
func SensitiveValues(values map[string]string) []string {
	result := make([]string, 0)
	for name, value := range values {
		upper := strings.ToUpper(name)
		if value != "" && (strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") ||
			strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "CREDENTIAL") ||
			strings.HasSuffix(upper, "_PASS") || strings.HasSuffix(upper, "_KEY") ||
			strings.HasSuffix(upper, "_AUTH")) {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

// ValidReference 同时支持变量名称和不带目录的 env 文件名。
func ValidReference(name string) bool {
	return variableName.MatchString(name) || ValidFileName(name)
}

func ValidFileName(name string) bool {
	return envFileName.MatchString(name) && name != "." && name != ".." && !strings.HasPrefix(name, "-")
}

// Parse 按物理行读取字面 KEY=value；不执行插值、转义或多行拼接。
func Parse(source string) (map[string]string, error) {
	if len(source) > 65536 || strings.ContainsRune(source, '\x00') {
		return nil, fmt.Errorf("env 文本过长或包含无效字符")
	}
	values := map[string]string{}
	reserved := variablecatalog.ReservedScriptEnvironmentNames()
	for index, line := range strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		_, system := reserved[key]
		if !found || !variableName.MatchString(key) || system || strings.HasPrefix(key, "EDO_") || strings.ContainsRune(value, '\r') {
			return nil, fmt.Errorf("env 文本第 %d 行无效，请使用 KEY=value，且不能覆盖系统变量", index+1)
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("env 文本第 %d 行的变量名称重复", index+1)
		}
		values[key] = value
		if len(values) > 100 {
			return nil, fmt.Errorf("env 文本最多配置 100 个变量")
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("请至少输入一行 KEY=value")
	}
	return values, nil
}

// Render 配合官方 env_file.format: raw，避免引号、美元符号和反斜杠被再次解释。
func Render(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&out, "%s=%s\n", key, values[key])
	}
	return out.String()
}

// TransportKey 仅作为一次部署期间传递文件内容的内部环境变量名。
func TransportKey(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "EDO_ENV_FILE_" + hex.EncodeToString(sum[:8])
}
