package composeconfig

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/compose-spec/compose-go/v2/template"
)

var ErrProjectName = errors.New("无法自动确定 Compose 项目名，请检查 compose.yaml 的 name 和部署变量")

var projectNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

func ValidProjectName(value string) bool { return projectNamePattern.MatchString(value) }

// ValidateProjectNameExpression 在保存正文时检查静态名称，变量值在部署前按本次运行解析。
func ValidateProjectNameExpression(document map[string]any) error {
	value, exists := document["name"]
	if !exists || value == nil {
		return nil
	}
	name, ok := value.(string)
	if !ok || len(name) > 512 || strings.ContainsAny(name, "\x00\r\n") {
		return ErrProjectName
	}
	if name != "" && !strings.Contains(name, "$") && !ValidProjectName(name) {
		return ErrProjectName
	}
	return nil
}

// ResolveProjectName 只读取 Compose 正文；没有名称时按应用和目标生成稳定身份，不读取旧手填配置。
func ResolveProjectName(document map[string]any, applicationID, targetID string, lookup func(string) (string, bool)) (string, error) {
	if err := ValidateProjectNameExpression(document); err != nil {
		return "", err
	}
	if value, ok := document["name"].(string); ok && value != "" {
		if lookup == nil {
			lookup = func(string) (string, bool) { return "", false }
		}
		name, err := template.SubstituteWithOptions(value, lookup, template.WithoutLogging)
		// 模板错误可能包含配置正文，只向调用方返回稳定诊断，不能把正文或密钥写入日志。
		if err != nil || !ValidProjectName(name) {
			return "", ErrProjectName
		}
		return name, nil
	}
	if applicationID == "" || targetID == "" {
		return "", ErrProjectName
	}
	identity := sha256.Sum256([]byte(applicationID + "\x00" + targetID))
	return fmt.Sprintf("edo-%x", identity[:10]), nil
}
