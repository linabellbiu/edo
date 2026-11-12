package configfile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"

	"edo/internal/model"
)

var ErrParameters = errors.New("部署参数无效；请使用 --env-file 相对路径或 --env-file ${EDO_APP_CONFIG_PATH}/相对路径")

type EnvFileParameter struct {
	Path          string
	Configuration bool
}

// Argument 保留工作目录相对路径，只有配置目录引用才使用内置变量。
func (p EnvFileParameter) Argument() (string, error) {
	if p.Configuration {
		return FileArgument(p.Path)
	}
	if !ValidPath(p.Path) {
		return "", ErrParameters
	}
	return Quote(p.Path), nil
}

func EnvFileParameters(value string, refs []model.DeploymentConfigFile) ([]string, error) {
	parameters, err := ParseEnvFileParameters(value, refs)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(parameters))
	for _, parameter := range parameters {
		paths = append(paths, parameter.Path)
	}
	return paths, nil
}

// ParseEnvFileParameters 使用现有 Shell 解析库识别引号，不执行命令或重定向。
// 普通相对路径在部署工作目录解析，所选配置的简写和显式内置变量继续引用配置目录。
func ParseEnvFileParameters(value string, refs []model.DeploymentConfigFile) ([]EnvFileParameter, error) {
	if len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
		return nil, ErrParameters
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX), syntax.KeepComments(true)).Parse(strings.NewReader("edo "+value), "")
	if err != nil || len(file.Stmts) != 1 || len(file.Last) != 0 {
		return nil, ErrParameters
	}
	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(stmt.Comments) > 0 || stmt.Background || stmt.Negated || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) > 0 || len(call.Assigns) > 0 {
		return nil, ErrParameters
	}
	args := []string{}
	for _, word := range call.Args[1:] {
		valid := true
		syntax.Walk(word, func(node syntax.Node) bool {
			switch node.(type) {
			case *syntax.ParamExp:
				parameter := node.(*syntax.ParamExp)
				if parameter.Param == nil || parameter.Param.Value != "EDO_APP_CONFIG_PATH" || parameter.Excl || parameter.Length || parameter.Width || parameter.IsSet || parameter.Flags != nil || parameter.NestedParam != nil || parameter.Index != nil || len(parameter.Modifiers) != 0 || parameter.Slice != nil || parameter.Repl != nil || parameter.Exp != nil || parameter.Names != 0 {
					valid = false
				}
			case *syntax.CmdSubst, *syntax.ArithmExp, *syntax.ProcSubst, *syntax.ExtGlob:
				valid = false
			}
			return valid
		})
		if !valid {
			return nil, ErrParameters
		}
		// 保留 ~/ 原文，由部署目标展开，不能使用 EDO 进程的主目录。
		arg, err := expand.Literal(&expand.Config{Env: expand.ListEnviron("HOME=~", "EDO_APP_CONFIG_PATH=${EDO_APP_CONFIG_PATH}")}, word)
		if err != nil || strings.ContainsAny(arg, "\x00\r\n") {
			return nil, ErrParameters
		}
		args = append(args, arg)
	}
	selected := map[string]bool{}
	for _, ref := range refs {
		selected[ref.Path] = true
	}
	parameters := []EnvFileParameter{}
	for i := 0; i < len(args); i++ {
		key, path, equals := strings.Cut(args[i], "=")
		if key != "--env-file" {
			return nil, ErrParameters
		}
		if !equals {
			i++
			if i >= len(args) {
				return nil, ErrParameters
			}
			path = args[i]
		}
		configuration := strings.HasPrefix(path, "${EDO_APP_CONFIG_PATH}/") || strings.HasPrefix(path, "$EDO_APP_CONFIG_PATH/")
		path = strings.TrimPrefix(strings.TrimPrefix(path, "${EDO_APP_CONFIG_PATH}/"), "$EDO_APP_CONFIG_PATH/")
		for strings.HasPrefix(path, "./") {
			path = strings.TrimPrefix(path, "./")
		}
		configuration = configuration || selected[path]
		if !ValidPath(path) && !(selected[path] && ValidFilePath(path)) {
			return nil, ErrParameters
		}
		parameters = append(parameters, EnvFileParameter{Path: path, Configuration: configuration})
	}
	return parameters, nil
}

