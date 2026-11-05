package dockerengine

import (
	"encoding/base64"
	"fmt"
	"path"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"edo/internal/envtext"
)

// 仅在原始 Compose 完成白名单校验后添加 EDO 托管的文件，不能借此读取任意主机文件。
func composeWithEnvFiles(source, service, directory string, files map[string]string) (string, []string, error) {
	var document map[string]any
	if err := yaml.Unmarshal([]byte(source), &document); err != nil {
		return "", nil, ErrInvalidComposeYAML
	}
	services, ok := document["services"].(map[string]any)
	if !ok {
		return "", nil, ErrInvalidComposeYAML
	}
	selected, ok := services[service].(map[string]any)
	if !ok {
		return "", nil, ErrInvalidComposeYAML
	}
	names := make([]string, 0, len(files))
	for name := range files {
		if !envtext.ValidFileName(name) {
			return "", nil, ErrInvalidComposeYAML
		}
		names = append(names, name)
	}
	sort.Strings(names)
	references := make([]map[string]string, 0, len(names))
	for _, name := range names {
		references = append(references, map[string]string{"path": path.Join(directory, name), "format": "raw"})
	}
	selected["env_file"] = references
	data, err := yaml.Marshal(document)
	if err != nil {
		return "", nil, ErrInvalidComposeYAML
	}
	return string(data), names, nil
}

// composeSSHEnvFiles 通过标准输入发送临时脚本；文件随 Compose 命令结束清理。
func composeSSHEnvFiles(command string, input ComposeDeployInput) (string, error) {
	content, names, err := composeWithEnvFiles(input.YAML, input.ServiceName, "${EDO_COMPOSE_ENV_DIR}", input.EnvFiles)
	if err != nil {
		return "", err
	}
	var script strings.Builder
	script.WriteString("set -eu\numask 077\nEDO_COMPOSE_ENV_DIR=$(mktemp -d /tmp/edo-compose-env-XXXXXX)\nexport EDO_COMPOSE_ENV_DIR\ntrap 'rm -rf \"$EDO_COMPOSE_ENV_DIR\"' EXIT\n")
	for _, name := range names {
		fmt.Fprintf(&script, "printf '%%s' %s | base64 -d >\"$EDO_COMPOSE_ENV_DIR\"/%s\n", shellQuote(base64.StdEncoding.EncodeToString([]byte(input.EnvFiles[name]))), shellQuote(name))
	}
	fmt.Fprintf(&script, "printf '%%s' %s | base64 -d | %s\n", shellQuote(base64.StdEncoding.EncodeToString([]byte(content))), command)
	return script.String(), nil
}
