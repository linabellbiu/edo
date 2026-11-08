package dockerengine

import (
	"context"
	"github.com/moby/moby/client"
	"strings"
)

// NamedContainerState 只读取明确给定的容器；调用方负责核验应用绑定，不枚举主机容器。
func (s *Service) NamedContainerState(ctx context.Context, endpoint, name string) (ComposeContainerState, map[string]string, error) {
	api, err := s.executionClient(ctx, endpoint)
	if err != nil {
		return ComposeContainerState{}, nil, err
	}
	defer api.Close()
	result, err := api.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return ComposeContainerState{}, nil, err
	}
	c := result.Container
	if c.Config == nil || c.State == nil || strings.TrimPrefix(c.Name, "/") != name {
		return ComposeContainerState{}, nil, ErrContainerControlChanged
	}
	view := ComposeContainerState{ID: c.ID, Name: name, Image: c.Config.Image, ImageDisplay: compactContainerImageReference(c.Config.Image), ImageID: c.Image, State: string(c.State.Status), Running: c.State.Running, CreatedAt: composeContainerTime(c.Created), StartedAt: composeContainerTime(c.State.StartedAt), RestartCount: c.RestartCount, FinishedAt: composeContainerTime(c.State.FinishedAt)}
	if c.State.Health != nil {
		view.Health = string(c.State.Health.Status)
		if view.Running && (view.Health == "unhealthy" || view.Health == "starting") {
			view.State = view.Health
		}
	}
	if !view.Running || c.State.Paused || c.State.Restarting {
		view.TerminalUnavailableReason = "容器当前未运行或正在重启"
	}
	return view, map[string]string{"deployment_id": c.Config.Labels["edo.deployment.id"], "project": c.Config.Labels["com.docker.compose.project"]}, nil
}
