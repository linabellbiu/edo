package composeconfig

import (
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"edo/internal/terminalassets"
)

func TestTerminalToolsPreservesReadOnlySecurityAndBusinessMounts(t *testing.T) {
	source := "services:\n  api:\n    image: alpine:3.22\n    read_only: true\n    user: '10001:10001'\n    cap_drop: [ALL]\n    security_opt: ['no-new-privileges:true']\n    volumes: ['data:/data:ro', '${EDO_APP_CONFIG_PATH}/config:/config:ro']\n  disabled:\n    init: false\n  shared:\n    pid: service:api\nvolumes:\n  data: {}\n"
	content, changed, err := WithTerminalTools(source)
	if err != nil || !slices.Equal(changed, []string{"api"}) {
		t.Fatal("工具卷修改了不支持终端的服务", changed, err)
	}
	var document map[string]any
	if err := yaml.Unmarshal([]byte(content), &document); err != nil {
		t.Fatal(err)
	}
	services := document["services"].(map[string]any)
	api := services["api"].(map[string]any)
	if api["read_only"] != true || api["user"] != "10001:10001" || api["cap_drop"].([]any)[0] != "ALL" || api["security_opt"].([]any)[0] != "no-new-privileges:true" {
		t.Fatal("业务只读、用户或安全设置被修改")
	}
	mounts := api["volumes"].([]any)
	if len(mounts) != 3 || mounts[0] != "data:/data:ro" || mounts[1] != "${EDO_APP_CONFIG_PATH}/config:/config:ro" {
		t.Fatal("原业务挂载被修改")
	}
	tools := mounts[2].(map[string]any)
	if tools["source"] != terminalassets.VolumeKey || tools["target"] != terminalassets.Directory || tools["read_only"] != true || tools["volume"].(map[string]any)["nocopy"] != true {
		t.Fatal("工具挂载不是只读隔离卷")
	}
	definition := document["volumes"].(map[string]any)[terminalassets.VolumeKey].(map[string]any)
	if len(definition) != 1 || definition["labels"].(map[string]any)[terminalassets.VolumeLabel] != terminalassets.Version {
		t.Fatal("工具卷没有使用 Compose 项目隔离或缺失身份标签")
	}
	for _, name := range []string{"disabled", "shared"} {
		if services[name].(map[string]any)["volumes"] != nil {
			t.Fatal("不支持终端的服务仍挂载工具卷")
		}
	}
	again, changed, err := WithTerminalTools(content)
	if err != nil || again != content || len(changed) != 0 {
		t.Fatal("重复准备产生额外修改", err)
	}
}

func TestTerminalToolsRejectsReservedDirectoryAndVolumeConflicts(t *testing.T) {
	for _, source := range []string{
		"services: {api: {volumes: ['data:/run/edo-terminal:ro']}}",
		"services: {api: {volumes: ['edo_terminal_tools:/run/edo-terminal:rw']}}",
		"services: {api: {volumes: ['edo_terminal_tools:/run/edo-terminal/tool:ro']}}",
		"services: {api: {volumes: [{type: bind, source: edo_terminal_tools, target: /run/edo-terminal, read_only: true}]}}",
		"services: {api: {image: alpine}}\nvolumes: {edo_terminal_tools: {external: true}}",
		"services: {api: {image: alpine}}\nvolumes: {edo_terminal_tools: {name: other}}",
	} {
		if _, _, err := WithTerminalTools(source); err == nil {
			t.Fatal("工具卷覆盖了业务目录或使用共享卷", source)
		}
	}
}

func TestTerminalToolsAddsMissingDefinitionAndLeavesIneligibleServicesUntouched(t *testing.T) {
	source := "services: {api: {volumes: ['edo_terminal_tools:/run/edo-terminal:ro']}}"
	result, changed, err := WithTerminalTools(source)
	if err != nil || !slices.Equal(changed, []string{"api"}) || !strings.Contains(result, terminalassets.VolumeLabel) {
		t.Fatal("已有挂载缺失卷定义时未补齐", err)
	}
	source = "services: {api: {init: false}}"
	result, changed, err = WithTerminalTools(source)
	if err != nil || result != source || len(changed) != 0 {
		t.Fatal("无适用服务时仍然修改 Compose", err)
	}
}
