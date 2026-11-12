package pipeline

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"edo/internal/dockerengine"
)

func (s *Service) logComposeImageFailure(ctx context.Context, prepared *executionContext, err error) string {
	diagnostic := string(redactLogBytes([]byte(err.Error()), normalizeLogRedactions(composeExecutionLogRedactions(prepared))))
	diagnostic = truncateUTF8Bytes(diagnostic, 64*1024)
	s.appendRunLog(withPipelineNodeLog(ctx, prepared.node.ID, prepared.node.Name), prepared.run.ID, "deploy", "error", "Compose 镜像准备错误详情："+diagnostic)
	return diagnostic
}

func composeRegistryLogRedactions(auth dockerengine.RegistryAuth) []string {
	if auth.Credential == "" {
		return nil
	}
	return []string{auth.Credential, base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Credential))}
}

func (s *Service) logComposeImageRegistryMismatch(ctx context.Context, prepared *executionContext, service, image, host, prefix string) {
	message := fmt.Sprintf("服务 %s 的镜像 %s 将从 %s 拉取，不属于已选择的镜像仓库路径 %s；按 Compose 的 image 执行，不向其他仓库发送所选仓库凭据。", service, image, host, prefix)
	message = string(redactLogBytes([]byte(message), normalizeLogRedactions(composeExecutionLogRedactions(prepared))))
	s.appendRunLog(withPipelineNodeLog(ctx, prepared.node.ID, prepared.node.Name), prepared.run.ID, "deploy", "warning", message)
}

// 错误尾部可能从多行配置中间开始；逐行保护已知秘密，复用现有跨分块脱敏和日志配额。
func composeExecutionLogRedactions(prepared *executionContext) []string {
	values := append([]string{}, prepared.variableRedactions...)
	values = append(values, sensitiveVariableValues(prepared.variables)...)
	values = append(values, sensitiveVariableValues(prepared.node.Config.EnvironmentVariables)...)
	if document, err := parseComposeStack(prepared.deploymentPlan.ComposeYAML); err == nil {
		for _, raw := range document["services"].(map[string]any) {
			service := raw.(map[string]any)
			environment := map[string]string{}
			switch entries := service["environment"].(type) {
			case map[string]any:
				for key, value := range entries {
					if value != nil {
						environment[key] = fmt.Sprint(value)
					}
				}
			case []any:
				for _, entry := range entries {
					if value, ok := entry.(string); ok {
						key, value, assigned := strings.Cut(value, "=")
						if assigned {
							environment[key] = value
						}
					}
				}
			}
			values = append(values, sensitiveVariableValues(environment)...)
		}
	}
	redactions := append([]string{}, values...)
	for _, value := range values {
		if strings.ContainsRune(value, '\n') {
			for _, line := range strings.Split(value, "\n") {
				if line = strings.TrimSuffix(line, "\r"); line != "" {
					redactions = append(redactions, line)
				}
			}
		}
	}
	return redactions
}
