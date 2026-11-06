package dockerengine

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/moby/moby/client"
)

// ComposeProjectScope 来自已成功发布的冻结配置，不接受客户端指定项目或服务。
type ComposeProjectScope struct {
	HostID           string
	ProjectName      string
	WorkingDirectory string
	Services         []string
}

type ComposeServiceState struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	Count      int    `json:"count"`
	ReadyCount int    `json:"ready_count"`
}

var runtimeComposeProjectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

type composeProjectContainer struct {
	id, service, state string
	running, live      bool
	restarts           int
}

func (s *Service) composeProjectClient(ctx context.Context, endpointID string, scope ComposeProjectScope) (*client.Client, error) {
	if s == nil || scope.HostID == "" || !runtimeComposeProjectPattern.MatchString(scope.ProjectName) || !path.IsAbs(scope.WorkingDirectory) || len(scope.Services) == 0 || len(scope.Services) > 64 {
		return nil, ErrRuntimeControlInvalid
	}
	seen := map[string]bool{}
	for _, name := range scope.Services {
		if !composeServicePattern.MatchString(name) || seen[name] {
			return nil, ErrRuntimeControlInvalid
		}
		seen[name] = true
	}
	endpoint, err := s.Find(ctx, endpointID)
	if err != nil {
		return nil, err
	}
	if endpoint.HostID != scope.HostID || !endpoint.IsActive {
		return nil, ErrRuntimeControlInvalid
	}
	return s.clientForEndpointWithTimeout(ctx, endpoint, 0)
}

func (s *Service) ComposeProjectRuntimeState(ctx context.Context, endpointID string, scope ComposeProjectScope) (RuntimeState, error) {
	apiClient, err := s.composeProjectClient(ctx, endpointID, scope)
	if err != nil {
		return RuntimeState{}, err
	}
	defer apiClient.Close()
	state, _, err := inspectComposeProject(ctx, apiClient, scope)
	return state, err
}

func (s *Service) ControlComposeProject(ctx context.Context, endpointID string, scope ComposeProjectScope, action string) (RuntimeState, error) {
	if action != "stop" && action != "restart" {
		return RuntimeState{}, ErrRuntimeControlInvalid
	}
	apiClient, err := s.composeProjectClient(ctx, endpointID, scope)
	if err != nil {
		return RuntimeState{}, err
	}
	defer apiClient.Close()
	_, containers, err := inspectComposeProject(ctx, apiClient, scope)
	if err != nil {
		return RuntimeState{}, err
	}
	// 缺失服务时不能把操作已有部分称为整个发布范围操作成功。
	seen := map[string]bool{}
	for _, item := range containers {
		seen[item.service] = true
	}
	if len(seen) != len(scope.Services) {
		return RuntimeState{}, ErrRuntimeResourceMissing
	}
	stopTimeout := 20
	for _, item := range containers {
		// 每次写操作前重新核对固定容器 ID 及 Compose 归属。
		if _, err := inspectComposeProjectContainer(ctx, apiClient, scope, item.id, item.service); err != nil {
			return RuntimeState{}, err
		}
		if action == "stop" {
			if !item.live {
				continue
			}
			if _, err = apiClient.ContainerStop(ctx, item.id, client.ContainerStopOptions{Timeout: &stopTimeout}); err != nil {
				return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
			}
		} else if item.live {
			if _, err = apiClient.ContainerRestart(ctx, item.id, client.ContainerRestartOptions{Timeout: &stopTimeout}); err != nil {
				return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
			}
		} else if _, err = apiClient.ContainerStart(ctx, item.id, client.ContainerStartOptions{}); err != nil {
			return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
		}
	}
	if action == "restart" {
		for _, item := range containers {
			if err = waitContainerHealthyWithRestartBaseline(ctx, apiClient, item.id, item.restarts); err != nil {
				return RuntimeState{}, err
			}
		}
	}
	state, after, err := inspectComposeProject(ctx, apiClient, scope)
	if err != nil {
		return RuntimeState{}, err
	}
	// 操作期间容器被其他流程替换时拒绝宣称成功。
	ids := map[string]bool{}
	for _, item := range containers {
		ids[item.id] = true
	}
	if len(after) != len(containers) {
		return RuntimeState{}, ErrRuntimeControlFailed
	}
	for _, item := range after {
		if !ids[item.id] || (action == "stop" && item.live) {
			return RuntimeState{}, ErrRuntimeControlFailed
		}
	}
	if action == "restart" && !state.Running {
		return RuntimeState{}, ErrRuntimeControlFailed
	}
	return state, nil
}

