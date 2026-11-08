package pipeline

import "strings"

// 旧运行保存变量名，新配置保存 ${NAME}；只在读取冻结配置时兼容旧形式。
func composeTagName(value string) string {
	if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
		value = strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}")
	}
	if !buildVariableNamePattern.MatchString(value) || strings.HasPrefix(value, "EDO_") {
		return ""
	}
	return value
}
