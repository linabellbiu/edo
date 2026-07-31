package dockerengine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"edo/internal/model"
)

// ImageSnapshot 保存被替换容器实际使用的镜像引用和本地 Image ID。
// 引用用于展示，Image ID 用于后续回滚时阻止同名标签漂移到其他内容。
type ImageSnapshot struct {
	Reference string
	ID        string
}

func (s *Service) DeployContainer(
	ctx context.Context,
	endpointID, targetID, containerName, image, imageDisplay, deploymentID string,
	timeout time.Duration, configuration model.DockerContainerConfig, registry RegistryAuth, stdout, stderr io.Writer,
) (ImageSnapshot, error, error) {
	return s.deployContainer(ctx, endpointID, targetID, containerName, image, imageDisplay, "", deploymentID, timeout, configuration, registry, stdout, stderr)
}

// DeployPreparedContainer 发布已经在目标 Docker daemon 中固定 Image ID 的镜像。
// expectedImageID 防止唯一标签在构建和发布之间，或回滚创建和执行之间被替换。
func (s *Service) DeployPreparedContainer(
	ctx context.Context,
	endpointID, targetID, containerName, image, imageDisplay, expectedImageID, deploymentID string,
	timeout time.Duration, configuration model.DockerContainerConfig, registry RegistryAuth, stdout, stderr io.Writer,
) (ImageSnapshot, error, error) {
	if strings.TrimSpace(image) == "" || !IsValidImageID(expectedImageID) {
		return ImageSnapshot{}, nil, errors.New("待发布的 Docker 镜像不可验证")
	}
	return s.deployContainer(ctx, endpointID, targetID, containerName, image, imageDisplay, expectedImageID, deploymentID, timeout, configuration, registry, stdout, stderr)
}

