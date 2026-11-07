package dockerengine

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"edo/internal/model"
	"github.com/moby/moby/client"
)

// ComposeProjectScope 来自已成功发布的冻结配置，不接受客户端指定项目或服务。
type ComposeProjectScope struct {
	ConfigurationDirectory  string
	ConfigurationParameters string
	ConfigurationFiles      []model.DeploymentConfigFile
	UseSudo                 bool
	EnvFile                 string
	TagVariable             string
	HostID                  string
	ProjectName             string
	WorkingDirectory        string
	Services                []string
	CompletionServices      []string
}

type ComposeServiceState struct {
	Name            string                  `json:"name"`
	State           string                  `json:"state"`
	Count           int                     `json:"count"`
	ReadyCount      int                     `json:"ready_count"`
	RunToCompletion bool                    `json:"run_to_completion,omitempty"`
	Containers      []ComposeContainerState `json:"containers"`
}

// ComposeContainerState 只包含容器基本信息，不返回环境变量、命令、挂载或完整标签。
type ComposeContainerState struct {
	ConfigurationHash         string                    `json:"-"`
	ConfigurationRevision     string                    `json:"-"`
	ID                        string                    `json:"id"`
	Name                      string                    `json:"name"`
	Image                     string                    `json:"image"`
	ImageDisplay              string                    `json:"image_display"`
	ImageID                   string                    `json:"image_id"`
	State                     string                    `json:"state"`
	Running                   bool                      `json:"running"`
	Health                    string                    `json:"health,omitempty"`
	CreatedAt                 *time.Time                `json:"created_at,omitempty"`
	StartedAt                 *time.Time                `json:"started_at,omitempty"`
	FinishedAt                *time.Time                `json:"finished_at,omitempty"`
	RestartCount              int                       `json:"restart_count"`
	ExitCode                  int                       `json:"exit_code"`
	OOMKilled                 bool                      `json:"oom_killed"`
	RestartPolicy             string                    `json:"restart_policy,omitempty"`
	TerminalUnavailableReason string                    `json:"terminal_unavailable_reason,omitempty"`
	Ports                     []ComposeContainerPort    `json:"ports"`
	Networks                  []ComposeContainerNetwork `json:"networks"`
}

type ComposeContainerPort struct {
	ContainerPort string `json:"container_port"`
	HostIP        string `json:"host_ip,omitempty"`
	HostPort      string `json:"host_port,omitempty"`
}

type ComposeContainerNetwork struct {
	Name      string `json:"name"`
	IPAddress string `json:"ip_address,omitempty"`
}

var runtimeComposeProjectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

