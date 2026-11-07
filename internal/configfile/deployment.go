package configfile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"

	"edo/internal/model"
)

var ErrParameters = errors.New("部署参数无效；目前支持 --env-file 配置文件路径")

// EnvFileParameters 使用现有 Shell 解析库识别引号，但不执行展开、命令或重定向。
// 只能读取本次明确选择的配置，不能借参数读取任意主机文件。
func EnvFileParameters(value string, refs []model.DeploymentConfigFile) ([]string, error) {
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
			case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp, *syntax.ProcSubst, *syntax.ExtGlob:
				valid = false
			}
			return valid
		})
		if !valid {
			return nil, ErrParameters
		}
		arg, err := expand.Literal(nil, word)
		if err != nil || strings.ContainsAny(arg, "\x00\r\n") {
			return nil, ErrParameters
		}
		args = append(args, arg)
	}
	selected := map[string]bool{}
	for _, ref := range refs {
		selected[ref.Path] = true
	}
	paths := []string{}
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
		if !ValidPath(path) || !selected[path] {
			return nil, ErrParameters
		}
		paths = append(paths, path)
	}
	return paths, nil
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

// WriteScript 仅引用通过独立输入传输的正文，生成脚本及发布快照不包含明文。
// 调用方在权限和运行锁内执行；始终使用独立版本目录，不覆盖其他发布的文件。
func WriteScript(directory string, paths []string) (string, error) {
	prepare, err := PrepareDirectoryScript(directory)
	if err != nil {
		return "", err
	}
	names := append([]string{}, paths...)
	sort.Strings(names)
	var script strings.Builder
	script.WriteString("edo_previous_directory=$PWD\n")
	script.WriteString(prepare)
	script.WriteString("umask 077\n")
	for _, name := range names {
		if !ValidPath(name) {
			return "", errors.New("配置文件路径无效")
		}
		components := strings.Split(name, "/")
		for i := 1; i < len(components); i++ {
			dir := Quote(strings.Join(components[:i], "/"))
			fmt.Fprintf(&script, "[ ! -L %s ] || exit 1\nmkdir -p %s\n", dir, dir)
		}
		target := Quote(name)
		fmt.Fprintf(&script, "[ ! -L %s ] && [ ! -d %s ] || exit 1\nedo_config_tmp=$(mktemp '.edo-config-XXXXXXXX')\nprintf '%%s' \"${%s?缺少配置文件内容}\" >\"$edo_config_tmp\" || { rm -f \"$edo_config_tmp\"; exit 1; }\nchmod 600 \"$edo_config_tmp\"\nmv -f \"$edo_config_tmp\" %s\nunset %s\n", target, target, TransportKey(name), target, TransportKey(name))
	}
	script.WriteString("edo_configuration_directory=$PWD\ncd \"$edo_previous_directory\"\nunset edo_previous_directory edo_config_tmp\n")
	return script.String(), nil
}
