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

var (
	ErrContainerNotRunning         = errors.New("Docker 容器未保持运行")
	ErrContainerRestarted          = errors.New("Docker 容器启动期间发生重启")
	ErrContainerUnhealthy          = errors.New("Docker 容器健康检查失败")
	ErrContainerReadinessTimeout   = errors.New("等待 Docker 容器就绪超时")
	ErrContainerStopTimeout        = errors.New("停止旧 Docker 容器超时")
	ErrContainerStopFailed         = errors.New("停止旧 Docker 容器失败")
	ErrContainerRollbackFailed     = errors.New("恢复旧 Docker 容器失败")
	ErrContainerPortAllocated      = errors.New("Docker 容器主机端口已被占用")
	ErrContainerRuntimeUnavailable = errors.New("Docker 容器运行时不可用")
	ErrContainerImageUnavailable   = errors.New("Docker 容器镜像不可用")
	ErrContainerImageMismatch      = errors.New("Docker 容器镜像身份不一致")
	ErrContainerConfigInvalid      = errors.New("Docker 容器发布配置无效")
	ErrContainerOwnershipConflict  = errors.New("Docker 容器名称被其他资源占用")
	ErrContainerVolumeFailed       = errors.New("Docker 容器卷准备失败")
	ErrContainerCreateFailed       = errors.New("创建 Docker 容器失败")
	ErrContainerStartFailed        = errors.New("启动 Docker 容器失败")
	ErrContainerCommandFailed      = errors.New("Docker 自定义部署命令失败")
	ErrContainerInspectFailed      = errors.New("读取 Docker 容器状态失败")
	ErrContainerReplaceFailed      = errors.New("替换 Docker 容器失败")
	ErrContainerVerificationFailed = errors.New("Docker 容器发布结果校验失败")
)

const (
	directContainerStabilityWindow = 10 * time.Second
	directContainerPollInterval    = time.Second
	directContainerStopGrace       = 10
	directContainerStopRequestWait = 5 * time.Second
	directContainerReconcileWait   = 5 * time.Second
	directContainerRollbackTimeout = 45 * time.Second
)