// RevisionDigest 只摘要引用和版本，不摘要配置正文，避免低熵密钥被离线猜测。
func RevisionDigest(bindings []model.DeploymentConfigBinding) string {
	if len(bindings) == 0 {
		return ""
	}
	payload, _ := json.Marshal(bindings)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Directory 仅恢复旧运行快照的路径；新发布不再使用该目录布局。
func Directory(configured, projectID, applicationID, environmentID, runID, nodeID string) string {
	if configured == "" {
		configured = "~/edo/deply"
	}
	if environmentID == "" {
		environmentID = "default"
	}
	// 节点标识来自流水线，使用摘要避免名称中的路径字符改变目录层级。
	sum := sha256.Sum256([]byte(nodeID))
	return strings.TrimSuffix(configured, "/") + "/" + projectID + "/" + applicationID + "/" + environmentID + "/" + runID + fmt.Sprintf("/%x", sum[:8])
}

// FileArgument 在目标 Shell 内解析主目录，不执行用户路径中的任何表达式。
func FileArgument(name string) (string, error) {
	if !ValidFilePath(name) {
		return "", errors.New("配置文件路径无效")
	}
	if strings.HasPrefix(name, "~/") {
		return `"${HOME:?无法取得目标用户目录}/"` + Quote(strings.TrimPrefix(name, "~/")), nil
	}
	if path.IsAbs(name) {
		return Quote(name), nil
	}
	return `"${EDO_APP_CONFIG_PATH:?缺少应用配置目录}/"` + Quote(name), nil
}

// ComposePath 保留用户指定的位置，仅将目标主目录交给目标 Shell 展开。
func ComposePath(name string) (string, error) {
	if !ValidFilePath(name) {
		return "", errors.New("配置文件路径无效")
	}
	if strings.HasPrefix(name, "~/") {
		return "${EDO_CONFIGURATION_HOME}/" + strings.TrimPrefix(name, "~/"), nil
	}
	if path.IsAbs(name) {
		return name, nil
	}
	return "${EDO_APP_CONFIG_PATH}/" + name, nil
}

// WriteScript 仅引用通过独立输入传输的正文，生成脚本及发布快照不包含明文。
// 调用方在权限和运行锁内执行；文件只写入用户指定的位置。
func WriteScript(directory string, paths []string) (string, error) {
	prepare, err := PrepareDirectoryScript(directory)
	if err != nil {
		return "", err
	}
	names := append([]string{}, paths...)
	sort.Strings(names)
	var script strings.Builder
	script.WriteString("edo_previous_directory=$PWD\numask 077\n")
	script.WriteString(prepare)
	script.WriteString("edo_configuration_directory=$PWD\nexport EDO_APP_CONFIG_PATH=\"$edo_configuration_directory\"\nexport EDO_CONFIGURATION_DIRECTORY=\"$edo_configuration_directory\"\n")
	for _, name := range names {
		if !ValidFilePath(name) {
			return "", errors.New("配置文件路径无效")
		}
		file := name
		if path.IsAbs(name) || strings.HasPrefix(name, "~/") {
			parent, err := PrepareDirectoryScript(path.Dir(name))
			if err != nil {
				return "", err
			}
			script.WriteString(parent)
			file = path.Base(name)
		} else {
			script.WriteString("cd \"$edo_configuration_directory\"\n")
		}
		components := strings.Split(file, "/")
		for i := 1; i < len(components); i++ {
			dir := Quote(strings.Join(components[:i], "/"))
			fmt.Fprintf(&script, "[ ! -L %s ] || exit 1\nmkdir -p %s\n", dir, dir)
		}
		if len(components) > 1 {
			fmt.Fprintf(&script, "cd %s\n", Quote(path.Dir(file)))
		}
		target := Quote(path.Base(file))
		fmt.Fprintf(&script, "[ ! -L %s ] && [ ! -d %s ] || exit 1\nedo_config_tmp=$(mktemp '.edo-config-XXXXXXXX')\nprintf '%%s' \"${%s?缺少配置文件内容}\" >\"$edo_config_tmp\" || { rm -f \"$edo_config_tmp\"; exit 1; }\nchmod 600 \"$edo_config_tmp\"\nmv -f \"$edo_config_tmp\" %s\nunset %s\n", target, target, TransportKey(name), target, TransportKey(name))
	}
	script.WriteString("export EDO_CONFIGURATION_HOME=\"${HOME-}\"\ncd \"$edo_previous_directory\"\nunset edo_previous_directory edo_config_tmp\n")
	return script.String(), nil
}
