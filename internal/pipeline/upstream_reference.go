package pipeline

import (
	"log/slog"
	"strings"

	"edo/internal/model"
)

func upstreamOutputName(value string) string {
	if !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") {
		return ""
	}
	name := value[2 : len(value)-1]
	if !outputNamePattern.MatchString(name) || strings.HasPrefix(name, "EDO_") {
		return ""
	}
	if _, reserved := reservedScriptEnvironmentNames[name]; reserved {
		return ""
	}
	return name
}

// 按前序阶段声明的命名产物解析，不依赖尚未执行的产物记录。
func resolveUpstreamOutput(nodeID, value string, stages []model.WorkflowStage) (string, string, error) {
	name := upstreamOutputName(value)
	var matches []string
	for _, stage := range stages {
		for _, task := range stage.Tasks {
			if task.ID == nodeID {
				if name != "" && len(matches) == 1 {
					return matches[0], name, nil
				}
				slog.Warn("上游构建产物变量无法唯一匹配", "operation", "deployment_output_reference", "node_id", nodeID, "candidates", len(matches), "err", ErrInvalidWorkflow)
				return "", "", ErrInvalidWorkflow
			}
		}
		for _, task := range stage.Tasks {
			if task.Type != model.WorkflowNodeBuild {
				continue
			}
			for _, output := range task.Config.BuildOutputs {
				if output.Name == name {
					matches = append(matches, task.ID)
				}
			}
		}
	}
	slog.Warn("上游构建产物变量缺少部署任务", "operation", "deployment_output_reference", "node_id", nodeID, "err", ErrInvalidWorkflow)
	return "", "", ErrInvalidWorkflow
}
