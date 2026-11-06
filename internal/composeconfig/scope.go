package composeconfig

import (
	"errors"
	"sort"
	"strings"
)

var ErrInvalidScope = errors.New("Compose 服务范围无效")

// CompletionServices 只识别 Compose 明确声明需要成功完成的一次性依赖，不按服务名称猜测。
func CompletionServices(doc map[string]any, scope []string) ([]string, error) {
	services, ok := doc["services"].(map[string]any)
	if !ok {
		return nil, ErrInvalidScope
	}
	included, completed := map[string]bool{}, map[string]bool{}
	for _, name := range scope {
		included[name] = true
	}
	for _, raw := range services {
		service, ok := raw.(map[string]any)
		if !ok {
			return nil, ErrInvalidScope
		}
		dependencies, _ := service["depends_on"].(map[string]any)
		for name, rawOptions := range dependencies {
			options, ok := rawOptions.(map[string]any)
			if ok && included[name] && options["condition"] == "service_completed_successfully" {
				completed[name] = true
			}
		}
	}
	names := []string{}
	for name := range completed {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// ServiceScope 仅计算服务作用范围，依赖的启动顺序仍由官方 Compose 处理。
func ServiceScope(doc map[string]any, selected []string) ([]string, error) {
	services, ok := doc["services"].(map[string]any)
	if !ok || len(services) == 0 || len(services) > 64 || len(selected) > 64 {
		return nil, ErrInvalidScope
	}
	for _, raw := range services {
		if _, ok := raw.(map[string]any); !ok {
			return nil, ErrInvalidScope
		}
	}
	seen, visiting := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if seen[name] {
			return nil
		}
		if visiting[name] {
			return ErrInvalidScope
		}
		raw, ok := services[name].(map[string]any)
		if !ok {
			return ErrInvalidScope
		}
		visiting[name] = true
		dependencies := []string{}
		switch value := raw["depends_on"].(type) {
		case []any:
			for _, dependency := range value {
				name, ok := dependency.(string)
				if !ok {
					return ErrInvalidScope
				}
				dependencies = append(dependencies, name)
			}
		case map[string]any:
			for name := range value {
				dependencies = append(dependencies, name)
			}
		case nil:
		default:
			return ErrInvalidScope
		}
		for _, key := range []string{"network_mode", "pid", "ipc"} {
			if value, ok := raw[key].(string); ok && strings.HasPrefix(value, "service:") {
				dependencies = append(dependencies, strings.TrimPrefix(value, "service:"))
			}
		}
		for _, dependency := range dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		delete(visiting, name)
		seen[name] = true
		return nil
	}
	roots := append([]string{}, selected...)
	if len(roots) == 0 {
		for name, raw := range services {
			if service := raw.(map[string]any); service["profiles"] == nil {
				roots = append(roots, name)
			}
		}
	}
	for _, name := range roots {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}
