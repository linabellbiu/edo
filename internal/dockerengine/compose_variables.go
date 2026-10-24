package dockerengine

import (
	"go.yaml.in/yaml/v3"
	"sort"
	"strings"
)

// 变量只在提交给 Compose 的内存配置中展开，美元符号转义后不会再次插值。
func composeWithEnvironment(source, service string, values map[string]string) (string, error) {
	if len(values) == 0 {
		return source, nil
	}
	if !validScriptEnvironment(values) {
		return "", ErrInvalidComposeYAML
	}
	var document yaml.Node
	if yaml.Unmarshal([]byte(source), &document) != nil || len(document.Content) != 1 {
		return "", ErrInvalidComposeYAML
	}
	root := document.Content[0]
	var selected *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "services" {
			continue
		}
		services := root.Content[i+1]
		for j := 0; j+1 < len(services.Content); j += 2 {
			if services.Content[j].Value == service {
				selected = services.Content[j+1]
			}
		}
	}
	if selected == nil {
		return "", ErrInvalidComposeYAML
	}
	environment := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(selected.Content); i += 2 {
		if selected.Content[i].Value != "environment" {
			continue
		}
		old := selected.Content[i+1]
		if old.Kind == yaml.MappingNode {
			environment = old
		} else if old.Kind == yaml.SequenceNode {
			for _, item := range old.Content {
				key, value, _ := strings.Cut(item.Value, "=")
				environment.Content = append(environment.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
			}
		}
		selected.Content = append(selected.Content[:i], selected.Content[i+2:]...)
		break
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for i := 0; i+1 < len(environment.Content); i += 2 {
			if environment.Content[i].Value == key {
				environment.Content = append(environment.Content[:i], environment.Content[i+2:]...)
				break
			}
		}
		environment.Content = append(environment.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.ReplaceAll(values[key], "$", "$$")})
	}
	selected.Content = append(selected.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "environment"}, environment)
	data, err := yaml.Marshal(&document)
	if err != nil {
		return "", ErrInvalidComposeYAML
	}
	return string(data), nil
}