func (s *Service) deployContainer(
	ctx context.Context,
	endpointID, targetID, containerName, image, imageDisplay, expectedImageID, deploymentID string,
	timeout time.Duration, configuration model.DockerContainerConfig, registry RegistryAuth, stdout, stderr io.Writer,
) (ImageSnapshot, error, error) {
	configuration, err := NormalizeContainerConfig(configuration)
	if err != nil {
		return ImageSnapshot{}, nil, err
	}
	apiClient, err := s.Client(ctx, endpointID)
	if err != nil {
		return ImageSnapshot{}, nil, err
	}
	defer apiClient.Close()
	deployContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	executionImage := image
	if expectedImageID != "" {
		// expectedImageID 已由构建或 SSH 导入阶段固定。校验和创建都直接按 ID
		// 执行，不能再次读取可能被其他构建覆盖的展示标签。
		localImage, err := apiClient.ImageInspect(deployContext, expectedImageID)
		if err != nil {
			return ImageSnapshot{}, nil, fmt.Errorf("目标主机上找不到待发布的 Docker 镜像: %w", err)
		}
		if localImage.ID != expectedImageID {
			return ImageSnapshot{}, nil, errors.New("目标主机上的 Docker 镜像与固定结果不一致")
		}
		executionImage = expectedImageID
	} else if IsEDOLocalImage(image) {
		if _, err := apiClient.ImageInspect(deployContext, image); err != nil {
			return ImageSnapshot{}, nil, fmt.Errorf("目标主机上找不到待发布的 Docker 镜像: %w", err)
		}
	} else if _, inspectErr := apiClient.ImageInspect(deployContext, image); inspectErr != nil {
		if !errdefs.IsNotFound(inspectErr) {
			return ImageSnapshot{}, nil, fmt.Errorf("检查目标主机 Docker 镜像失败: %w", inspectErr)
		}
		pulledWithSSH, err := s.pullImageWithSSH(deployContext, endpointID, image, registry)
		if err != nil {
			return ImageSnapshot{}, nil, err
		}
		if !pulledWithSSH {
			encodedAuth, encodeErr := encodeRegistryAuth(registryAuthConfig(registry))
			if encodeErr != nil {
				return ImageSnapshot{}, nil, encodeErr
			}
			pull, err := apiClient.ImagePull(deployContext, image, client.ImagePullOptions{RegistryAuth: encodedAuth})
			if err != nil {
				return ImageSnapshot{}, nil, fmt.Errorf("拉取 Docker 镜像失败: %w", err)
			}
			if err := pull.Wait(deployContext); err != nil {
				return ImageSnapshot{}, nil, fmt.Errorf("等待 Docker 镜像拉取完成失败: %w", err)
			}
		}
	}
	if configuration.DeploymentScript != "" {
		return s.deployContainerWithHostCommand(
			deployContext, apiClient, endpointID, targetID, containerName, executionImage, imageDisplay,
			deploymentID, configuration.DeploymentScript, stdout, stderr,
		)
	}
	configuration, err = prepareManagedContainerVolumes(deployContext, apiClient, targetID, configuration)
	if err != nil {
		return ImageSnapshot{}, nil, err
	}

	inspect, err := apiClient.ContainerInspect(deployContext, containerName, client.ContainerInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return createInitialContainer(deployContext, apiClient, targetID, containerName, executionImage, imageDisplay, deploymentID, configuration)
		}
		return ImageSnapshot{}, nil, fmt.Errorf("读取待更新 Docker 容器失败: %w", err)
	}
	if inspect.Container.Config == nil || inspect.Container.HostConfig == nil {
		return ImageSnapshot{}, nil, errors.New("Docker 容器配置不完整")
	}
	if inspect.Container.Config.Labels["edo.managed"] != "true" ||
		inspect.Container.Config.Labels["edo.deployment.target.id"] != targetID {
		return ImageSnapshot{}, nil, errors.New("同名 Docker 容器不属于当前 EDO 部署目标")
	}
	previousImage := ImageSnapshot{Reference: inspect.Container.Config.Image, ID: inspect.Container.Image}
	oldID := inspect.Container.ID
	canonicalName := strings.TrimPrefix(inspect.Container.Name, "/")
	if canonicalName == "" {
		canonicalName = strings.TrimPrefix(containerName, "/")
	}
	shortDeploymentID := strings.ReplaceAll(deploymentID, "-", "")
	if len(shortDeploymentID) > 8 {
		shortDeploymentID = shortDeploymentID[:8]
	}
	backupName := canonicalName + "-edo-backup-" + shortDeploymentID
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

	newConfig, newHostConfig, err := initialContainerConfig(executionImage, targetID, deploymentID, configuration)
	if err != nil {
		rollbackOld()
		return previousImage, nil, err
	}
	applyImageDisplayLabel(newConfig, imageDisplay)
	created, err := apiClient.ContainerCreate(deployContext, client.ContainerCreateOptions{
		Config: newConfig, HostConfig: newHostConfig, Name: canonicalName,
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

func (s *Service) deployContainerWithHostCommand(
	ctx context.Context,
	apiClient *client.Client,
	endpointID, targetID, containerName, image, imageDisplay, deploymentID, commandTemplate string,
	stdout, stderr io.Writer,
) (ImageSnapshot, error, error) {
	imageInspect, err := apiClient.ImageInspect(ctx, image)
	if err != nil || imageInspect.ID == "" {
		return ImageSnapshot{}, nil, errors.New("目标主机上找不到待发布的 Docker 镜像")
	}
	arguments, err := dockerRunCommandArguments(
		commandTemplate, image, imageDisplay, strings.TrimPrefix(containerName, "/"), targetID, deploymentID,
	)
	if err != nil {
		return ImageSnapshot{}, nil, err
	}

	var previous ImageSnapshot
	oldID := ""
	canonicalName := strings.TrimPrefix(containerName, "/")
	inspect, inspectErr := apiClient.ContainerInspect(ctx, canonicalName, client.ContainerInspectOptions{})
	if inspectErr == nil {
		if inspect.Container.Config == nil || inspect.Container.Config.Labels["edo.managed"] != "true" ||
			inspect.Container.Config.Labels["edo.deployment.target.id"] != targetID {
			return ImageSnapshot{}, nil, errors.New("同名 Docker 容器不属于当前 EDO 部署目标")
		}
		oldID = inspect.Container.ID
		previous = ImageSnapshot{Reference: inspect.Container.Config.Image, ID: inspect.Container.Image}
	} else if !errdefs.IsNotFound(inspectErr) {
		return ImageSnapshot{}, nil, fmt.Errorf("读取待更新 Docker 容器失败: %w", inspectErr)
	}

	backupName := ""
	oldStopped := false
	rollbackOld := func() {
		if !oldStopped || oldID == "" {
			return
		}
		rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = apiClient.ContainerRename(rollbackContext, oldID, client.ContainerRenameOptions{NewName: canonicalName})
		_, _ = apiClient.ContainerStart(rollbackContext, oldID, client.ContainerStartOptions{})
	}
	if oldID != "" {
		shortID := strings.ReplaceAll(deploymentID, "-", "")
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		backupName = canonicalName + "-edo-backup-" + shortID
		stopTimeout := 30
		if _, err := apiClient.ContainerStop(ctx, oldID, client.ContainerStopOptions{Timeout: &stopTimeout}); err != nil {
			return previous, nil, fmt.Errorf("停止旧 Docker 容器失败: %w", err)
		}
		oldStopped = true
		if _, err := apiClient.ContainerRename(ctx, oldID, client.ContainerRenameOptions{NewName: backupName}); err != nil {
			_, _ = apiClient.ContainerStart(ctx, oldID, client.ContainerStartOptions{})
			return previous, nil, fmt.Errorf("为旧 Docker 容器创建回退名称失败: %w", err)
		}
	}

	cleanupNew := func() {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		created, createdErr := apiClient.ContainerInspect(cleanupContext, canonicalName, client.ContainerInspectOptions{})
		if createdErr == nil && created.Container.Config != nil && created.Container.Config.Labels["edo.managed"] == "true" &&
			created.Container.Config.Labels["edo.deployment.target.id"] == targetID {
			_, _ = apiClient.ContainerRemove(cleanupContext, created.Container.ID, client.ContainerRemoveOptions{Force: true})
		}
	}
	if err := s.runDockerHostCommand(ctx, endpointID, arguments, stdout, stderr); err != nil {
		cleanupNew()
		rollbackOld()
		return previous, nil, err
	}

	created, err := apiClient.ContainerInspect(ctx, canonicalName, client.ContainerInspectOptions{})
	if err != nil || created.Container.Config == nil || created.Container.Config.Labels["edo.managed"] != "true" ||
		created.Container.Config.Labels["edo.deployment.target.id"] != targetID || created.Container.Image != imageInspect.ID {
		cleanupNew()
		rollbackOld()
		return previous, nil, errors.New("Docker 部署命令没有创建可验证的目标容器")
	}
	if err := waitContainerHealthy(ctx, apiClient, created.Container.ID); err != nil {
		cleanupNew()
		rollbackOld()
		return previous, nil, err
	}
	if oldID != "" {
		if _, err := apiClient.ContainerRemove(ctx, oldID, client.ContainerRemoveOptions{}); err != nil {
			oldStopped = false
			return previous, fmt.Errorf("清理旧 Docker 容器失败: %w", err), nil
		}
	}
	oldStopped = false
	return previous, nil, nil
}

func prepareManagedContainerVolumes(
	ctx context.Context,
	apiClient *client.Client,
	targetID string,
	configuration model.DockerContainerConfig,
) (model.DockerContainerConfig, error) {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" || len(targetID) > 64 || strings.ContainsAny(targetID, "\x00\r\n") {
		return configuration, ErrInvalidContainerConfig
	}
	for index := range configuration.VolumeMounts {
		logicalName := configuration.VolumeMounts[index].Source
		actualName := managedContainerVolumeName(targetID, logicalName)
		labels := map[string]string{
			"edo.managed":              "true",
			"edo.deployment.target.id": targetID,
			"edo.volume.logical_name":  logicalName,
		}
		inspected, err := apiClient.VolumeInspect(ctx, actualName, client.VolumeInspectOptions{})
		if err != nil {
			if !errdefs.IsNotFound(err) {
				return configuration, fmt.Errorf("检查 Docker 命名卷失败: %w", err)
			}
			created, createErr := apiClient.VolumeCreate(ctx, client.VolumeCreateOptions{Name: actualName, Labels: labels})
			if createErr != nil {
				return configuration, fmt.Errorf("创建 Docker 命名卷失败: %w", createErr)
			}
			inspected.Volume = created.Volume
		}
		for key, expected := range labels {
			if inspected.Volume.Labels[key] != expected {
				return configuration, errors.New("Docker 命名卷不属于当前部署目标")
			}
		}
		configuration.VolumeMounts[index].Source = actualName
	}
	return configuration, nil
}

func managedContainerVolumeName(targetID, logicalName string) string {
	digest := sha256.Sum256([]byte(targetID + "\x00" + logicalName))
	return fmt.Sprintf("edo-%x", digest[:16])
}

func createInitialContainer(
	ctx context.Context,
	apiClient *client.Client,
	targetID, containerName, image, imageDisplay, deploymentID string, deploymentConfig model.DockerContainerConfig,
) (ImageSnapshot, error, error) {
	configuration, hostConfiguration, err := initialContainerConfig(image, targetID, deploymentID, deploymentConfig)
	if err != nil {
		return ImageSnapshot{}, nil, err
	}
	applyImageDisplayLabel(configuration, imageDisplay)
	created, err := apiClient.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: configuration, HostConfig: hostConfiguration, Name: strings.TrimPrefix(containerName, "/"),
	})
	if err != nil {
		return ImageSnapshot{}, nil, fmt.Errorf("创建首个 Docker 容器失败: %w", err)
	}
	removeCreated := true
	defer func() {
		if removeCreated {
			cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_, _ = apiClient.ContainerRemove(cleanupContext, created.ID, client.ContainerRemoveOptions{Force: true})
		}
	}()
	if _, err := apiClient.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return ImageSnapshot{}, nil, fmt.Errorf("启动首个 Docker 容器失败: %w", err)
	}
	if err := waitContainerHealthy(ctx, apiClient, created.ID); err != nil {
		return ImageSnapshot{}, nil, err
	}
	removeCreated = false
	return ImageSnapshot{}, nil, nil
}