type containerInspectFunc func(context.Context) (client.ContainerInspectResult, error)

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
		return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerImageMismatch, errors.New("待发布的 Docker 镜像不可验证"))
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
		return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerConfigInvalid, err)
	}
	apiClient, err := s.executionClient(ctx, endpointID)
	if err != nil {
		return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerRuntimeUnavailable, err)
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
			return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerImageUnavailable, fmt.Errorf("目标主机上找不到待发布的 Docker 镜像: %w", err))
		}
		if localImage.ID != expectedImageID {
			return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerImageMismatch, errors.New("目标主机上的 Docker 镜像与固定结果不一致"))
		}
		executionImage = expectedImageID
	} else if IsEDOLocalImage(image) {
		if _, err := apiClient.ImageInspect(deployContext, image); err != nil {
			return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerImageUnavailable, fmt.Errorf("目标主机上找不到待发布的 Docker 镜像: %w", err))
		}
	} else if _, inspectErr := apiClient.ImageInspect(deployContext, image); inspectErr != nil {
		if !errdefs.IsNotFound(inspectErr) {
			return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerImageUnavailable, fmt.Errorf("检查目标主机 Docker 镜像失败: %w", inspectErr))
		}
		pulledWithSSH, err := s.pullImageWithSSH(deployContext, endpointID, image, registry)
		if err != nil {
			return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerImageUnavailable, err)
		}
		if !pulledWithSSH {
			encodedAuth, encodeErr := encodeRegistryAuth(registryAuthConfig(registry))
			if encodeErr != nil {
				return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerImageUnavailable, encodeErr)
			}
			pull, err := apiClient.ImagePull(deployContext, image, client.ImagePullOptions{RegistryAuth: encodedAuth})
			if err != nil {
				return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerImageUnavailable, fmt.Errorf("拉取 Docker 镜像失败: %w", err))
			}
			if err := pull.Wait(deployContext); err != nil {
				return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerImageUnavailable, fmt.Errorf("等待 Docker 镜像拉取完成失败: %w", err))
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
		return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerVolumeFailed, err)
	}

	inspect, err := apiClient.ContainerInspect(deployContext, containerName, client.ContainerInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return createInitialContainer(deployContext, apiClient, targetID, containerName, executionImage, imageDisplay, deploymentID, configuration)
		}
		return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerInspectFailed, fmt.Errorf("读取待更新 Docker 容器失败: %w", err))
	}
	if inspect.Container.Config == nil || inspect.Container.HostConfig == nil {
		return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerConfigInvalid, errors.New("Docker 容器配置不完整"))
	}
	if inspect.Container.Config.Labels["edo.managed"] != "true" ||
		inspect.Container.Config.Labels["edo.deployment.target.id"] != targetID {
		return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerOwnershipConflict, errors.New("同名 Docker 容器不属于当前 EDO 部署目标"))
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
	if err := stopContainerForReplacement(deployContext, apiClient, oldID); err != nil {
		return previousImage, nil, err
	}
	oldStopped := true
	rollbackOld := func() error {
		if oldStopped {
			if err := restoreStoppedContainer(ctx, apiClient, oldID, canonicalName); err != nil {
				return err
			}
			oldStopped = false
		}
		return nil
	}
	if _, err := apiClient.ContainerRename(deployContext, oldID, client.ContainerRenameOptions{NewName: backupName}); err != nil {
		return previousImage, nil, deploymentErrorWithRollback(
			containerDeploymentError(ErrContainerReplaceFailed, fmt.Errorf("为旧 Docker 容器创建回退名称失败: %w", err)), rollbackOld(),
		)
	}

	newConfig, newHostConfig, err := initialContainerConfig(executionImage, targetID, deploymentID, configuration)
	if err != nil {
		return previousImage, nil, deploymentErrorWithRollback(containerDeploymentError(ErrContainerConfigInvalid, err), rollbackOld())
	}
	applyImageDisplayLabel(newConfig, imageDisplay)
	created, err := apiClient.ContainerCreate(deployContext, client.ContainerCreateOptions{
		Config: newConfig, HostConfig: newHostConfig, Name: canonicalName,
	})
	if err != nil {
		return previousImage, nil, deploymentErrorWithRollback(
			containerDeploymentError(ErrContainerCreateFailed, fmt.Errorf("创建新 Docker 容器失败: %w", err)), rollbackOld(),
		)
	}
	newID := created.ID
	newCreated := true
	rollbackNew := func() error {
		rollbackContext, rollbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer rollbackCancel()
		var removeErr error
		if newCreated {
			if _, err := apiClient.ContainerRemove(rollbackContext, newID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
				removeErr = fmt.Errorf("清理未就绪的新 Docker 容器失败: %w", err)
			}
		}
		return errors.Join(removeErr, rollbackOld())
	}
	if _, err := apiClient.ContainerStart(deployContext, newID, client.ContainerStartOptions{}); err != nil {
		return previousImage, nil, deploymentErrorWithRollback(
			containerDeploymentError(ErrContainerStartFailed, fmt.Errorf("启动新 Docker 容器失败: %w", classifyContainerStartError(err))), rollbackNew(),
		)
	}
	if err := waitContainerHealthy(deployContext, apiClient, newID); err != nil {
		return previousImage, nil, deploymentErrorWithRollback(err, rollbackNew())
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

func stopContainerForReplacement(ctx context.Context, apiClient *client.Client, containerID string) error {
	stopTimeout := directContainerStopGrace
	stopContext, cancel := context.WithTimeout(
		ctx,
		time.Duration(directContainerStopGrace)*time.Second+directContainerStopRequestWait,
	)
	_, stopErr := apiClient.ContainerStop(stopContext, containerID, client.ContainerStopOptions{Timeout: &stopTimeout})
	cancel()
	if stopErr == nil {
		return nil
	}

	// Docker 可能已经完成强制停止，但响应在超时边界上丢失。先重新读取真实状态，
	// 避免把已经停止的容器误判为发布失败并中断替换流程。
	reconcileContext, reconcileCancel := context.WithTimeout(context.WithoutCancel(ctx), directContainerReconcileWait)
	defer reconcileCancel()
	inspect, inspectErr := apiClient.ContainerInspect(reconcileContext, containerID, client.ContainerInspectOptions{})
	if inspectErr == nil && inspect.Container.State != nil && !inspect.Container.State.Running && !inspect.Container.State.Restarting {
		return nil
	}
	if errors.Is(stopErr, context.DeadlineExceeded) || errors.Is(stopErr, context.Canceled) {
		return fmt.Errorf("%w: stop_error=%v inspect_error=%v", ErrContainerStopTimeout, stopErr, inspectErr)
	}
	return fmt.Errorf("%w: stop_error=%v inspect_error=%v", ErrContainerStopFailed, stopErr, inspectErr)
}

func restoreStoppedContainer(
	ctx context.Context,
	apiClient *client.Client,
	containerID string,
	canonicalName string,
) error {
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), directContainerRollbackTimeout)
	defer cancel()
	inspect, err := apiClient.ContainerInspect(rollbackContext, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("读取待恢复 Docker 容器失败: %w", err)
	}
	restartBaseline := inspect.Container.RestartCount
	if strings.TrimPrefix(inspect.Container.Name, "/") != canonicalName {
		if _, err := apiClient.ContainerRename(rollbackContext, containerID, client.ContainerRenameOptions{NewName: canonicalName}); err != nil {
			return fmt.Errorf("恢复旧 Docker 容器名称失败: %w", err)
		}
	}
	if inspect.Container.State == nil || !inspect.Container.State.Running {
		if _, err := apiClient.ContainerStart(rollbackContext, containerID, client.ContainerStartOptions{}); err != nil {
			return fmt.Errorf("重新启动旧 Docker 容器失败: %w", err)
		}
	}
	if err := waitContainerHealthyWithRestartBaseline(rollbackContext, apiClient, containerID, restartBaseline); err != nil {
		return fmt.Errorf("确认旧 Docker 容器恢复失败: %w", err)
	}
	return nil
}