func inspectComposeProject(ctx context.Context, apiClient *client.Client, scope ComposeProjectScope) (RuntimeState, []composeProjectContainer, error) {
	result, err := apiClient.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{"label": {"com.docker.compose.project=" + scope.ProjectName: true}}})
	if err != nil {
		return RuntimeState{}, nil, fmt.Errorf("%w: %v", ErrRuntimeStateUnavailable, err)
	}
	if len(result.Items) > 256 {
		return RuntimeState{}, nil, ErrRuntimeStateUnavailable
	}
	selected := map[string]bool{}
	for _, name := range scope.Services {
		selected[name] = true
	}
	containers := []composeProjectContainer{}
	for _, item := range result.Items {
		name := item.Labels["com.docker.compose.service"]
		if !selected[name] || strings.EqualFold(item.Labels["com.docker.compose.oneoff"], "true") {
			continue
		}
		inspected, err := inspectComposeProjectContainer(ctx, apiClient, scope, item.ID, name)
		if err != nil {
			return RuntimeState{}, nil, err
		}
		containers = append(containers, inspected)
	}
	sort.Slice(containers, func(i, j int) bool {
		return containers[i].service < containers[j].service || containers[i].service == containers[j].service && containers[i].id < containers[j].id
	})
	state := RuntimeState{Kind: "compose", Name: scope.ProjectName, Count: len(containers), Running: true, State: "running", Services: []ComposeServiceState{}}
	for _, name := range scope.Services {
		service := ComposeServiceState{Name: name, State: "missing"}
		for _, item := range containers {
			if item.service == name {
				service.Count++
				if item.running {
					service.ReadyCount++
				}
				if service.State == "missing" || service.State == "running" {
					service.State = item.state
				}
			}
		}
		if service.Count == 0 || service.ReadyCount != service.Count {
			state.Running = false
			if state.State == "running" {
				state.State = service.State
			}
		}
		if service.ReadyCount > 0 && service.ReadyCount < service.Count {
			service.State = "partial"
		}
		state.Services = append(state.Services, service)
	}
	if !state.Running {
		running := 0
		for _, item := range containers {
			if item.running {
				running++
			}
		}
		if running > 0 {
			state.State = "partial"
		} else if len(containers) == 0 {
			state.State = "missing"
		}
	}
	return state, containers, nil
}

func inspectComposeProjectContainer(ctx context.Context, apiClient *client.Client, scope ComposeProjectScope, id, service string) (composeProjectContainer, error) {
	result, err := apiClient.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return composeProjectContainer{}, fmt.Errorf("%w: %v", ErrRuntimeStateUnavailable, err)
	}
	c := result.Container
	if c.State == nil || c.Config == nil || c.ID != id || c.Config.Labels["com.docker.compose.project"] != scope.ProjectName || c.Config.Labels["com.docker.compose.service"] != service || strings.EqualFold(c.Config.Labels["com.docker.compose.oneoff"], "true") || path.Clean(c.Config.Labels["com.docker.compose.project.working_dir"]) != path.Clean(scope.WorkingDirectory) {
		return composeProjectContainer{}, ErrRuntimeControlInvalid
	}
	state := string(c.State.Status)
	if c.State.Running && state == "running" && c.State.Health != nil && (string(c.State.Health.Status) == "unhealthy" || string(c.State.Health.Status) == "starting") {
		state = string(c.State.Health.Status)
	}
	return composeProjectContainer{id: id, service: service, state: state, live: c.State.Running, running: c.State.Running && state == "running", restarts: c.RestartCount}, nil
}
