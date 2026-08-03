package dockerengine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

var (
	ErrRuntimeControlInvalid   = errors.New("Docker 运行控制参数无效")
	ErrRuntimeResourceMissing  = errors.New("Docker 运行资源不存在")
	ErrRuntimeControlFailed    = errors.New("Docker 运行资源操作失败")
	ErrRuntimeStateUnavailable = errors.New("Docker 运行状态不可用")
)

type RuntimeState struct {
	Kind       string `json:"kind"`
	ResourceID string `json:"resource_id,omitempty"`
	Name       string `json:"name"`
	State      string `json:"state"`
	Running    bool   `json:"running"`
	Count      int    `json:"count"`
}

// ContainerRuntimeState 按发布快照中的固定容器名读取状态，不接受浏览器传入任意 Docker 资源。
func (s *Service) ContainerRuntimeState(ctx context.Context, endpointID, containerName string) (RuntimeState, error) {
	containerName = strings.TrimSpace(containerName)
	if s == nil || strings.TrimSpace(endpointID) == "" || containerName == "" {
		return RuntimeState{}, ErrRuntimeControlInvalid
	}
	apiClient, err := s.executionClient(ctx, endpointID)
	if err != nil {
		return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeStateUnavailable, err)
	}
	defer apiClient.Close()
	return inspectContainerRuntimeState(ctx, apiClient, containerName)
}

func (s *Service) ControlContainer(ctx context.Context, endpointID, containerName, action string) (RuntimeState, error) {
	containerName = strings.TrimSpace(containerName)
	if s == nil || strings.TrimSpace(endpointID) == "" || containerName == "" || (action != "restart" && action != "stop") {
		return RuntimeState{}, ErrRuntimeControlInvalid
	}
	apiClient, err := s.executionClient(ctx, endpointID)
	if err != nil {
		return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
	}
	defer apiClient.Close()
	before, err := apiClient.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{})
	if err != nil {
		return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeResourceMissing, err)
	}
	if before.Container.State == nil {
		return RuntimeState{}, ErrRuntimeStateUnavailable
	}
	stopTimeout := 20
	if action == "stop" {
		if before.Container.State.Running {
			if _, err := apiClient.ContainerStop(ctx, before.Container.ID, client.ContainerStopOptions{Timeout: &stopTimeout}); err != nil {
				return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
			}
		}
		state, err := inspectContainerRuntimeState(ctx, apiClient, before.Container.ID)
		if err != nil {
			return RuntimeState{}, err
		}
		if state.Running {
			return RuntimeState{}, ErrRuntimeControlFailed
		}
		return state, nil
	}

	restartBaseline := before.Container.RestartCount
	if before.Container.State.Running {
		if _, err := apiClient.ContainerRestart(ctx, before.Container.ID, client.ContainerRestartOptions{Timeout: &stopTimeout}); err != nil {
			return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
		}
	} else if _, err := apiClient.ContainerStart(ctx, before.Container.ID, client.ContainerStartOptions{}); err != nil {
		return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
	}
	if err := waitContainerHealthyWithRestartBaseline(ctx, apiClient, before.Container.ID, restartBaseline); err != nil {
		return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
	}
	return inspectContainerRuntimeState(ctx, apiClient, before.Container.ID)
}

// RemoveContainer 按发布快照中的固定容器名强制移除单个容器，不删除镜像和数据卷。
// 容器已经不存在时仍视为成功，允许上层停止继续监控失效的历史实例。
func (s *Service) RemoveContainer(ctx context.Context, endpointID, containerName string) error {
	containerName = strings.TrimSpace(containerName)
	if s == nil || strings.TrimSpace(endpointID) == "" || containerName == "" {
		return ErrRuntimeControlInvalid
	}
	apiClient, err := s.executionClient(ctx, endpointID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
	}
	defer apiClient.Close()
	if _, err := apiClient.ContainerRemove(ctx, containerName, client.ContainerRemoveOptions{Force: true}); err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
	}
	return nil
}

