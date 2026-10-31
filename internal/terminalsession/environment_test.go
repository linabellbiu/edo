package terminalsession

import (
	"reflect"
	"strings"
	"testing"
)

func TestShellEnvironmentPreservesContainerValuesWithoutHelperBudgets(t *testing.T) {
	for _, term := range []string{"TERM=vt100", "TERM="} {
		original := []string{"PATH=/fixture/bin", term, "QA_CANARY=fixture-value", "GOMAXPROCS=7", "GOMEMLIMIT=128MiB"}
		got := shellEnvironment(original)
		if !reflect.DeepEqual(got, original) {
			t.Fatal("原容器环境被改写")
		}
		got[0] = "changed"
		if original[0] != "PATH=/fixture/bin" {
			t.Fatal("环境切片复用")
		}
	}
	original := []string{"QA_CANARY=fixture-value"}
	got := shellEnvironment(original)
	if !reflect.DeepEqual(got, []string{"QA_CANARY=fixture-value", "TERM=xterm-256color"}) || len(original) != 1 {
		t.Fatal("缺失 TERM 的处理错误")
	}
	for _, v := range got {
		if strings.HasPrefix(v, "GOMAXPROCS=") || strings.HasPrefix(v, "GOMEMLIMIT=") {
			t.Fatal("工具预算泄露给用户环境")
		}
	}
}
