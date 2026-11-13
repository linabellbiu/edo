package composeconfig

import (
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestDefaultInitPreservesExplicitSettingsAndSharedPID(t *testing.T) {
	source := "services:\n  api:\n    image: alpine:3.22\n  disabled:\n    image: alpine:3.22\n    init: false\n  enabled:\n    image: alpine:3.22\n    init: true\n  shared:\n    image: alpine:3.22\n    pid: host\n"
	content, changed, err := WithDefaultInit(source)
	if err != nil || !slices.Equal(changed, []string{"api"}) {
		t.Fatal("默认 init 修改了其他服务", changed, err)
	}
	var document map[string]any
	if err := yaml.Unmarshal([]byte(content), &document); err != nil {
		t.Fatal(err)
	}
	services := document["services"].(map[string]any)
	if services["api"].(map[string]any)["init"] != true || services["disabled"].(map[string]any)["init"] != false || services["enabled"].(map[string]any)["init"] != true {
		t.Fatal("显式 init 设置丢失")
	}
	if _, exists := services["shared"].(map[string]any)["init"]; exists {
		t.Fatal("共享 PID 被强行启用 init")
	}
	again, next, err := WithDefaultInit(content)
	if err != nil || again != content || len(next) != 0 {
		t.Fatal("重复准备修改了 Compose")
	}
}
