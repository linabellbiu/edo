package shellcheck

import (
	"strings"
	"testing"
)

func TestPOSIXSyntaxChecksDoNotExecuteOrEchoContent(t *testing.T) {
	for _, script := range []string{"docker compose --env-file \"${EDO_APP_CONFIG_PATH}/runtime.env\" up -d", "printf '%s' \"${IMAGE_TAG}\"\n", "cat <<'EOF'\nhello\nEOF\n"} {
		if issues := Check(script); len(issues) != 0 {
			t.Fatalf("有效脚本被拒绝: %+v", issues)
		}
	}
	issues := Check("echo ok\nif PRIVATE_SECRET=hidden; then\n")
	if len(issues) != 1 || issues[0].Line != 2 || strings.Contains(issues[0].Message, "PRIVATE_SECRET") {
		t.Fatalf("错误定位或脱敏不正确: %+v", issues)
	}
	if len(Check("x=(one two)")) == 0 {
		t.Fatal("POSIX sh 不支持 Bash 数组")
	}
}
