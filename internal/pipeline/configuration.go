package pipeline

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"edo/internal/configfile"
	"edo/internal/deployment"
	"edo/internal/model"
)

// 使用稳定的 env_file 引用，新版本新增或删除键时不需要改写冻结的 Compose 正文。
func composeConfiguration(content string, set *deployment.ConfigurationSet) (string, string, error) {
	if set == nil {
		return content, "", nil
	}
	var document map[string]any
	if err := yaml.Unmarshal([]byte(content), &document); err != nil {
		return "", "", ErrInvalidComposeDelivery
	}
	services, ok := document["services"].(map[string]any)
	if !ok {
		return "", "", ErrInvalidComposeDelivery
	}
	for _, raw := range services {
		service, ok := raw.(map[string]any)
		if !ok {
			return "", "", ErrInvalidComposeDelivery
		}
		labels := map[string]any{}
		switch old := service["labels"].(type) {
		case nil:
		case map[string]any:
			for key, value := range old {
				labels[key] = value
			}
		case []any:
			for _, value := range old {
				entry, ok := value.(string)
				if !ok {
					return "", "", ErrInvalidComposeDelivery
				}
				key, value, _ := strings.Cut(entry, "=")
				labels[key] = value
			}
		default:
			return "", "", ErrInvalidComposeDelivery
		}
		labels["edo.configuration.revision"] = "${EDO_CONFIGURATION_REVISION}"
		service["labels"] = labels
		references := []any{}
		switch previous := service["env_file"].(type) {
		case nil:
		case string:
			references = append(references, previous)
		case []any:
			references = append(references, previous...)
		default:
			return "", "", ErrInvalidComposeDelivery
		}
		for _, binding := range set.Bindings {
			if binding.Kind == "config_env" && !set.FilesOnly {
				file, err := configfile.ComposePath(binding.Path)
				if err != nil {
					return "", "", err
				}
				references = append(references, map[string]any{"path": file, "format": "raw"})
			}
		}
		if len(references) > 0 {
			service["env_file"] = references
		}
	}
	encoded, err := yaml.Marshal(document)
	if err != nil || len(encoded) > maximumComposeStackBytes {
		return "", "", ErrInvalidComposeDelivery
	}
	paths := []string{}
	for _, binding := range set.Bindings {
		paths = append(paths, binding.Path)
	}
	writes, err := configfile.WriteScript(set.Directory, paths)
	if err != nil {
		return "", "", err
	}
	writes += "export EDO_CONFIGURATION_DIRECTORY=\"$edo_configuration_directory\"\n"
	return string(encoded), writes, nil
}

func configurationComposeArguments(config model.WorkflowNodeConfig) (string, error) {
	paths, err := configfile.EnvFileParameters(config.DeploymentParameters, config.ConfigurationFiles)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, name := range paths {
		argument, err := configfile.FileArgument(name)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&out, " --env-file %s", argument)
	}
	return out.String(), nil
}
