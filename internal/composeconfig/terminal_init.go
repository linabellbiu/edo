package composeconfig

import (
	"errors"
	"sort"

	"go.yaml.in/yaml/v3"
)

// WithDefaultInit 只补充未指定的 init，不覆盖用户的关闭配置或共享 PID 语义。
func WithDefaultInit(source string) (string, []string, error) {
	var document map[string]any
	if err := yaml.Unmarshal([]byte(source), &document); err != nil {
		return "", nil, err
	}
	services, ok := document["services"].(map[string]any)
	if !ok {
		return "", nil, errors.New("Compose 服务定义无效")
	}
	var changed []string
	for name, raw := range services {
		service, ok := raw.(map[string]any)
		if !ok {
			return "", nil, errors.New("Compose 服务定义无效")
		}
		if _, explicit := service["init"]; explicit {
			continue
		}
		if pid, explicit := service["pid"]; explicit && pid != "" {
			continue
		}
		service["init"] = true
		changed = append(changed, name)
	}
	if len(changed) == 0 {
		return source, nil, nil
	}
	sort.Strings(changed)
	content, err := yaml.Marshal(document)
	return string(content), changed, err
}