func deploymentErrorWithRollback(deployErr, rollbackErr error) error {
	if rollbackErr == nil {
		return deployErr
	}
	return fmt.Errorf("%w: deployment_error=%v rollback_error=%v", ErrContainerRollbackFailed, deployErr, rollbackErr)
}

// Docker Engine 的端口分配失败通过 500 响应中的稳定 daemon 文案返回，
// Moby 客户端没有提供更细的结构化错误类型。这里在 Docker 启动边界转换为
// EDO 的业务分类，原始错误仍保留在错误链中供系统日志诊断。
func classifyContainerStartError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "port is already allocated") ||
		strings.Contains(message, "failed to bind host port") {
		return fmt.Errorf("%w: %v", ErrContainerPortAllocated, err)
	}
	return err
}

func containerDeploymentError(category error, cause error) error {
	if cause == nil || errors.Is(cause, category) {
		return cause
	}
	return fmt.Errorf("%w: %w", category, cause)
}

func (s *Service) deployContainerWithHostCommand(
	ctx context.Context,
	apiClient *client.Client,
	endpointID, targetID, containerName, image, imageDisplay, deploymentID, commandTemplate string,
	stdout, stderr io.Writer,
) (ImageSnapshot, error, error) {
	imageInspect, err := apiClient.ImageInspect(ctx, image)
	if err != nil || imageInspect.ID == "" {
		return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerImageUnavailable, errors.New("目标主机上找不到待发布的 Docker 镜像"))
	}
	arguments, err := dockerRunCommandArguments(
		commandTemplate, image, imageDisplay, strings.TrimPrefix(containerName, "/"), targetID, deploymentID,
	)
	if err != nil {
		return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerConfigInvalid, err)
	}

	var previous ImageSnapshot
	oldID := ""
	canonicalName := strings.TrimPrefix(containerName, "/")
	inspect, inspectErr := apiClient.ContainerInspect(ctx, canonicalName, client.ContainerInspectOptions{})
	if inspectErr == nil {
		if inspect.Container.Config == nil || inspect.Container.Config.Labels["edo.managed"] != "true" ||
			inspect.Container.Config.Labels["edo.deployment.target.id"] != targetID {
			return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerOwnershipConflict, errors.New("同名 Docker 容器不属于当前 EDO 部署目标"))
		}
		oldID = inspect.Container.ID
		previous = ImageSnapshot{Reference: inspect.Container.Config.Image, ID: inspect.Container.Image}
	} else if !errdefs.IsNotFound(inspectErr) {
		return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerInspectFailed, fmt.Errorf("读取待更新 Docker 容器失败: %w", inspectErr))
	}

	backupName := ""
	oldStopped := false
	rollbackOld := func() error {
		if !oldStopped || oldID == "" {
			return nil
		}
		if err := restoreStoppedContainer(ctx, apiClient, oldID, canonicalName); err != nil {
			return err
		}
		oldStopped = false
		return nil
	}
	if oldID != "" {
		shortID := strings.ReplaceAll(deploymentID, "-", "")
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		backupName = canonicalName + "-edo-backup-" + shortID
		if err := stopContainerForReplacement(ctx, apiClient, oldID); err != nil {
			return previous, nil, err
		}
		oldStopped = true
		if _, err := apiClient.ContainerRename(ctx, oldID, client.ContainerRenameOptions{NewName: backupName}); err != nil {
			return previous, nil, deploymentErrorWithRollback(
				containerDeploymentError(ErrContainerReplaceFailed, fmt.Errorf("为旧 Docker 容器创建回退名称失败: %w", err)), rollbackOld(),
			)
		}
	}

	cleanupNew := func() error {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		created, createdErr := apiClient.ContainerInspect(cleanupContext, canonicalName, client.ContainerInspectOptions{})
		if createdErr != nil {
			if errdefs.IsNotFound(createdErr) {
				return nil
			}
			return fmt.Errorf("检查未就绪的新 Docker 容器失败: %w", createdErr)
		}
		if createdErr == nil && created.Container.Config != nil && created.Container.Config.Labels["edo.managed"] == "true" &&
			created.Container.Config.Labels["edo.deployment.target.id"] == targetID {
			if _, err := apiClient.ContainerRemove(cleanupContext, created.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
				return fmt.Errorf("清理未就绪的新 Docker 容器失败: %w", err)
			}
		}
		return nil
	}
	if err := s.runDockerHostCommand(ctx, endpointID, arguments, stdout, stderr); err != nil {
		rollbackErr := errors.Join(cleanupNew(), rollbackOld())
		return previous, nil, deploymentErrorWithRollback(containerDeploymentError(ErrContainerCommandFailed, err), rollbackErr)
	}

	created, err := apiClient.ContainerInspect(ctx, canonicalName, client.ContainerInspectOptions{})
	if err != nil || created.Container.Config == nil || created.Container.Config.Labels["edo.managed"] != "true" ||
		created.Container.Config.Labels["edo.deployment.target.id"] != targetID || created.Container.Image != imageInspect.ID {
		deployErr := containerDeploymentError(ErrContainerVerificationFailed, errors.New("Docker 部署命令没有创建可验证的目标容器"))
		return previous, nil, deploymentErrorWithRollback(deployErr, errors.Join(cleanupNew(), rollbackOld()))
	}
	if err := waitContainerHealthy(ctx, apiClient, created.Container.ID); err != nil {
		return previous, nil, deploymentErrorWithRollback(err, errors.Join(cleanupNew(), rollbackOld()))
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
		return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerConfigInvalid, err)
	}
	applyImageDisplayLabel(configuration, imageDisplay)
	created, err := apiClient.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: configuration, HostConfig: hostConfiguration, Name: strings.TrimPrefix(containerName, "/"),
	})
	if err != nil {
		return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerCreateFailed, fmt.Errorf("创建首个 Docker 容器失败: %w", err))
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
		return ImageSnapshot{}, nil, containerDeploymentError(ErrContainerStartFailed, fmt.Errorf("启动首个 Docker 容器失败: %w", classifyContainerStartError(err)))
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
	return waitContainerHealthyWithRestartBaseline(ctx, apiClient, containerID, 0)
}

