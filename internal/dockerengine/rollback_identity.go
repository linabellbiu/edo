package dockerengine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// ContainerRollbackIdentity 只包含回退确认所需的容器归属与不可变镜像身份。
type ContainerRollbackIdentity struct {
	ContainerID            string
	ImageID                string
	DeploymentID           string
	TargetID               string
	Managed                bool
	PreviousImageAvailable bool
}

func matchesRollbackContainer(inspected container.InspectResponse, current ContainerRollbackIdentity) bool {
	return inspected.ID == current.ContainerID && inspected.Image == current.ImageID && inspected.Config != nil &&
		inspected.Config.Labels["edo.managed"] == "true" && inspected.Config.Labels["edo.deployment.target.id"] == current.TargetID &&
		inspected.Config.Labels["edo.deployment.id"] == current.DeploymentID
}

// RollbackIdentity 使用当前项目授权的 Docker 连接读取身份，不启动容器或拉取镜像。
func (s *Service) RollbackIdentity(ctx context.Context, endpointID, containerName, previousImageID string) (ContainerRollbackIdentity, error) {
	identity := ContainerRollbackIdentity{}
	apiClient, err := s.Client(ctx, endpointID)
	if err != nil {
		slog.WarnContext(ctx, "读取 Docker 回退身份连接失败", "operation", "docker_rollback_identity_client", "endpoint_id", endpointID, "err", err)
		return identity, err
	}
	defer apiClient.Close()
	requestContext, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	defer cancel()
	inspected, err := apiClient.ContainerInspect(requestContext, strings.TrimSpace(containerName), client.ContainerInspectOptions{})
	if err != nil {
		slog.WarnContext(ctx, "读取 Docker 回退容器身份失败", "operation", "docker_rollback_identity_container", "endpoint_id", endpointID, "container_name", containerName, "err", err)
		return identity, fmt.Errorf("读取回退容器身份失败: %w", err)
	}
	identity.ContainerID, identity.ImageID = inspected.Container.ID, inspected.Container.Image
	if inspected.Container.Config != nil {
		labels := inspected.Container.Config.Labels
		identity.Managed = labels["edo.managed"] == "true"
		identity.DeploymentID, identity.TargetID = labels["edo.deployment.id"], labels["edo.deployment.target.id"]
	}
	image, err := apiClient.ImageInspect(requestContext, previousImageID)
	if err != nil {
		if errors.Is(err, errdefs.ErrNotFound) {
			return identity, nil
		}
		slog.WarnContext(ctx, "读取 Docker 上一镜像身份失败", "operation", "docker_rollback_identity_image", "endpoint_id", endpointID, "err", err)
		return identity, fmt.Errorf("读取回退镜像身份失败: %w", err)
	}
	identity.PreviousImageAvailable = image.ID == previousImageID
	return identity, nil
}
