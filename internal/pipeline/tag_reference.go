package pipeline

import "edo/internal/composeconfig"

// 旧运行保存变量名，新配置保存 ${NAME}；只在读取冻结配置时兼容旧形式。
func composeTagName(value string) string {
	return composeconfig.TagVariableName(value)
}
