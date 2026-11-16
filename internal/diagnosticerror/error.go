// Package diagnosticerror 分离安全业务提示和明确允许进入任务日志的诊断。
package diagnosticerror

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"edo/internal/logredact"
)

const MaximumBytes = 8 * 1024

type failure struct {
	summary error
	detail  string
}

func (e *failure) Error() string      { return e.summary.Error() }
func (e *failure) Unwrap() error      { return e.summary }
func (e *failure) Diagnostic() string { return e.detail }

// New 只接受源头已确认属于本次操作的诊断及凭据；原始错误不进入错误链。
// 复用项目已有脱敏规则与标准库，任务范围和公开提示仍由业务层决定。
func New(summary error, detail string, secrets ...string) error {
	if summary == nil {
		return nil
	}
	known := map[string]struct{}{}
	for _, value := range secrets {
		if value == "" {
			continue
		}
		known[value] = struct{}{}
		known[url.QueryEscape(value)] = struct{}{}
		known[url.PathEscape(value)] = struct{}{}
		known[base64.StdEncoding.EncodeToString([]byte(value))] = struct{}{}
		if encoded, err := json.Marshal(value); err == nil && len(encoded) > 2 {
			known[string(encoded[1:len(encoded)-1])] = struct{}{}
		}
		for _, line := range strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == '\r' }) {
			if line != "" {
				known[line] = struct{}{}
			}
		}
	}
	values := make([]string, 0, len(known))
	for value := range known {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	detail = logredact.Text(detail, values...)
	detail = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, strings.ToValidUTF8(detail, "�")))
	if len(detail) > MaximumBytes {
		const suffix = "\n…诊断过长，已截断"
		detail = detail[:MaximumBytes-len(suffix)]
		for !utf8.ValidString(detail) {
			detail = detail[:len(detail)-1]
		}
		detail += suffix
	}
	if detail == "" {
		return summary
	}
	return &failure{summary: summary, detail: detail}
}
