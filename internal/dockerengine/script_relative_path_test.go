package dockerengine

import (
	"strings"
	"testing"
)

// 只检查原执行端标准path规则，不连接Docker或根据文件系统猜测源文件存在性。
func TestScriptContainerRelativePathContract(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        bool
	}{
		{"默认点", ".", true},
		{"合法嵌套", "src/module/output", true},
		{"空格与中文", "./源码 空间/output.tar", true},
		{"清理后仍在工作区", "src/../output/", true},
		{"字节上限", strings.Repeat("界", 170) + "ab", true},
		{"空路径", "", false},
		{"父目录", "../outside", false},
		{"父目录本身", "..", false},
		{"嵌套越界", "src/../../outside", false},
		{"绝对路径", "/outside", false},
		{"反斜杠", `dir\file`, false},
		{"空字符", "bad\x00path", false},
		{"超长", strings.Repeat("a", 513), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidScriptContainerRelativePath(tc.value); got != tc.want {
				t.Fatalf("执行端相对路径规则不符: got=%t want=%t", got, tc.want)
			}
		})
	}
}
