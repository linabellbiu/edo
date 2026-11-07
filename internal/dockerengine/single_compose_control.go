package dockerengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/moby/moby/client"
	"go.yaml.in/yaml/v3"
)

func (s *Service) SingleComposeContainerControlPlan(ctx context.Context, endpointID, targetID, service, containerID, action string) (*ComposeContainerControlPlan, error) {
	if action != "restart" && action != "stop" && action != "recreate" {
		return nil, ErrRuntimeControlInvalid
	}
	api, err := s.executionClient(ctx, endpointID)
	if err != nil {
		return nil, err
	}
	defer api.Close()
	items, err := composeServiceContainers(ctx, api, composeProjectName(targetID), service)
	if err != nil {
		return nil, err
	}
	plan := &ComposeContainerControlPlan{Kind: "compose", SingleCompose: true, Action: action, Service: service, ContainerID: containerID, Containers: []ComposeContainerState{}, Others: []ComposeContainerState{}}
	found := false
	revisions := map[string]string{}
	for _, item := range items {
		inspected, err := api.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if err != nil {
			return nil, err
		}
		c := inspected.Container
		if c.Config == nil || c.State == nil || c.Config.Labels["com.docker.compose.project"] != composeProjectName(targetID) || c.Config.Labels["com.docker.compose.service"] != service {
			return nil, ErrContainerControlChanged
		}
		detail := ComposeContainerState{ID: c.ID, Name: strings.TrimPrefix(c.Name, "/"), Image: c.Config.Image, ImageDisplay: compactContainerImageReference(c.Config.Image), ImageID: c.Image, State: string(c.State.Status), Running: c.State.Running, CreatedAt: composeContainerTime(c.Created), StartedAt: composeContainerTime(c.State.StartedAt), RestartCount: c.RestartCount, ConfigurationRevision: c.Config.Labels[ConfigurationRevisionLabel]}
		found = found || c.ID == containerID
		if action == "recreate" || c.ID == containerID {
			plan.Containers = append(plan.Containers, detail)
		} else {
			plan.Others = append(plan.Others, detail)
		}
		revisions[detail.ID] = detail.ConfigurationRevision
	}
	if !found {
		return nil, ErrContainerControlChanged
	}
	// SDK 列表顺序不稳定，确认摘要须与返回顺序无关。
	sortContainerStates(plan.Containers)
	sortContainerStates(plan.Others)
	payload, _ := json.Marshal([]any{endpointID, targetID, action, plan.Containers, plan.Others, revisions})
	sum := sha256.Sum256(payload)
	plan.Digest = hex.EncodeToString(sum[:])
	return plan, nil
}

func composeWithConfigurationRevision(source, service, revision string) (string, error) {
	if revision == "" {
		return source, nil
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(source), &doc); err != nil {
		return "", err
	}
	services, ok := doc["services"].(map[string]any)
	if !ok {
		return "", ErrInvalidComposeYAML
	}
	selected, ok := services[service].(map[string]any)
	if !ok {
		return "", ErrInvalidComposeYAML
	}
	labels := map[string]any{}
	switch existing := selected["labels"].(type) {
	case nil:
	case map[string]any:
		labels = existing
	case []any:
		for _, raw := range existing {
			value, ok := raw.(string)
			if !ok {
				return "", ErrInvalidComposeYAML
			}
			key, value, _ := strings.Cut(value, "=")
			labels[key] = value
		}
	default:
		return "", ErrInvalidComposeYAML
	}
	labels[ConfigurationRevisionLabel] = revision
	selected["labels"] = labels
	data, err := yaml.Marshal(doc)
	return string(data), err
}

// RecreateSingleCompose 复用冻结定义和本机已有镜像，只重建确认的服务副本，不构建、不拉取、不启动依赖。
func (s *Service) RecreateSingleCompose(ctx context.Context, input ComposeDeployInput, plan *ComposeContainerControlPlan, authorize func() error) (RuntimeState, error) {
	if !plan.SingleCompose || len(plan.Containers) == 0 {
		return RuntimeState{}, ErrRuntimeControlInvalid
	}
	current, err := s.SingleComposeContainerControlPlan(ctx, input.EndpointID, input.TargetID, input.ServiceName, plan.ContainerID, "recreate")
	if err != nil {
		return RuntimeState{}, err
	}
	expected := plan.BaseDigest
	if expected == "" {
		expected = plan.Digest
	}
	if current.Digest != expected {
		return RuntimeState{}, ErrContainerControlChanged
	}
	content, err := composeYAMLWithManagedImage(input.YAML, input.ServiceName)
	if err != nil {
		return RuntimeState{}, err
	}
	content, err = composeWithEnvironment(content, input.ServiceName, input.Environment)
	if err != nil {
		return RuntimeState{}, err
	}
	content, err = composeWithConfigurationRevision(content, input.ServiceName, configurationRevision(ctx))
	if err != nil {
		return RuntimeState{}, err
	}
	imageID := plan.Containers[0].ImageID
	for _, c := range plan.Containers {
		if c.ImageID != imageID {
			return RuntimeState{}, ErrContainerControlChanged
		}
	}
	endpoint, err := s.Find(ctx, input.EndpointID)
	if err != nil {
		return RuntimeState{}, err
	}
	api, err := s.executionClient(ctx, input.EndpointID)
	if err != nil {
		return RuntimeState{}, err
	}
	defer api.Close()
	image, err := api.ImageInspect(ctx, imageID)
	if err != nil || image.ID != imageID {
		return RuntimeState{}, ErrComposeImageUnavailable
	}
	input.Image = imageID
	input.YAML = content
	input.RecreateCount = len(plan.Containers)
	if authorize != nil {
		if err = authorize(); err != nil {
			return RuntimeState{}, err
		}
	}
	if err = s.runCompose(ctx, endpoint, composeProjectName(input.TargetID), input); err != nil {
		return RuntimeState{}, err
	}
	if err = waitComposeServiceHealthy(ctx, api, composeProjectName(input.TargetID), input.ServiceName); err != nil {
		return RuntimeState{}, err
	}
	items, err := composeServiceContainers(ctx, api, composeProjectName(input.TargetID), input.ServiceName)
	if err != nil || len(items) != len(plan.Containers) {
		return RuntimeState{}, ErrContainerControlChanged
	}
	for _, item := range items {
		for _, old := range plan.Containers {
			if item.ID == old.ID {
				return RuntimeState{}, ErrContainerControlChanged
			}
		}
		c, err := api.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if err != nil || c.Container.Image != imageID || c.Container.Config == nil || c.Container.Config.Labels[ConfigurationRevisionLabel] != configurationRevision(ctx) {
			return RuntimeState{}, fmt.Errorf("%w: 重建结果不一致", ErrComposeVerificationFailed)
		}
	}
	return inspectComposeRuntimeState(ctx, api, input.TargetID, input.ServiceName)
}

func sortContainerStates(items []ComposeContainerState) {
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
}
