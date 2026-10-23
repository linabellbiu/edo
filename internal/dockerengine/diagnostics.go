package dockerengine

import (
	"context"
	"errors"
	"strings"

	"github.com/moby/moby/client"
)

var ErrDiagnosticIdentity = errors.New("容器与当前部署不一致，请重新查询部署实例")

type ContainerDiagnostic struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Image        string `json:"image"`
	State        string `json:"state"`
	Health       string `json:"health,omitempty"`
	ExitCode     int    `json:"exit_code"`
	OOMKilled    bool   `json:"oom_killed"`
	RestartCount int    `json:"restart_count"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at"`
	Error        string `json:"error,omitempty"`
}

// InspectManagedContainer 不返回环境变量、启动命令、挂载内容或完整标签。
func (s *Service) InspectManagedContainer(ctx context.Context, endpointID, containerID, targetID, deploymentID, composeService string) (*ContainerDiagnostic, error) {
	if endpointID == "" || containerID == "" || targetID == "" || deploymentID == "" {
		return nil, ErrDiagnosticIdentity
	}
	apiClient, err := s.executionClient(ctx, endpointID)
	if err != nil {
		return nil, err
	}
	defer apiClient.Close()
	result, err := apiClient.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	item := result.Container
	if item.ID != containerID || item.Config == nil || item.State == nil {
		return nil, ErrDiagnosticIdentity
	}
	labels := item.Config.Labels
	if composeService != "" {
		if labels["com.docker.compose.project"] != composeProjectName(targetID) || labels["com.docker.compose.service"] != composeService {
			return nil, ErrDiagnosticIdentity
		}
	} else if labels["edo.managed"] != "true" || labels["edo.deployment.target.id"] != targetID || labels["edo.deployment.id"] != deploymentID {
		return nil, ErrDiagnosticIdentity
	}
	view := &ContainerDiagnostic{ID: item.ID, Name: strings.TrimPrefix(item.Name, "/"), Image: item.Image, State: string(item.State.Status), ExitCode: item.State.ExitCode, OOMKilled: item.State.OOMKilled, RestartCount: item.RestartCount, StartedAt: item.State.StartedAt, FinishedAt: item.State.FinishedAt, Error: item.State.Error}
	if item.State.Health != nil {
		view.Health = string(item.State.Health.Status)
	}
	return view, nil
}