func applyImageDisplayLabel(configuration *container.Config, imageDisplay string) {
	imageDisplay = strings.TrimSpace(imageDisplay)
	if configuration == nil || imageDisplay == "" || len(imageDisplay) > 255 || strings.ContainsAny(imageDisplay, "\x00\r\n") {
		return
	}
	configuration.Labels[managedImageDisplayLabel] = imageDisplay
}

func initialContainerConfig(
	image, targetID, deploymentID string,
	deploymentConfig model.DockerContainerConfig,
) (*container.Config, *container.HostConfig, error) {
	deploymentConfig, err := NormalizeContainerConfig(deploymentConfig)
	if err != nil {
		return nil, nil, err
	}
	configuration := &container.Config{
		Image: image,
		Labels: map[string]string{
			"edo.deployment.id":        deploymentID,
			"edo.deployment.target.id": targetID,
			"edo.managed":              "true",
		},
	}
	hostConfiguration := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		NetworkMode:   container.NetworkMode(deploymentConfig.Network),
	}
	if len(deploymentConfig.Command) > 0 {
		configuration.Cmd = slices.Clone(deploymentConfig.Command)
	}
	if len(deploymentConfig.EnvironmentVariables) > 0 {
		names := make([]string, 0, len(deploymentConfig.EnvironmentVariables))
		for name := range deploymentConfig.EnvironmentVariables {
			names = append(names, name)
		}
		sort.Strings(names)
		configuration.Env = make([]string, 0, len(names))
		for _, name := range names {
			configuration.Env = append(configuration.Env, name+"="+deploymentConfig.EnvironmentVariables[name])
		}
	}
	if deploymentConfig.HealthCheck.Enabled {
		configuration.Healthcheck = &container.HealthConfig{
			Test:        append([]string{"CMD"}, deploymentConfig.HealthCheck.Command...),
			Interval:    time.Duration(deploymentConfig.HealthCheck.IntervalSeconds) * time.Second,
			Timeout:     time.Duration(deploymentConfig.HealthCheck.TimeoutSeconds) * time.Second,
			Retries:     deploymentConfig.HealthCheck.Retries,
			StartPeriod: time.Duration(deploymentConfig.HealthCheck.StartPeriodSeconds) * time.Second,
		}
	}
	if len(deploymentConfig.PortMappings) > 0 {
		configuration.ExposedPorts = make(network.PortSet, len(deploymentConfig.PortMappings))
		hostConfiguration.PortBindings = make(network.PortMap, len(deploymentConfig.PortMappings))
		for _, mapping := range deploymentConfig.PortMappings {
			port, parseErr := network.ParsePort(strconv.Itoa(mapping.ContainerPort) + "/" + mapping.Protocol)
			if parseErr != nil {
				return nil, nil, ErrInvalidContainerConfig
			}
			configuration.ExposedPorts[port] = struct{}{}
			hostConfiguration.PortBindings[port] = []network.PortBinding{{
				HostIP: netip.MustParseAddr(mapping.HostIP), HostPort: strconv.Itoa(mapping.HostPort),
			}}
		}
	}
	if len(deploymentConfig.VolumeMounts) > 0 {
		hostConfiguration.Mounts = make([]mount.Mount, 0, len(deploymentConfig.VolumeMounts))
		for _, volume := range deploymentConfig.VolumeMounts {
			hostConfiguration.Mounts = append(hostConfiguration.Mounts, mount.Mount{
				Type: mount.Type(volume.Type), Source: volume.Source, Target: volume.Target, ReadOnly: volume.ReadOnly,
			})
		}
	}
	return configuration, hostConfiguration, nil
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
