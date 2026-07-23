package dockerengine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

func (s *Service) DeployContainer(
	ctx context.Context,
	endpointID, containerName, image, deploymentID string,
	timeout time.Duration,
) (string, error, error) {
	apiClient, err := s.Client(ctx, endpointID)
	if err != nil {
		return "", nil, err
	}
	defer apiClient.Close()
	deployContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pull, err := apiClient.ImagePull(deployContext, image, client.ImagePullOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("拉取 Docker 镜像失败: %w", err)
	}
	if err := pull.Wait(deployContext); err != nil {
		return "", nil, fmt.Errorf("等待 Docker 镜像拉取完成失败: %w", err)
	}

	inspect, err := apiClient.ContainerInspect(deployContext, containerName, client.ContainerInspectOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("读取待更新 Docker 容器失败: %w", err)
	}
	if inspect.Container.Config == nil || inspect.Container.HostConfig == nil {
		return "", nil, errors.New("Docker 容器配置不完整")
	}
	previousImage := inspect.Container.Config.Image
	oldID := inspect.Container.ID
	canonicalName := strings.TrimPrefix(inspect.Container.Name, "/")
	if canonicalName == "" {
		canonicalName = strings.TrimPrefix(containerName, "/")
	}
	shortDeploymentID := strings.ReplaceAll(deploymentID, "-", "")
	if len(shortDeploymentID) > 8 {
		shortDeploymentID = shortDeploymentID[:8]
	}
	backupName := canonicalName + "-zrt-backup-" + shortDeploymentID
	stopTimeout := 30
	if _, err := apiClient.ContainerStop(deployContext, oldID, client.ContainerStopOptions{Timeout: &stopTimeout}); err != nil {
		return previousImage, nil, fmt.Errorf("停止旧 Docker 容器失败: %w", err)
	}
	oldStopped := true
	rollbackOld := func() {
		if oldStopped {
			rollbackContext, rollbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer rollbackCancel()
			_, _ = apiClient.ContainerRename(rollbackContext, oldID, client.ContainerRenameOptions{NewName: canonicalName})
			_, _ = apiClient.ContainerStart(rollbackContext, oldID, client.ContainerStartOptions{})
		}
	}
	if _, err := apiClient.ContainerRename(deployContext, oldID, client.ContainerRenameOptions{NewName: backupName}); err != nil {
		rollbackContext, rollbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer rollbackCancel()
		_, _ = apiClient.ContainerStart(rollbackContext, oldID, client.ContainerStartOptions{})
		return previousImage, nil, fmt.Errorf("为旧 Docker 容器创建回退名称失败: %w", err)
	}

	newConfig := *inspect.Container.Config
	newConfig.Image = image
	newConfig.Labels = maps.Clone(newConfig.Labels)
	if newConfig.Labels == nil {
		newConfig.Labels = map[string]string{}
	}
	newConfig.Labels["zrt.deployment.id"] = deploymentID
	newConfig.Labels["zrt.managed"] = "true"
	networkingConfig := sanitizedNetworkingConfig(inspect.Container.NetworkSettings)
	created, err := apiClient.ContainerCreate(deployContext, client.ContainerCreateOptions{
		Config: &newConfig, HostConfig: inspect.Container.HostConfig,
		NetworkingConfig: networkingConfig, Name: canonicalName,
	})
	if err != nil {
		rollbackOld()
		return previousImage, nil, fmt.Errorf("创建新 Docker 容器失败: %w", err)
	}
	newID := created.ID
	newCreated := true
	rollbackNew := func() {
		rollbackContext, rollbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer rollbackCancel()
		if newCreated {
			_, _ = apiClient.ContainerRemove(rollbackContext, newID, client.ContainerRemoveOptions{Force: true})
		}
		rollbackOld()
	}
	if _, err := apiClient.ContainerStart(deployContext, newID, client.ContainerStartOptions{}); err != nil {
		rollbackNew()
		return previousImage, nil, fmt.Errorf("启动新 Docker 容器失败: %w", err)
	}
	if err := waitContainerHealthy(deployContext, apiClient, newID); err != nil {
		rollbackNew()
		return previousImage, nil, err
	}
	if _, err := apiClient.ContainerRemove(deployContext, oldID, client.ContainerRemoveOptions{}); err != nil {
		newCreated = false
		oldStopped = false
		return previousImage, fmt.Errorf("清理旧 Docker 容器失败: %w", err), nil
	}
	newCreated = false
	oldStopped = false
	return previousImage, nil, nil
}

func sanitizedNetworkingConfig(settings *container.NetworkSettings) *network.NetworkingConfig {
	if settings == nil || len(settings.Networks) == 0 {
		return nil
	}
	endpoints := make(map[string]*network.EndpointSettings, len(settings.Networks))
	for name, current := range settings.Networks {
		if current == nil {
			endpoints[name] = nil
			continue
		}
		endpoints[name] = &network.EndpointSettings{
			Links: slices.Clone(current.Links), Aliases: slices.Clone(current.Aliases),
			DriverOpts: maps.Clone(current.DriverOpts), GwPriority: current.GwPriority,
		}
	}
	return &network.NetworkingConfig{EndpointsConfig: endpoints}
}

func waitContainerHealthy(ctx context.Context, apiClient *client.Client, containerID string) error {
	started := time.Now()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		inspect, err := apiClient.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		if err != nil {
			return fmt.Errorf("检查新 Docker 容器状态失败: %w", err)
		}
		if inspect.Container.State == nil || !inspect.Container.State.Running {
			return errors.New("新 Docker 容器未保持运行状态")
		}
		if inspect.Container.State.Health != nil {
			switch inspect.Container.State.Health.Status {
			case "healthy":
				return nil
			case "unhealthy":
				return errors.New("新 Docker 容器健康检查失败")
			}
		} else if time.Since(started) >= 5*time.Second {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("等待新 Docker 容器就绪超时")
		case <-ticker.C:
		}
	}
}
