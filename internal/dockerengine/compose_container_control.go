package dockerengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/moby/moby/client"
)

var ErrContainerControlChanged = errors.New("容器状态或服务副本已变化，请重新确认")

type ComposeContainerControlPlan struct {
	Service         string                  `json:"service"`
	Action          string                  `json:"action"`
	ContainerID     string                  `json:"container_id"`
	Digest          string                  `json:"digest"`
	RunToCompletion bool                    `json:"run_to_completion"`
	Containers      []ComposeContainerState `json:"containers"`
	Others          []ComposeContainerState `json:"-"`
}

func (s *Service) ComposeContainerControlPlan(ctx context.Context, endpointID string, scope ComposeProjectScope, containerID, action string) (*ComposeContainerControlPlan, error) {
	if action != "stop" && action != "restart" && action != "recreate" {
		return nil, ErrRuntimeControlInvalid
	}
	api, err := s.composeProjectClient(ctx, endpointID, scope)
	if err != nil {
		return nil, err
	}
	defer api.Close()
	_, items, err := inspectComposeProject(ctx, api, scope)
	if err != nil {
		return nil, err
	}
	var selected *composeProjectContainer
	for i := range items {
		if items[i].id == containerID {
			selected = &items[i]
			break
		}
	}
	if selected == nil {
		return nil, ErrContainerControlChanged
	}
	plan := &ComposeContainerControlPlan{Action: action, Service: selected.service, ContainerID: containerID, Containers: []ComposeContainerState{}, Others: []ComposeContainerState{}}
	for _, name := range scope.CompletionServices {
		plan.RunToCompletion = plan.RunToCompletion || name == selected.service
	}
	for _, item := range items {
		if item.id == containerID || action == "recreate" && item.service == selected.service {
			plan.Containers = append(plan.Containers, item.detail)
		} else {
			plan.Others = append(plan.Others, item.detail)
		}
	}
	// 确认摘要绑定动作、冻结目标、当前容器身份及运行代次，不包含任何敏感配置。
	payload, _ := json.Marshal([]any{endpointID, scope, action, containerID, plan.Containers, plan.Others})
	sum := sha256.Sum256(payload)
	plan.Digest = hex.EncodeToString(sum[:])
	return plan, nil
}

func (s *Service) ControlComposeContainer(ctx context.Context, endpointID string, scope ComposeProjectScope, containerID, action, expectedDigest string, authorize func() error) (RuntimeState, error) {
	if action != "stop" && action != "restart" {
		return RuntimeState{}, ErrRuntimeControlInvalid
	}
	plan, err := s.ComposeContainerControlPlan(ctx, endpointID, scope, containerID, action)
	if err != nil {
		return RuntimeState{}, err
	}
	if expectedDigest == "" || plan.Digest != expectedDigest {
		return RuntimeState{}, ErrContainerControlChanged
	}
	api, err := s.composeProjectClient(ctx, endpointID, scope)
	if err != nil {
		return RuntimeState{}, err
	}
	defer api.Close()
	item, err := inspectComposeProjectContainer(ctx, api, scope, containerID, plan.Service)
	if err != nil {
		return RuntimeState{}, err
	}
	if !sameComposeContainerGeneration(plan.Containers[0], item.detail) {
		return RuntimeState{}, ErrContainerControlChanged
	}
	timeout := 20
	if authorize != nil {
		if err = authorize(); err != nil {
			return RuntimeState{}, err
		}
	}
	if action == "stop" {
		if item.live {
			_, err = api.ContainerStop(ctx, containerID, client.ContainerStopOptions{Timeout: &timeout})
		}
	} else if item.live {
		_, err = api.ContainerRestart(ctx, containerID, client.ContainerRestartOptions{Timeout: &timeout})
	} else {
		_, err = api.ContainerStart(ctx, containerID, client.ContainerStartOptions{})
	}
	if err != nil {
		return RuntimeState{}, fmt.Errorf("%w: %v", ErrRuntimeControlFailed, err)
	}
	if action == "restart" {
		if plan.RunToCompletion {
			err = waitComposeCompletion(ctx, api, scope, containerID, plan.Service)
		} else {
			err = waitContainerHealthyWithRestartBaseline(ctx, api, containerID, item.restarts)
		}
		if err != nil {
			return RuntimeState{}, err
		}
	}
	state, after, err := inspectComposeProject(ctx, api, scope)
	if err != nil {
		return RuntimeState{}, err
	}
	for _, item := range after {
		if item.id == containerID && (action != "stop" || !item.live) {
			if action == "restart" && !item.ready {
				return RuntimeState{}, ErrRuntimeControlFailed
			}
			state.RuntimeID = endpointID
			return state, nil
		}
	}
	return RuntimeState{}, ErrContainerControlChanged
}

