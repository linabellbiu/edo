package variablecatalog

import "testing"

func TestCatalogDefinitionsAreUniqueAndNeverExposeValues(t *testing.T) {
	catalog := Snapshot()
	if catalog.SchemaVersion != 1 || len(catalog.Variables) < 30 {
		t.Fatalf("内置变量目录不完整: version=%d variables=%d", catalog.SchemaVersion, len(catalog.Variables))
	}
	ids, syntaxes := map[string]struct{}{}, map[string]struct{}{}
	for _, definition := range catalog.Variables {
		if definition.ID == "" || definition.Name == "" || definition.Syntax == "" ||
			definition.Label == "" || definition.Description == "" || definition.Availability == "" || len(definition.Scopes) == 0 {
			t.Fatalf("内置变量说明不完整: %+v", definition)
		}
		if _, exists := ids[definition.ID]; exists {
			t.Fatalf("内置变量 ID 重复: %s", definition.ID)
		}
		ids[definition.ID] = struct{}{}
		key := string(definition.Kind) + "\x00" + definition.Syntax
		if _, exists := syntaxes[key]; exists {
			t.Fatalf("同类型变量语法重复: %s", definition.Syntax)
		}
		syntaxes[key] = struct{}{}
		if definition.Sensitive {
			t.Fatalf("公开目录不得包含敏感变量: %s", definition.ID)
		}
	}
}

func TestRenderNotificationTemplateOnlyReplacesReferencedKnownVariables(t *testing.T) {
	input := "{{application.name}} / {{task.status}} / {{unknown.value}}"
	result := RenderNotificationTemplate(input, map[string]string{
		"application.name": "商城", "task.status": "成功", "run.id": "不应追加",
	})
	if result != "商城 / 成功 / {{unknown.value}}" {
		t.Fatalf("通知模板替换结果错误: %q", result)
	}
}

func TestReservedScriptEnvironmentNamesMatchExecutionBoundary(t *testing.T) {
	reserved := ReservedScriptEnvironmentNames()
	for _, name := range []string{
		"CI", "HOME", "TMPDIR", "EDO_PIPELINE_RUN_ID", "EDO_APPLICATION_ID", "EDO_GIT_REF",
		"EDO_COMMIT_SHA", "EDO_TARGET_PLATFORM", "EDO_TARGET_ARCH", "GOOS", "GOARCH",
	} {
		if _, exists := reserved[name]; !exists {
			t.Fatalf("缺少脚本保留变量: %s", name)
		}
	}
	if _, exists := reserved["EDO_DEPLOYMENT_ID"]; exists {
		t.Fatal("部署脚本变量不应扩大流水线 Shell 用户变量的保留范围")
	}
}
