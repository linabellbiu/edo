package deployment

import (
	"context"
	"crypto/sha256"
	"edo/internal/configfile"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/sshdeploy"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/distribution/reference"
	"io"
	"time"
)

func (s *Service) customRuntimeBindings(ctx context.Context, record *model.DeploymentRecord) ([]model.WorkflowRuntimeService, string, error) {
	if !isCustomScriptRuntime(record) {
		return nil, "", nil
	}
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).Where("uuid = ? AND application_id = ? AND project_id = ?", record.PipelineRunID, record.ApplicationID, record.ProjectID).First(&run).Error; err != nil {
		return nil, "", err
	}
	var snapshot struct {
		Stages []model.WorkflowStage `json:"stages"`
	}
	if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		return nil, "", err
	}
	for _, stage := range snapshot.Stages {
		for _, node := range stage.Tasks {
			if node.ID == record.WorkflowNodeID && node.Type == model.WorkflowNodeDeploy && node.Config.DeploymentPlanID == record.DeploymentPlanID {
				if len(node.Config.RuntimeServices) == 0 {
					return nil, "", nil
				}
				if record.RuntimeID == "" {
					return nil, "", ErrRuntimeControlInvalid
				}
				var capability model.HostCapability
				if err := s.db.WithContext(ctx).Where("host_id = ? AND kind = ? AND status = ? AND runtime_id = ?", record.HostID, model.HostCapabilityDocker, model.HostCapabilityReady, record.RuntimeID).First(&capability).Error; err != nil {
					return nil, "", err
				}
				return node.Config.RuntimeServices, capability.RuntimeID, nil
			}
		}
	}
	return nil, "", nil
}