type composeProjectContainer struct {
	id, service, state string
	ready, live        bool
	restarts           int
	detail             ComposeContainerState
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
	completed := map[string]bool{}
	for _, name := range scope.CompletionServices {
		if !seen[name] || completed[name] {
			return nil, ErrRuntimeControlInvalid
		}
		completed[name] = true
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
	state.RuntimeID = endpointID
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
	completion := map[string]bool{}
	for _, name := range scope.CompletionServices {
		completion[name] = true
	}
	mutable := 0
	for _, item := range containers {
		if completion[item.service] {
			if item.state != "completed" {
				return RuntimeState{}, fmt.Errorf("%w: Compose 一次性任务尚未成功完成", ErrRuntimeControlFailed)
			}
		} else {
			mutable++
		}
	}
	if mutable == 0 {
		return RuntimeState{}, ErrRuntimeControlInvalid
	}
	stopTimeout := 20
	for _, item := range containers {
		if completion[item.service] {
			continue
		}
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
			if completion[item.service] {
				continue
			}
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
		if !ids[item.id] || (action == "stop" && item.live) || (completion[item.service] && item.state != "completed") {
			return RuntimeState{}, ErrRuntimeControlFailed
		}
	}
	if action == "restart" && !state.Running {
		return RuntimeState{}, ErrRuntimeControlFailed
	}
	state.RuntimeID = endpointID
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
	state := RuntimeState{Kind: "compose", Name: scope.ProjectName, Count: len(containers), Services: []ComposeServiceState{}}
	completion := map[string]bool{}
	for _, name := range scope.CompletionServices {
		completion[name] = true
	}
	allReady, allDaemonsExited, completionFailed := true, true, false
	readyDaemons, daemonContainers, issue := 0, 0, ""
	for _, name := range scope.Services {
		service := ComposeServiceState{Name: name, State: "missing", RunToCompletion: completion[name], Containers: []ComposeContainerState{}}
		for _, item := range containers {
			if item.service != name {
				continue
			}
			service.Count++
			service.Containers = append(service.Containers, item.detail)
			if item.ready {
				service.ReadyCount++
			}
			if service.State == "missing" || composeStatePriority(item.state) > composeStatePriority(service.State) {
				service.State = item.state
			}
		}
		ready := service.Count > 0 && service.ReadyCount == service.Count
		if ready {
			if service.RunToCompletion {
				service.State = "completed"
			} else {
				service.State = "running"
			}
		} else if service.ReadyCount > 0 {
			service.State = "partial"
		}
		if !ready {
			allReady = false
			if issue == "" || composeStatePriority(service.State) > composeStatePriority(issue) {
				issue = service.State
			}
		}
		if service.RunToCompletion {
			if !ready {
				completionFailed = true
			}
		} else {
			daemonContainers += service.Count
			if service.Count == 0 || service.State != "exited" {
				allDaemonsExited = false
			}
			if service.ReadyCount > 0 {
				readyDaemons++
			}
		}
		state.Services = append(state.Services, service)
	}
	switch {
	case allReady && readyDaemons > 0:
		state.State, state.Running = "running", true
	case allReady:
		state.State = "completed"
	case len(containers) == 0:
		state.State = "missing"
	case !completionFailed && allDaemonsExited && daemonContainers > 0:
		state.State = "exited"
	case readyDaemons > 0:
		state.State = "partial"
	default:
		state.State = issue
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
	ready := c.State.Running && state == "running"
	for _, name := range scope.CompletionServices {
		if name != service {
			continue
		}
		ready = false
		if state == "exited" && !c.State.Running {
			if c.State.ExitCode == 0 && !c.State.OOMKilled && c.State.Error == "" {
				state, ready = "completed", true
			} else {
				state = "failed"
			}
		} else if c.State.Running && (state == "running" || state == "starting") {
			state = "completing"
		}
	}
	detail := ComposeContainerState{
		ID: id, Name: strings.TrimPrefix(c.Name, "/"), Image: c.Config.Image,
		ConfigurationRevision: c.Config.Labels[ConfigurationRevisionLabel], ConfigurationHash: c.Config.Labels["com.docker.compose.config-hash"],
		ImageDisplay: compactContainerImageReference(c.Config.Image), ImageID: c.Image,
		State: state, Running: c.State.Running, CreatedAt: composeContainerTime(c.Created),
		StartedAt: composeContainerTime(c.State.StartedAt), FinishedAt: composeContainerTime(c.State.FinishedAt),
		RestartCount: c.RestartCount, ExitCode: c.State.ExitCode, OOMKilled: c.State.OOMKilled,
		Ports: []ComposeContainerPort{}, Networks: []ComposeContainerNetwork{},
	}
	if c.State.Health != nil {
		detail.Health = string(c.State.Health.Status)
	}
	if c.HostConfig != nil {
		detail.RestartPolicy = string(c.HostConfig.RestartPolicy.Name)
	}
	if c.HostConfig == nil || c.HostConfig.Init == nil || !*c.HostConfig.Init || !c.HostConfig.PidMode.IsPrivate() {
		detail.TerminalUnavailableReason = "容器终端需要启用 init 并使用独立 PID 命名空间"
	}
	if c.NetworkSettings != nil {
		for port, bindings := range c.NetworkSettings.Ports {
			if len(bindings) == 0 {
				detail.Ports = append(detail.Ports, ComposeContainerPort{ContainerPort: port.String()})
			}
			for _, binding := range bindings {
				hostIP := ""
				if binding.HostIP.IsValid() {
					hostIP = binding.HostIP.String()
				}
				detail.Ports = append(detail.Ports, ComposeContainerPort{ContainerPort: port.String(), HostIP: hostIP, HostPort: binding.HostPort})
			}
		}
		sort.Slice(detail.Ports, func(i, j int) bool {
			a, b := detail.Ports[i], detail.Ports[j]
			if a.ContainerPort != b.ContainerPort {
				return a.ContainerPort < b.ContainerPort
			}
			if a.HostIP != b.HostIP {
				return a.HostIP < b.HostIP
			}
			return a.HostPort < b.HostPort
		})
		for name, network := range c.NetworkSettings.Networks {
			if network != nil {
				address := ""
				if network.IPAddress.IsValid() {
					address = network.IPAddress.String()
				}
				detail.Networks = append(detail.Networks, ComposeContainerNetwork{Name: name, IPAddress: address})
			}
		}
		sort.Slice(detail.Networks, func(i, j int) bool { return detail.Networks[i].Name < detail.Networks[j].Name })
	}
	return composeProjectContainer{id: id, service: service, state: state, live: c.State.Running, ready: ready, restarts: c.RestartCount, detail: detail}, nil
}

func composeContainerTime(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() {
		return nil
	}
	return &parsed
}

func composeStatePriority(state string) int {
	switch state {
	case "failed", "dead":
		return 7
	case "missing":
		return 6
	case "unhealthy":
		return 5
	case "partial":
		return 4
	case "starting", "completing", "restarting":
		return 3
	case "paused", "created":
		return 2
	case "exited":
		return 1
	default:
		return 0
	}
}
