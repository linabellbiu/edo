package pipeline

import (
	"context"

	"gorm.io/gorm"

	"edo/internal/kube"
	"edo/internal/model"
)

type kubernetesArchitectureReader interface {
	KubernetesNodeArchitectures(context.Context, *gorm.DB, string, string) ([]model.HostArchitecture, error)
}

func (s *Service) deploymentTargetArchitectures(ctx context.Context, database *gorm.DB, target model.DeploymentTarget) ([]model.HostArchitecture, error) {
	return resolveDeploymentTargetArchitectures(ctx, database, target, s.deployments)
}

func resolveDeploymentTargetArchitectures(ctx context.Context, database *gorm.DB, target model.DeploymentTarget, clusters kubernetesArchitectureReader) ([]model.HostArchitecture, error) {
	if target.Platform == model.DeploymentKubernetes {
		if clusters == nil {
			return nil, kube.ErrNodeArchitecturesUnavailable
		}
		return clusters.KubernetesNodeArchitectures(ctx, database, target.RuntimeID, target.ProjectID)
	}
	architecture, err := deploymentTargetArchitecture(ctx, database, target.HostID)
	if err != nil {
		return nil, err
	}
	return []model.HostArchitecture{architecture}, nil
}

func singleTargetArchitecture(architectures []model.HostArchitecture) model.HostArchitecture {
	if len(architectures) == 1 {
		return architectures[0]
	}
	return ""
}
