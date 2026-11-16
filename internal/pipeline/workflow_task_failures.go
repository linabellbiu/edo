package pipeline

import (
	"context"
	"errors"
	"log/slog"

	"edo/internal/cicd"
	"edo/internal/deployment"
	"edo/internal/kube"
	"edo/internal/model"

	"gorm.io/gorm"
)

var errWorkflowSnapshotUnavailable = errors.New("流水线运行配置暂时无法读取，请稍后重试")

// 只展示登记过的稳定业务错误；底层包装错误可能包含连接地址或其他内部诊断。
func workflowResourceFailureMessage(resource string, err error) string {
	for _, safe := range []error{
		kube.ErrNodeArchitectureConnection,
		kube.ErrNodeArchitectureTimeout,
		kube.ErrNodeArchitectureCredentials,
		kube.ErrNodeArchitecturePermission,
		kube.ErrNodeArchitectureTLS,
		kube.ErrNodeArchitectureCanceled,
		kube.ErrNodeArchitecturesMissing,
		kube.ErrNodeArchitectureUnsupported,
		kube.ErrNodeArchitecturesUnavailable,
		kube.ErrClusterNotFound,
		kube.ErrInvalidCluster,
		kube.ErrRBACForbidden,
		kube.ErrRBACSyncPending,
		kube.ErrResourceForbidden,
		deployment.ErrComposeDirectoryTimeout,
		deployment.ErrComposeDirectoryHostUnavailable,
		deployment.ErrComposeDirectoryCanceled,
		deployment.ErrComposeHomeInvalid,
		deployment.ErrComposeDirectoryUnavailable,
		deployment.ErrEnvironmentTargetUnavailable,
		deployment.ErrEnvironmentTargetAmbiguous,
		deployment.ErrTargetNotFound,
		deployment.ErrInvalidTarget,
		cicd.ErrExecutionAuthorization,
		ErrDeploymentRegistryRequired,
		ErrDeploymentRegistryUnavailable,
		ErrBuildPlanNotFound,
		ErrDeploymentPlanNotFound,
		ErrKubernetesTemplatePermission,
		ErrDeploymentPlanTargetMismatch,
		ErrInvalidBuildPlatform,
		ErrInvalidBuildTarget,
		ErrInvalidRegistryEndpoint,
		ErrInsecureRegistryEndpoint,
		ErrDeploymentInputs,
		errDeploymentEnvironmentCheckFailed,
		errDeploymentClusterCheckFailed,
		model.ErrDeploymentOverride,
		model.ErrKubernetesDeploymentSettings,
	} {
		if errors.Is(err, safe) {
			return resource + "：" + safe.Error()
		}
	}
	return resource + "不可用，请检查此任务的配置与使用权限"
}

func (s *Service) readWorkflowSnapshotResource(ctx context.Context, query *gorm.DB, value any) error {
	err := query.First(value).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.logger.ErrorContext(ctx, "读取流水线快照资源失败", "operation", "workflow_snapshot_resource", "err", err)
		return errWorkflowSnapshotUnavailable
	}
	return err
}

// 安全提示与源头明确提供的脱敏诊断分别冻结，不阻断独立任务。
func (s *Service) recordWorkflowTaskFailure(ctx context.Context, failures map[string]string, nodeID, operation, message string, err error) {
	if s.logger != nil {
		s.logger.Log(ctx, slog.LevelError, "冻结流水线任务失败", "operation", operation, "node_id", nodeID, "err", err)
	}
	if failures[nodeID] == "" {
		failures[nodeID] = message
		if diagnostics, ok := ctx.Value(workflowTaskDiagnosticsKey{}).(*workflowTaskDiagnostics); ok {
			diagnostics.record(nodeID, operation, err)
		}
	}
}