func sameComposeContainerGeneration(a, b ComposeContainerState) bool {
	equalTime := func(a, b *time.Time) bool { return a == nil && b == nil || a != nil && b != nil && a.Equal(*b) }
	return a.ID == b.ID && a.ImageID == b.ImageID && a.State == b.State && a.RestartCount == b.RestartCount && equalTime(a.StartedAt, b.StartedAt)
}

func waitComposeCompletion(ctx context.Context, api *client.Client, scope ComposeProjectScope, id, service string) error {
	for {
		item, err := inspectComposeProjectContainer(ctx, api, scope, id, service)
		if err != nil {
			return err
		}
		if item.state == "completed" {
			return nil
		}
		if item.state != "completing" {
			return ErrRuntimeControlFailed
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// VerifyComposeRecreated 检查副本确已换成新容器、镜像未变，且其他服务容器运行代次保持不变。
func (s *Service) VerifyComposeRecreated(ctx context.Context, endpointID string, scope ComposeProjectScope, plan *ComposeContainerControlPlan) (RuntimeState, error) {
	api, err := s.composeProjectClient(ctx, endpointID, scope)
	if err != nil {
		return RuntimeState{}, err
	}
	defer api.Close()
	state, items, err := inspectComposeProject(ctx, api, scope)
	if err != nil {
		return RuntimeState{}, err
	}
	if len(items) != len(plan.Containers)+len(plan.Others) {
		return RuntimeState{}, ErrContainerControlChanged
	}
	selected := []composeProjectContainer{}
	oldIDs := map[string]bool{}
	images := map[string]bool{}
	for _, old := range plan.Containers {
		oldIDs[old.ID] = true
		images[old.ImageID] = true
	}
	for _, item := range items {
		if item.service == plan.Service {
			if oldIDs[item.id] || !images[item.detail.ImageID] {
				return RuntimeState{}, ErrContainerControlChanged
			}
			selected = append(selected, item)
		}
	}
	if len(selected) != len(plan.Containers) {
		return RuntimeState{}, ErrContainerControlChanged
	}
	for _, old := range plan.Others {
		found := false
		for _, item := range items {
			if old.ID == item.id {
				found = sameComposeContainerGeneration(old, item.detail)
				break
			}
		}
		if !found {
			return RuntimeState{}, ErrContainerControlChanged
		}
	}
	for _, item := range selected {
		if plan.RunToCompletion {
			err = waitComposeCompletion(ctx, api, scope, item.id, item.service)
		} else {
			err = waitContainerHealthyWithRestartBaseline(ctx, api, item.id, 0)
		}
		if err != nil {
			return RuntimeState{}, err
		}
	}
	state, _, err = inspectComposeProject(ctx, api, scope)
	if err == nil {
		for _, service := range state.Services {
			if service.Name == plan.Service && (service.Count == 0 || service.ReadyCount != service.Count) {
				return RuntimeState{}, ErrRuntimeControlFailed
			}
		}
	}
	state.RuntimeID = endpointID
	return state, err
}
