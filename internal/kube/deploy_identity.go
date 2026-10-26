package kube

import (
	"context"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var ErrDeploymentTargetChanged = errors.New("Kubernetes 工作负载已变化，请重新确认目标")

// DeploymentImageIdentity 同时固定对象身份、目标容器镜像和发布归属。
type DeploymentImageIdentity struct {
	UID          string
	Image        string
	DeploymentID string
}

func (s *Service) DeploymentImageIdentity(ctx context.Context, clusterID, namespace, name, container string) (DeploymentImageIdentity, error) {
	namespace, err := normalizeNamespace(namespace)
	if err != nil {
		return DeploymentImageIdentity{}, ErrDeploymentConfigInvalid
	}
	ctx, query, err := s.authorizeResourceOperation(ctx, clusterID, "deployments", namespace, "read", false)
	if err != nil {
		return DeploymentImageIdentity{}, err
	}
	current, err := query.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return DeploymentImageIdentity{}, fmt.Errorf("%w: %w", ErrDeploymentStatusUnavailable, err)
	}
	if current.UID == "" {
		return DeploymentImageIdentity{}, ErrDeploymentStatusUnavailable
	}
	for _, item := range current.Spec.Template.Spec.Containers {
		if item.Name == container {
			return DeploymentImageIdentity{UID: string(current.UID), Image: item.Image, DeploymentID: current.Spec.Template.Annotations["edo.io/deployment-id"]}, nil
		}
	}
	return DeploymentImageIdentity{}, ErrDeploymentContainerNotFound
}

// DeployImageIfCurrent 在每次冲突重试中复验身份，避免确认后覆盖其他发布。
func (s *Service) DeployImageIfCurrent(ctx context.Context, clusterID, namespace, name, container, image, deploymentID string, timeout time.Duration, expected DeploymentImageIdentity) (string, error) {
	return s.deployImage(ctx, clusterID, namespace, name, container, image, deploymentID, timeout, &expected)
}
