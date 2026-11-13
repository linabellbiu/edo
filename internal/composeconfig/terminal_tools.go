package composeconfig

import (
	"errors"
	"path"
	"reflect"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/format"
	"go.yaml.in/yaml/v3"

	"edo/internal/terminalassets"
)

// WithTerminalTools 给可进入终端的服务预留只读工具卷，不修改业务根目录的只读设置。
func WithTerminalTools(source string) (string, []string, error) {
	var document map[string]any
	if err := yaml.Unmarshal([]byte(source), &document); err != nil {
		return "", nil, err
	}
	services, ok := document["services"].(map[string]any)
	if !ok {
		return "", nil, errors.New("Compose 服务定义无效")
	}
	definition := map[string]any{"labels": map[string]any{terminalassets.VolumeLabel: terminalassets.Version}}
	volumes, ok := document["volumes"].(map[string]any)
	if !ok {
		if value := document["volumes"]; value != nil {
			return "", nil, errors.New("Compose 数据卷定义无效")
		}
		volumes = map[string]any{}
	}
	if existing, exists := volumes[terminalassets.VolumeKey]; exists && !reflect.DeepEqual(existing, definition) {
		return "", nil, errors.New("Compose 中的 EDO 终端工具卷名称已被占用")
	}
	var changed []string
	_, defined := volumes[terminalassets.VolumeKey]
	for name, raw := range services {
		service, ok := raw.(map[string]any)
		if !ok {
			return "", nil, errors.New("Compose 服务定义无效")
		}
		if init, explicit := service["init"].(bool); explicit && !init {
			continue
		}
		if pid, explicit := service["pid"]; explicit && pid != "" {
			continue
		}
		mounts, ok := service["volumes"].([]any)
		if !ok && service["volumes"] != nil {
			return "", nil, errors.New("Compose 服务挂载定义无效")
		}
		present := false
		for _, mount := range mounts {
			var target, source, kind string
			var readonly bool
			switch value := mount.(type) {
			case string:
				parsed, err := format.ParseVolume(value)
				if err != nil {
					return "", nil, err
				}
				target, source, kind, readonly = parsed.Target, parsed.Source, parsed.Type, parsed.ReadOnly
			case map[string]any:
				target, _ = value["target"].(string)
				source, _ = value["source"].(string)
				kind, _ = value["type"].(string)
				readonly, _ = value["read_only"].(bool)
			default:
				return "", nil, errors.New("Compose 服务挂载定义无效")
			}
			target = path.Clean(target)
			if strings.HasPrefix(target, terminalassets.Directory+"/") {
				return "", nil, errors.New("Compose 中的 EDO 终端工具目录已被占用")
			}
			if target == terminalassets.Directory {
				if source != terminalassets.VolumeKey || !readonly || kind != "volume" {
					return "", nil, errors.New("Compose 中的 EDO 终端工具目录已被占用")
				}
				present = true
			}
		}
		if !present {
			service["volumes"] = append(mounts, map[string]any{"type": "volume", "source": terminalassets.VolumeKey, "target": terminalassets.Directory, "read_only": true, "volume": map[string]any{"nocopy": true}})
			changed = append(changed, name)
		} else if !defined {
			changed = append(changed, name)
		}
		volumes[terminalassets.VolumeKey] = definition
	}
	if len(changed) == 0 {
		return source, nil, nil
	}
	document["volumes"] = volumes
	sort.Strings(changed)
	content, err := yaml.Marshal(document)
	return string(content), changed, err
}
