package dockerengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/moby/moby/client"
)

// NativeContainerControlPlan 同时核对名称、精确 ID、发布和目标标签，避免名称被替换后操作其他实例。
func (s *Service) NativeContainerControlPlan(ctx context.Context, endpointID, targetID, deploymentID, name, containerID, action string) (*ComposeContainerControlPlan, error) {
	if action != "restart" && action != "stop" && action != "recreate" {
		return nil, ErrRuntimeControlInvalid
	}
	api, err := s.executionClient(ctx, endpointID)
	if err != nil {
		return nil, err
	}
	defer api.Close()
	inspected, err := api.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	c := inspected.Container
	if c.ID != containerID || c.Config == nil || c.State == nil || c.Config.Labels["edo.managed"] != "true" || c.Config.Labels["edo.deployment.target.id"] != targetID || c.Config.Labels["edo.deployment.id"] != deploymentID {
		return nil, ErrContainerControlChanged
	}
	detail := ComposeContainerState{ID: c.ID, Name: strings.TrimPrefix(c.Name, "/"), Image: c.Config.Image, ImageDisplay: compactContainerImageReference(c.Config.Image), ImageID: c.Image, State: string(c.State.Status), Running: c.State.Running, CreatedAt: composeContainerTime(c.Created), StartedAt: composeContainerTime(c.State.StartedAt), RestartCount: c.RestartCount, ConfigurationRevision: c.Config.Labels[ConfigurationRevisionLabel]}
	plan := &ComposeContainerControlPlan{Kind: "docker", Service: name, Action: action, ContainerID: c.ID, Containers: []ComposeContainerState{detail}, Others: []ComposeContainerState{}, DockerIdentity: &ContainerRollbackIdentity{ContainerID: c.ID, ImageID: c.Image, DeploymentID: deploymentID, TargetID: targetID, Managed: true, PreviousImageAvailable: true}}
	payload, _ := json.Marshal([]any{endpointID, targetID, deploymentID, action, detail, detail.ConfigurationRevision})
	sum := sha256.Sum256(payload)
	plan.Digest = hex.EncodeToString(sum[:])
	return plan, nil
}
