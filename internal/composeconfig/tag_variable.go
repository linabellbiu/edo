package composeconfig

import (
	"regexp"
	"strings"
)

var tagVariableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// TagVariableName 将用户引用与历史裸名称转换成实际环境变量名，不覆盖其他系统变量。
func TagVariableName(value string) string {
	if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
		value = strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}")
	}
	if !tagVariableName.MatchString(value) || strings.HasPrefix(value, "EDO_") && value != "EDO_IMAGE_TAG" {
		return ""
	}
	return value
}