func (s *Service) customServiceOwnership(ctx context.Context, record *model.DeploymentRecord, name string, labels map[string]string) error {
	if id := labels["deployment_id"]; id != "" {
		var owner model.DeploymentRecord
		if err := s.db.WithContext(ctx).Where("uuid = ?", id).First(&owner).Error; err != nil {
			return ErrRuntimeControlInvalid
		}
		if owner.ApplicationID != record.ApplicationID || owner.ProjectID != record.ProjectID {
			return ErrRuntimeControlInvalid
		}
	}
	// 只核对数据库内其他应用已经声明的名称及 Compose 项目，不扫描其他容器。
	var records []model.DeploymentRecord
	latest := s.db.WithContext(ctx).Model(&model.DeploymentRecord{}).Select("MAX(id)").Where("host_id = ? AND status = ?", record.HostID, model.DeploymentSucceeded).Group("application_id, deployment_plan_id, target_id")
	if err := s.db.WithContext(ctx).Where("id IN (?) AND application_id <> ? AND runtime_deleted_at IS NULL", latest, record.ApplicationID).Order("id DESC").Limit(501).Find(&records).Error; err != nil {
		return err
	}
	if len(records) > 500 {
		return ErrRuntimeStateUnavailable
	}
	seen := map[string]bool{}
	for _, other := range records {
		key := other.ApplicationID + ":" + other.DeploymentPlanID + ":" + other.TargetID
		if seen[key] {
			continue
		}
		seen[key] = true
		if other.WorkloadName == name {
			return ErrRuntimeControlInvalid
		}
		var run model.PipelineRun
		if err := s.db.WithContext(ctx).Select("workflow_snapshot").Where("uuid = ?", other.PipelineRunID).First(&run).Error; err != nil {
			return err
		}
		var snapshot struct {
			Stages []model.WorkflowStage `json:"stages"`
		}
		if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
			return err
		}
		for _, stage := range snapshot.Stages {
			for _, node := range stage.Tasks {
				if node.ID != other.WorkflowNodeID {
					continue
				}
				for _, binding := range node.Config.RuntimeServices {
					if binding.Name == name {
						return ErrRuntimeControlInvalid
					}
				}
				if node.Config.Compose != nil && labels["project"] != "" && node.Config.Compose.ProjectName == labels["project"] {
					return ErrRuntimeControlInvalid
				}
			}
		}
	}
	return nil
}
func (s *Service) customRuntimeState(ctx context.Context, record *model.DeploymentRecord) (*RuntimeState, error) {
	bindings, endpoint, err := s.customRuntimeBindings(ctx, record)
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return nil, nil
	}
	if s.docker == nil {
		return nil, ErrRuntimeStateUnavailable
	}
	state := &RuntimeState{DeploymentID: record.ID, Kind: "custom", Name: record.TargetName, RuntimeID: endpoint, State: "running", Running: true}
	for _, binding := range bindings {
		c, labels, err := s.docker.NamedContainerState(ctx, endpoint, binding.Name)
		if err != nil {
			return nil, err
		}
		if err = s.customServiceOwnership(ctx, record, binding.Name, labels); err != nil {
			return nil, err
		}
		ready := 0
		if c.Running && c.Health != "unhealthy" && c.Health != "starting" {
			ready = 1
		} else {
			state.Running = false
			state.State = "stopped"
		}
		state.Services = append(state.Services, dockerengine.ComposeServiceState{Name: binding.Name, State: c.State, Count: 1, ReadyCount: ready, Containers: []dockerengine.ComposeContainerState{c}})
		state.Count++
	}
	return state, nil
}
func (s *Service) customContainerPlan(ctx context.Context, record *model.DeploymentRecord, containerID, action string) (*dockerengine.ComposeContainerControlPlan, error) {
	state, err := s.customRuntimeState(ctx, record)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, ErrRuntimeControlUnsupported
	}
	bindings, _, err := s.customRuntimeBindings(ctx, record)
	if err != nil {
		return nil, err
	}
	for i, service := range state.Services {
		for _, c := range service.Containers {
			if c.ID == containerID {
				payload, _ := json.Marshal([]any{record.ID, state.RuntimeID, containerID, c, action, bindings[i]})
				sum := sha256.Sum256(payload)
				return &dockerengine.ComposeContainerControlPlan{Kind: "custom", Action: action, Service: service.Name, ContainerID: c.ID, Containers: []dockerengine.ComposeContainerState{c}, Digest: hex.EncodeToString(sum[:])}, nil
			}
		}
	}
	return nil, dockerengine.ErrContainerControlChanged
}
func (s *Service) controlCustomContainer(ctx context.Context, record *model.DeploymentRecord, plan *dockerengine.ComposeContainerControlPlan, actorID string, configuration *ConfigurationSet) (dockerengine.RuntimeState, error) {
	action := plan.Action
	if plan.EffectiveAction != "" {
		action = plan.EffectiveAction
	}
	bindings, endpoint, err := s.customRuntimeBindings(ctx, record)
	if err != nil {
		return dockerengine.RuntimeState{}, err
	}
	if action == "stop" {
		if _, err := s.docker.ControlContainer(ctx, endpoint, plan.ContainerID, "stop"); err != nil {
			return dockerengine.RuntimeState{}, err
		}
		state, err := s.customRuntimeState(ctx, record)
		if err != nil {
			return dockerengine.RuntimeState{}, err
		}
		return dockerengine.RuntimeState{Kind: state.Kind, Name: state.Name, RuntimeID: endpoint, State: state.State, Running: state.Running, Count: state.Count, Services: state.Services}, nil
	}
	script := ""
	for _, binding := range bindings {
		if binding.Name == plan.Service {
			script = binding.RestartScript
			if action == "recreate" {
				script = binding.RecreateScript
			}
			break
		}
	}
	if script == "" || s.ssh == nil {
		return dockerengine.RuntimeState{}, ErrLifecycleScriptRequired
	}
	var run model.PipelineRun
	if err = s.db.WithContext(ctx).Where("uuid = ? AND application_id = ? AND project_id = ?", record.PipelineRunID, record.ApplicationID, record.ProjectID).First(&run).Error; err != nil {
		return dockerengine.RuntimeState{}, err
	}
	var snapshot struct {
		Directory      string                `json:"application_config_directory"`
		Stages         []model.WorkflowStage `json:"stages"`
		RegistryInputs map[string][]struct {
			Index    int    `json:"index"`
			ImageRef string `json:"image_ref"`
		} `json:"registry_inputs"`
	}
	if err = json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		return dockerengine.RuntimeState{}, err
	}
	paths := []string{}
	environment := map[string]string{}
	if record.UsesDeliveryVariables {
		environment, err = s.resolveRecordVariablesAsActor(ctx, record, actorID)
		if err != nil {
			return dockerengine.RuntimeState{}, err
		}
	}
	if environment == nil {
		environment = map[string]string{}
	}
	if configuration != nil {
		for _, binding := range configuration.Bindings {
			paths = append(paths, binding.Path)
		}
		for path, body := range configuration.Files {
			environment[configfile.TransportKey(path)] = body
		}
	}
	prefix, err := configfile.WriteScript(snapshot.Directory, paths)
	if err != nil {
		return dockerengine.RuntimeState{}, err
	}
	if record.WorkingDirectory == "" {
		prefix += "cd \"${EDO_APP_CONFIG_PATH:?缺少应用配置目录}\"\n"
	}
	for _, stage := range snapshot.Stages {
		for _, node := range stage.Tasks {
			if node.ID == record.WorkflowNodeID {
				for _, input := range node.Config.DeploymentInputs {
					if input.Kind == "upstream" && input.ArtifactName != "" {
						prefix += "export " + input.ArtifactName + "=\"${EDO_APP_CONFIG_PATH}/" + input.Path + "\"\n"
					}
				}
			}
		}
	}
	script = prefix + script
	environment["EDO_PIPELINE_RUN_ID"] = run.ID
	environment["EDO_GIT_REF"] = run.Ref
	environment["EDO_COMMIT_SHA"] = run.ShortCommitSHA()

	for _, image := range snapshot.RegistryInputs[record.WorkflowNodeID] {
		if image.Index < 0 || image.Index >= 32 || image.ImageRef == "" {
			return dockerengine.RuntimeState{}, ErrRuntimeControlInvalid
		}
		environment[fmt.Sprintf("EDO_INPUT_IMAGE_%d", image.Index+1)] = image.ImageRef
	}
	environment["EDO_APPLICATION_ID"] = record.ApplicationID
	environment["EDO_DEPLOYMENT_ID"] = record.ID
	delete(environment, "EDO_IMAGE_TAG")
	delete(environment, "IMAGE_TAG")
	var images []model.Artifact
	query := s.db.WithContext(ctx).Where("project_id = ? AND application_id = ? AND kind = ? AND status = ?", record.ProjectID, record.ApplicationID, model.ArtifactKindOCIImage, model.ArtifactStatusAvailable)
	if record.ArtifactID != "" {
		query = query.Where("pipeline_run_id = ? OR uuid = ?", run.ID, record.ArtifactID)
	} else {
		query = query.Where("pipeline_run_id = ?", run.ID)
	}
	if err = query.Limit(65).Find(&images).Error; err != nil {
		return dockerengine.RuntimeState{}, err
	}
	tag := ""
	imageRef := ""
	ambiguous := false
	for _, image := range images {
		named, parseErr := reference.ParseNormalizedNamed(image.Name)
		if parseErr != nil {
			continue
		}
		if tagged, ok := named.(reference.Tagged); ok {
			if tag != "" && tag != tagged.Tag() {
				ambiguous = true
			}
			tag = tagged.Tag()
		}
		if imageRef == "" {
			imageRef = image.ImageRef
		} else if imageRef != image.ImageRef {
			imageRef = ""
		}
	}
	if !ambiguous && len(images) <= 64 && tag != "" {
		environment["EDO_IMAGE_TAG"] = tag
		environment["IMAGE_TAG"] = tag
	}
	if len(images) == 1 {
		environment["EDO_IMAGE"] = imageRef
	}
	var app model.Application
	if err = s.db.WithContext(ctx).Select("name").Where("uuid = ?", record.ApplicationID).First(&app).Error; err != nil {
		return dockerengine.RuntimeState{}, err
	}
	environment["EDO_APPLICATION_NAME"] = app.Name

	latest, err := s.customContainerPlan(ctx, record, plan.ContainerID, action)
	if err != nil || latest.Digest != func() string {
		if plan.BaseDigest != "" {
			return plan.BaseDigest
		}
		return plan.Digest
	}() {
		return dockerengine.RuntimeState{}, dockerengine.ErrContainerControlChanged
	}
	timeout := time.Duration(record.CommandTimeout) * time.Second
	if timeout < 30*time.Second || timeout > time.Hour {
		timeout = 5 * time.Minute
	}
	if err = s.requireContainerControl(ctx, record, actorID, action); err != nil {
		return dockerengine.RuntimeState{}, err
	}
	result, err := s.ssh.RunHostDeploymentScript(ctx, sshdeploy.Input{HostID: record.HostID, EnvironmentID: record.EnvironmentID, WorkingDirectory: record.WorkingDirectory, Script: script, Environment: environment, Timeout: timeout, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.ExitCode != 0 {
		return dockerengine.RuntimeState{}, errors.Join(fmt.Errorf("自定义服务命令执行失败 exit_code=%d", result.ExitCode), err)
	}
	var state *RuntimeState
	for {
		state, err = s.customRuntimeState(ctx, record)
		if err != nil {
			return dockerengine.RuntimeState{}, err
		}
		var current *dockerengine.ComposeContainerState
		for _, service := range state.Services {
			if service.Name == plan.Service && len(service.Containers) == 1 {
				value := service.Containers[0]
				current = &value
			}
		}
		if current != nil && current.Running {
			changed := current.ID != plan.ContainerID
			if action == "restart" && len(plan.Containers) == 1 {
				before := plan.Containers[0]
				changed = changed || current.RestartCount != before.RestartCount || current.StartedAt != nil && before.StartedAt != nil && !current.StartedAt.Equal(*before.StartedAt)
			}
			if changed {
				break
			}
		}
		select {
		case <-ctx.Done():
			return dockerengine.RuntimeState{}, ErrRuntimeControlFailed
		case <-time.After(200 * time.Millisecond):
		}
	}

	return dockerengine.RuntimeState{Kind: state.Kind, Name: state.Name, RuntimeID: endpoint, State: state.State, Running: state.Running, Count: state.Count, Services: state.Services}, nil
}
