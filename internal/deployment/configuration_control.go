package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"

	"edo/internal/configfile"
	"edo/internal/dockerengine"
	"edo/internal/model"
)

func (s *Service) containerControlPlan(ctx context.Context, record *model.DeploymentRecord, containerID, actorID, action string) (*dockerengine.ComposeContainerControlPlan, *ConfigurationSet, error) {
	var plan *dockerengine.ComposeContainerControlPlan
	var err error
	single := record.Platform == model.DeploymentDocker && record.DeploymentPlanKind == model.DeploymentPlanCompose
	native := record.Platform == model.DeploymentDocker && record.DeploymentPlanKind == model.DeploymentPlanDocker
	var endpoint string
	var scope dockerengine.ComposeProjectScope
	makePlan := func(effective string) (*dockerengine.ComposeContainerControlPlan, error) {
		if single {
			return s.docker.SingleComposeContainerControlPlan(ctx, record.RuntimeID, record.TargetID, record.ComposeService, containerID, effective)
		}
		if native {
			return s.docker.NativeContainerControlPlan(ctx, record.RuntimeID, record.TargetID, record.ID, record.WorkloadName, containerID, effective)
		}
		return s.docker.ComposeContainerControlPlan(ctx, endpoint, scope, containerID, effective)
	}
	if s.docker == nil {
		return nil, nil, ErrRuntimeControlUnsupported
	}
	if !native && !single {
		endpoint, scope, err = s.composeRuntimeScope(ctx, record)
		if err != nil {
			return nil, nil, err
		}
	}
	plan, err = makePlan(action)
	if err != nil {
		return nil, nil, err
	}
	var set *ConfigurationSet
	hasConfiguration := false
	if action != "stop" && record.UsesDeliveryVariables {
		hasConfiguration, err = s.recordUsesConfiguration(ctx, record)
		if err != nil {
			return nil, nil, err
		}
	}
	if hasConfiguration {
		if err = s.requireCurrentActorPermission(ctx, record, actorID, "delivery_variable.execute"); err != nil {
			return nil, nil, err
		}
		set, err = s.ResolveDeploymentConfiguration(ctx, record, true)
		if err != nil {
			return nil, nil, err
		}
	}
	if set != nil {
		desired := configfile.RevisionDigest(set.Bindings)
		changed := false
		for _, c := range plan.Containers {
			changed = changed || c.ConfigurationRevision != desired
		}
		if action == "restart" && changed {
			if err = s.requireCurrentActorPermission(ctx, record, actorID, ContainerControlPermission("recreate")); err != nil {
				return nil, nil, err
			}
			plan, err = makePlan("recreate")
			if err != nil {
				return nil, nil, err
			}
			plan.Action = action
			plan.EffectiveAction = "recreate"
		}
		plan.ConfigurationChanged = changed
		plan.ConfigurationBindings = set.Bindings
		for _, binding := range set.Bindings {
			plan.ConfigurationFiles = append(plan.ConfigurationFiles, dockerengine.ConfigurationFileRevision{Path: binding.Path, Version: binding.Version})
		}
		plan.BaseDigest = plan.Digest
		payload, _ := json.Marshal([]any{plan.Digest, action, plan.EffectiveAction, set.Bindings})
		sum := sha256.Sum256(payload)
		plan.Digest = hex.EncodeToString(sum[:])
	}
	effective := plan.Action
	if plan.EffectiveAction != "" {
		effective = plan.EffectiveAction
	}
	if effective == "recreate" && !native && !single {
		if _, err = frozenComposeContent(record); err != nil {
			return nil, nil, err
		}
	}
	return plan, set, nil
}

func (s *Service) authorizeConfigurationControl(ctx context.Context, record *model.DeploymentRecord, actorID, action string, set *ConfigurationSet) error {
	if err := s.requireContainerControl(ctx, record, actorID, action); err != nil {
		return err
	}
	if set == nil {
		return nil
	}
	if err := s.requireCurrentActorPermission(ctx, record, actorID, "delivery_variable.execute"); err != nil {
		return err
	}
	if action == "recreate" {
		if err := s.requireCurrentActorPermission(ctx, record, actorID, ContainerControlPermission("recreate")); err != nil {
			return err
		}
	}
	// 确认以后保存新正文、撤销或改变范围，都要求重新检查。
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "uuid = ? AND project_id = ?", record.PipelineRunID, record.ProjectID).Error; err != nil {
		return s.configurationError(record, "configuration_control_run", err)
	}
	latest, err := s.variables.service.LatestConfigFiles(ctx, record.ProjectID, record.ApplicationID, record.EnvironmentID, run.Ref, set.Bindings)
	if err != nil {
		return s.configurationError(record, "configuration_control_current", err)
	}
	if configfile.RevisionDigest(latest) != configfile.RevisionDigest(set.Bindings) {
		s.logger.Warn("确认后配置版本已变化", "operation", "configuration_control_version", "deployment_id", record.ID, "err", dockerengine.ErrContainerControlChanged)
		return dockerengine.ErrContainerControlChanged
	}
	return nil
}

