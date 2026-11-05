package pipeline

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"edo/internal/envtext"
)

// composeStackVariables 只把名称、引用和文件路径写入发布脚本，正文留在执行内存中。
func composeStackVariables(source, project, directory, executionID string, values, files map[string]string) (string, string, error) {
	if len(values)+len(files) == 0 {
		return source, "", nil
	}
	doc, err := parseComposeStack(source)
	if err != nil {
		return "", "", err
	}
	fileNames := make([]string, 0, len(files))
	for name := range files {
		if !envtext.ValidFileName(name) {
			return "", "", ErrInvalidComposeDelivery
		}
		fileNames = append(fileNames, name)
	}
	sort.Strings(fileNames)
	// 目录按不可变运行与任务身份隔离，失败恢复仍可读取上一版本的文件。
	identity := strings.TrimPrefix(sha256Digest([]byte(executionID)), "sha256:")
	relative := path.Join(".edo", "compose", project, "env", identity)
	var writes strings.Builder
	if len(fileNames) > 0 {
		for _, component := range []string{path.Dir(relative), relative} {
			quoted := composeShellLiteral(component)
			fmt.Fprintf(&writes, "[ ! -L %s ] || { echo 'env 文件目录无效' >&2; exit 1; }\nmkdir -p %s\n", quoted, quoted)
		}
	}
	for _, name := range fileNames {
		target := composeShellLiteral(path.Join(relative, name))
		fmt.Fprintf(&writes, "[ ! -L %s ] && [ ! -d %s ] || { echo 'env 文件目标无效' >&2; exit 1; }\n", target, target)
		fmt.Fprintf(&writes, "env_tmp=$(mktemp %s)\nprintf '%%s' \"${%s:?缺少 env 文件内容}\" >\"$env_tmp\" || { rm -f \"$env_tmp\"; echo '写入 env 文件失败' >&2; exit 1; }\nchmod 600 \"$env_tmp\"\nmv -f \"$env_tmp\" %s\nunset %s\n", composeShellLiteral(relative+"/.env-XXXXXX"), envtext.TransportKey(name), target, envtext.TransportKey(name))
	}
	for _, raw := range doc["services"].(map[string]any) {
		service := raw.(map[string]any)
		if len(values) > 0 {
			environment := map[string]any{}
			switch previous := service["environment"].(type) {
			case nil:
			case map[string]any:
				for key, value := range previous {
					environment[key] = value
				}
			case []any:
				for _, item := range previous {
					entry, ok := item.(string)
					if !ok {
						return "", "", ErrInvalidComposeDelivery
					}
					key, value, assigned := strings.Cut(entry, "=")
					if assigned {
						environment[key] = value
					} else {
						environment[key] = nil
					}
				}
			default:
				return "", "", ErrInvalidComposeDelivery
			}
			for key := range values {
				environment[key] = "${" + key + "}"
			}
			service["environment"] = environment
		}
		if len(fileNames) > 0 {
			var references []any
			switch previous := service["env_file"].(type) {
			case nil:
			case string:
				references = append(references, previous)
			case []any:
				references = append(references, previous...)
			default:
				return "", "", ErrInvalidComposeDelivery
			}
			// 同名相对引用由 EDO 托管文件接替，不再依赖原服务器上另有一份文件。
			kept := references[:0]
			for _, reference := range references {
				name, _ := reference.(string)
				if mapping, ok := reference.(map[string]any); ok {
					name, _ = mapping["path"].(string)
				}
				if _, managed := files[strings.TrimPrefix(name, "./")]; !managed {
					kept = append(kept, reference)
				}
			}
			references = kept
			for _, name := range fileNames {
				references = append(references, map[string]any{"path": path.Join(directory, relative, name), "format": "raw"})
			}
			service["env_file"] = references
		}
	}
	encoded, err := yaml.Marshal(doc)
	if err != nil || len(encoded) > maximumComposeStackBytes {
		return "", "", ErrInvalidComposeDelivery
	}
	return string(encoded), writes.String(), nil
}
