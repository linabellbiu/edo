package envtext

import (
	"strings"
	"testing"
)

func TestParseLiteralLines(t *testing.T) {
	values, err := Parse("# 注释\r\nTOKEN=a=b $c 'quote' \\\nEMPTY=\nUNICODE=中文\n")
	if err != nil || values["TOKEN"] != "a=b $c 'quote' \\" || values["EMPTY"] != "" || values["UNICODE"] != "中文" {
		t.Fatalf("文本未按物理行原样解析: %#v %v", values, err)
	}
	for _, text := range []string{"A=private\nA=other", "A=private\ninvalid", "EDO_ENV_FILE_1=private", "HOME=private", "A=private\x00", "# 注释"} {
		if _, err := Parse(text); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("无效文本未安全拒绝: %v", err)
		}
	}
	for _, name := range []string{"../.env", "/tmp/a", ".", "..", "-f", "a\nb"} {
		if ValidFileName(name) {
			t.Fatalf("允许不安全文件名 %q", name)
		}
	}
	if !ValidFileName(".prod.env") {
		t.Fatal("没有支持隐藏 env 文件")
	}
}
