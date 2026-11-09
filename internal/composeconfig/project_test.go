package composeconfig

import (
	"errors"
	"strings"
	"testing"
)

func TestProjectNameUsesComposeTemplateSemantics(t *testing.T) {
	for _, item := range []struct{ expression, expected string }{
		{"app-prod", "app-prod"},
		{"${TEAM}-${STAGE:-prod}", "team-prod"},
		{"${TEAM}-${EMPTY:-dev}", "team-dev"},
		{"${TEAM}-${STAGE?missing-stage}", "team-prod"},
	} {
		t.Run(item.expression, func(t *testing.T) {
			values := map[string]string{"TEAM": "team", "EMPTY": ""}
			if item.expression == "${TEAM}-${STAGE?missing-stage}" {
				values["STAGE"] = "prod"
			}
			name, err := ResolveProjectName(map[string]any{"name": item.expression}, "application", "target", func(key string) (string, bool) { value, ok := values[key]; return value, ok })
			if err != nil || name != item.expected {
				t.Fatalf("Compose 名称解析不符: name=%q err=%v", name, err)
			}
		})
	}
}

func TestGeneratedProjectNameStableAndScoped(t *testing.T) {
	lookup := func(string) (string, bool) { return "", false }
	first, err := ResolveProjectName(nil, "app-a", "target-a", lookup)
	if err != nil || !ValidProjectName(first) {
		t.Fatal("未生成有效项目名")
	}
	for i := range 3 {
		same, err := ResolveProjectName(map[string]any{}, "app-a", "target-a", lookup)
		if err != nil || same != first {
			t.Fatalf("第 %d 次部署身份不稳定", i)
		}
	}
	for _, identity := range [][2]string{{"app-b", "target-a"}, {"app-a", "target-b"}} {
		other, _ := ResolveProjectName(nil, identity[0], identity[1], lookup)
		if other == first {
			t.Fatal("不同应用或目标复用了项目名")
		}
	}
}

func TestProjectNameRejectsInvalidOrUnresolvedNamesWithoutLeakingTemplate(t *testing.T) {
	for _, value := range []any{42, []string{"app"}, "bad name", "$(id)", "${MISSING}", "${MISSING:?private-message}", "${TEAM", strings.Repeat("a", 513)} {
		_, err := ResolveProjectName(map[string]any{"name": value}, "app", "target", func(string) (string, bool) { return "", false })
		if !errors.Is(err, ErrProjectName) || err.Error() != ErrProjectName.Error() {
			t.Fatalf("非法名称未被安全拒绝: err=%v", err)
		}
	}
	if _, err := ResolveProjectName(nil, "", "target", nil); !errors.Is(err, ErrProjectName) {
		t.Fatal("缺少应用身份时不能凭目录生成名称")
	}
}
