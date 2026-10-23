package task

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

// LogicalSubjectPrefix 是 Job 和 Outbox 中保存的任务逻辑命名空间，不代表实际队列地址。
const LogicalSubjectPrefix = "edo.task"

var kindSubjectPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)*$`)

// TransportSubject 只把与 kind 完全匹配的逻辑任务主题映射到配置的传输命名空间。
// 外部事件与死信不属于任务主题，调用方应原样投递，不能通过此函数改写。
func TransportSubject(logicalSubject, kind, configuredPrefix string) (string, error) {
	if len(kind) < 2 || len(kind) > 64 || !kindSubjectPattern.MatchString(kind) {
		return "", errors.New("任务类型格式无效")
	}
	if logicalSubject != LogicalSubjectPrefix+"."+kind {
		return "", errors.New("任务主题与任务类型不一致")
	}
	if !strings.HasPrefix(configuredPrefix, "edo.") || strings.ContainsAny(configuredPrefix, "*>") ||
		strings.IndexFunc(configuredPrefix, func(value rune) bool { return unicode.IsSpace(value) || unicode.IsControl(value) }) >= 0 {
		return "", errors.New("任务传输主题前缀无效")
	}
	for _, token := range strings.Split(configuredPrefix, ".") {
		if token == "" {
			return "", errors.New("任务传输主题前缀无效")
		}
	}
	return configuredPrefix + "." + kind, nil
}