func waitContainerHealthyWithRestartBaseline(
	ctx context.Context,
	apiClient *client.Client,
	containerID string,
	restartBaseline int,
) error {
	return waitContainerReadyFromRestartCount(ctx, func(inspectContext context.Context) (client.ContainerInspectResult, error) {
		return apiClient.ContainerInspect(inspectContext, containerID, client.ContainerInspectOptions{})
	}, directContainerStabilityWindow, directContainerPollInterval, restartBaseline)
}

func waitContainerReady(
	ctx context.Context,
	inspect containerInspectFunc,
	stabilityWindow time.Duration,
	pollInterval time.Duration,
) error {
	return waitContainerReadyFromRestartCount(ctx, inspect, stabilityWindow, pollInterval, 0)
}

func waitContainerReadyFromRestartCount(
	ctx context.Context,
	inspect containerInspectFunc,
	stabilityWindow time.Duration,
	pollInterval time.Duration,
	restartBaseline int,
) error {
	if inspect == nil || stabilityWindow <= 0 || pollInterval <= 0 || restartBaseline < 0 {
		return errors.New("Docker 容器就绪检查配置无效")
	}
	started := time.Now()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		result, err := inspect(ctx)
		if err != nil {
			return fmt.Errorf("检查新 Docker 容器状态失败: %w", err)
		}
		state := result.Container.State
		if state == nil {
			return fmt.Errorf("%w: 没有可读取的状态", ErrContainerNotRunning)
		}
		if state.Restarting || state.Status == container.StateRestarting || result.Container.RestartCount > restartBaseline {
			return fmt.Errorf("%w: status=%s restart_count=%d exit_code=%d",
				ErrContainerRestarted, state.Status, result.Container.RestartCount, state.ExitCode)
		}
		if !state.Running || state.Status != container.StateRunning {
			return fmt.Errorf("%w: status=%s exit_code=%d", ErrContainerNotRunning, state.Status, state.ExitCode)
		}
		if state.Health != nil {
			switch state.Health.Status {
			case container.Healthy:
				return nil
			case container.Unhealthy:
				return fmt.Errorf("%w: failing_streak=%d", ErrContainerUnhealthy, state.Health.FailingStreak)
			case container.Starting:
				// 继续等待 Docker 健康检查给出最终结果。
			case container.NoHealthcheck:
				if time.Since(started) >= stabilityWindow {
					return nil
				}
			default:
				return fmt.Errorf("新 Docker 容器返回未知健康状态: %s", state.Health.Status)
			}
		} else if time.Since(started) >= stabilityWindow {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", ErrContainerReadinessTimeout, ctx.Err())
		case <-ticker.C:
		}
	}
}
