package pipeline

import (
	"context"

	"edo/internal/dockerengine"
)

// 镜像准备与部署命令使用相同的任务日志和密钥脱敏规则。
func (s *Service) prepareDeploymentImage(ctx context.Context, prepared *executionContext, runtimeID, hostID, image string, auth dockerengine.RegistryAuth, configured bool) error {
	prepared.variableRedactions = append(prepared.variableRedactions, composeRegistryLogRedactions(auth)...)
	redactions := composeExecutionLogRedactions(prepared)
	output := s.newExecutionLogStreams(ctx, prepared.run.ID, "prepare", "镜像传输", redactions...)
	defer output.Close()
	if configured {
		return s.docker.PrepareConfiguredRegistryImage(ctx, runtimeID, hostID, image, auth, output, output.Stderr())
	}
	return s.docker.PrepareRegistryImage(ctx, runtimeID, hostID, image, auth, output, output.Stderr())
}