func (s *Service) ComposeRuntimeState(ctx context.Context, endpointID, targetID, serviceName string) (RuntimeState, error) {
	if s == nil || strings.TrimSpace(endpointID) == "" || strings.TrimSpace(targetID) == "" || strings.TrimSpace(serviceName) == "" {
		return RuntimeState{}, ErrRuntimeControlInvalid
	}
	apiClient, err := s.executionClient(ctx, endpointID)
	if err != nil {
		return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeStateUnavailable, err)
	}
	defer apiClient.Close()
	return inspectComposeRuntimeState(ctx, apiClient, targetID, serviceName)
}

func (s *Service) ControlCompose(ctx context.Context, endpointID, targetID, serviceName, action string) (RuntimeState, error) {
	if s == nil || strings.TrimSpace(endpointID) == "" || strings.TrimSpace(targetID) == "" ||
		strings.TrimSpace(serviceName) == "" || (action != "restart" && action != "stop") {
		return RuntimeState{}, ErrRuntimeControlInvalid
	}
	apiClient, err := s.executionClient(ctx, endpointID)
	if err != nil {
		return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
	}
	defer apiClient.Close()
	containers, err := composeServiceContainers(ctx, apiClient, composeProjectName(targetID), serviceName)
	if err != nil {
		return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeStateUnavailable, err)
	}
	if len(containers) == 0 {
		return RuntimeState{}, ErrRuntimeResourceMissing
	}
	stopTimeout := 20
	restartBaselines := make(map[string]int, len(containers))
	for _, item := range containers {
		inspected, err := apiClient.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if err != nil || inspected.Container.State == nil {
			return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeStateUnavailable, err)
		}
		restartBaselines[item.ID] = inspected.Container.RestartCount
		if action == "stop" {
			if inspected.Container.State.Running {
				if _, err := apiClient.ContainerStop(ctx, item.ID, client.ContainerStopOptions{Timeout: &stopTimeout}); err != nil {
					return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
				}
			}
			continue
		}
		if inspected.Container.State.Running {
			if _, err := apiClient.ContainerRestart(ctx, item.ID, client.ContainerRestartOptions{Timeout: &stopTimeout}); err != nil {
				return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
			}
		} else if _, err := apiClient.ContainerStart(ctx, item.ID, client.ContainerStartOptions{}); err != nil {
			return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
		}
	}
	if action == "restart" {
		for _, item := range containers {
			if err := waitContainerHealthyWithRestartBaseline(ctx, apiClient, item.ID, restartBaselines[item.ID]); err != nil {
				return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
			}
		}
	}
	state, err := inspectComposeRuntimeState(ctx, apiClient, targetID, serviceName)
	if err != nil {
		return RuntimeState{}, err
	}
	if action == "stop" && state.Running {
		return RuntimeState{}, ErrRuntimeControlFailed
	}
	return state, nil
}

func inspectContainerRuntimeState(ctx context.Context, apiClient *client.Client, containerID string) (RuntimeState, error) {
	result, err := apiClient.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeResourceMissing, err)
	}
	if result.Container.State == nil {
		return RuntimeState{}, ErrRuntimeStateUnavailable
	}
	return RuntimeState{
		Kind: "docker", ResourceID: result.Container.ID, Name: strings.TrimPrefix(result.Container.Name, "/"),
		State: string(result.Container.State.Status), Running: result.Container.State.Running, Count: 1,
	}, nil
}

func inspectComposeRuntimeState(ctx context.Context, apiClient *client.Client, targetID, serviceName string) (RuntimeState, error) {
	items, err := composeServiceContainers(ctx, apiClient, composeProjectName(targetID), serviceName)
	if err != nil {
		return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeStateUnavailable, err)
	}
	if len(items) == 0 {
		return RuntimeState{}, ErrRuntimeResourceMissing
	}
	running := true
	state := "running"
	for _, item := range items {
		result, err := apiClient.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if err != nil || result.Container.State == nil {
			return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeStateUnavailable, err)
		}
		if !result.Container.State.Running {
			running = false
			state = string(result.Container.State.Status)
		}
	}
	return RuntimeState{Kind: "compose", Name: serviceName, State: state, Running: running, Count: len(items)}, nil
}
