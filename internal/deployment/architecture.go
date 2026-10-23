package deployment

import (
	"context"
	"log/slog"

	"gorm.io/gorm"

	"edo/internal/kube"
	"edo/internal/model"
)

// KubernetesNodeArchitectures 复用部署连接和当前事务，供流水线冻结集群构建平台。
func (s *Service) KubernetesNodeArchitectures(ctx context.Context, tx *gorm.DB, clusterID, projectID string) ([]model.HostArchitecture, error) {
	if s == nil || s.kube == nil || tx == nil {
		slog.ErrorContext(ctx, "部署集群架构检查未配置", "operation", "deployment_node_architectures_unavailable", "cluster_id", clusterID, "project_id", projectID, "err", kube.ErrNodeArchitecturesUnavailable)
		return nil, kube.ErrNodeArchitecturesUnavailable
	}
	return s.kube.WithTransaction(tx).NodeArchitectures(ctx, clusterID, projectID)
}
