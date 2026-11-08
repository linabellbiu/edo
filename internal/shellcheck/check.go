// Package shellcheck 使用现有 mvdan Shell 解析器检查 POSIX sh，不执行待检查文本。
package shellcheck

import (
	"errors"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type Issue struct {
	Line    uint   `json:"line"`
	Column  uint   `json:"column"`
	Message string `json:"message"`
}

func Check(script string) []Issue {
	if len(script) > 256*1024 || strings.ContainsRune(script, '\x00') {
		return []Issue{{Line: 1, Column: 1, Message: "脚本不能超过 256 KiB 或包含空字符"}}
	}
	_, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(script), "")
	if err == nil {
		return []Issue{}
	}
	var parseError syntax.ParseError
	if errors.As(err, &parseError) {
		// 原始错误可能引用脚本中的令牌或正文；只返回位置与稳定文案。
		return []Issue{{Line: parseError.Pos.Line(), Column: parseError.Pos.Col(), Message: "Shell 语法无效，请检查引号、括号和语句结束；执行器使用 POSIX sh"}}
	}
	return []Issue{{Line: 1, Column: 1, Message: "Shell 语法检查失败"}}
}