func (s *Service) recreateNativeContainer(ctx context.Context, record *model.DeploymentRecord, plan *dockerengine.ComposeContainerControlPlan, set *ConfigurationSet, actorID, action string, timeout time.Duration) (dockerengine.RuntimeState, error) {
	configuration, err := dockerengine.NormalizeContainerConfig(record.DockerConfig)
	if err != nil || record.DockerConfigDigest != model.DockerContainerConfigDigest(configuration) || plan.DockerIdentity == nil {
		return dockerengine.RuntimeState{}, ErrContainerRecreateBaseline
	}
	values := map[string]string{}
	if record.UsesDeliveryVariables {
		values, err = s.resolveRecordVariablesAsActor(ctx, record, actorID)
		if err != nil {
			return dockerengine.RuntimeState{}, err
		}
	}
	if values == nil {
		values = map[string]string{}
	}
	if set != nil {
		for key, value := range set.Values {
			values[key] = value
		}
		ctx = dockerengine.WithConfigurationRevision(ctx, configfile.RevisionDigest(set.Bindings))
	}
	if configuration.EnvironmentVariables == nil {
		configuration.EnvironmentVariables = map[string]string{}
	}
	for key, value := range values {
		configuration.EnvironmentVariables[key] = value
	}
	if err = s.requireContainerControl(ctx, record, actorID, plan.Action); err != nil {
		return dockerengine.RuntimeState{}, err
	}
	if err = s.authorizeConfigurationControl(ctx, record, actorID, action, set); err != nil {
		return dockerengine.RuntimeState{}, err
	}
	if set != nil {
		if err = s.docker.WriteConfigurationFiles(ctx, record.RuntimeID, set.Directory, set.Files); err != nil {
			return dockerengine.RuntimeState{}, err
		}
	}
	identity := *plan.DockerIdentity
	_, warning, err := s.docker.DeployPreparedContainerIfCurrent(ctx, record.RuntimeID, record.TargetID, record.WorkloadName, identity.ImageID, record.ImageDisplay, identity.ImageID, record.ID, timeout, configuration, identity, dockerengine.RegistryAuth{}, io.Discard, io.Discard)
	if warning != nil {
		s.logger.Warn("容器重建完成但旧实例清理有提示", "operation", "native_container_recreate", "deployment_id", record.ID, "err", warning)
	}
	if err != nil {
		return dockerengine.RuntimeState{}, err
	}
	return s.docker.ContainerRuntimeState(ctx, record.RuntimeID, record.WorkloadName)
}

func (s *Service) recreateSingleCompose(ctx context.Context, record *model.DeploymentRecord, plan *dockerengine.ComposeContainerControlPlan, set *ConfigurationSet, actorID string, timeout time.Duration) (dockerengine.RuntimeState, error) {
	if record.ComposeDigest != model.DeploymentPlanComposeExecutionDigest(record.ComposeYAML, record.ComposeService, record.ComposeTimeout) {
		return dockerengine.RuntimeState{}, ErrContainerRecreateBaseline
	}
	values, files := map[string]string{}, map[string]string{}
	var err error
	if record.UsesDeliveryVariables {
		values, files, err = s.resolveRecordVariableSetForActors(ctx, record, []string{actorID})
		if err != nil {
			return dockerengine.RuntimeState{}, err
		}
	}
	if values == nil {
		values = map[string]string{}
	}
	if set != nil {
		for key, value := range set.Values {
			values[key] = value
		}
		ctx = dockerengine.WithConfigurationRevision(ctx, configfile.RevisionDigest(set.Bindings))
	}
	authorize := func() error {
		if err := s.requireContainerControl(ctx, record, actorID, plan.Action); err != nil {
			return err
		}
		return s.authorizeConfigurationControl(ctx, record, actorID, "recreate", set)
	}
	if err = authorize(); err != nil {
		return dockerengine.RuntimeState{}, err
	}
	if set != nil {
		if err = s.docker.WriteConfigurationFiles(ctx, record.RuntimeID, set.Directory, set.Files); err != nil {
			return dockerengine.RuntimeState{}, err
		}
	}
	return s.docker.RecreateSingleCompose(ctx, dockerengine.ComposeDeployInput{EndpointID: record.RuntimeID, TargetID: record.TargetID, DeploymentID: record.ID, ServiceName: record.ComposeService, YAML: record.ComposeYAML, Timeout: timeout, Environment: values, EnvFiles: files}, plan, authorize)
}
