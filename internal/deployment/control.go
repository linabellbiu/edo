package deployment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/dockerengine"
	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/sshdeploy"
)

var (
	ErrRuntimeControlInvalid     = errors.New("运行控制参数无效")
	ErrRuntimeControlUnsupported = errors.New("当前部署方式不支持此操作")
	ErrDeploymentNotCurrent      = errors.New("只能操作应用当前最新的部署实例")
	ErrRuntimeInstanceRemoved    = errors.New("容器实例已删除，请等待下一次部署成功")
	ErrLifecycleScriptRequired   = errors.New("请先在该 Shell 部署实例中保存对应的停止或重启命令")
	ErrRuntimeControlFailed      = errors.New("运行资源操作失败，请检查当前状态后重试")
	ErrRuntimeRemovalFailed      = errors.New("删除容器实例失败，请检查当前状态后重试")
	ErrRuntimeStateUnavailable   = errors.New("无法读取当前运行资源状态")
)

type RuntimeControlInput struct {
	Action   string
	Replicas *int32
}

type RuntimeState struct {
	DeploymentID      string `json:"deployment_id"`
	Kind              string `json:"kind"`
	ResourceID        string `json:"resource_id,omitempty"`
	Name              string `json:"name"`
	Namespace         string `json:"namespace,omitempty"`
	State             string `json:"state"`
	Running           bool   `json:"running"`
	Count             int    `json:"count,omitempty"`
	Replicas          int32  `json:"replicas,omitempty"`
	ReadyReplicas     int32  `json:"ready_replicas,omitempty"`
	AvailableReplicas int32  `json:"available_replicas,omitempty"`
	RestartConfigured bool   `json:"restart_configured,omitempty"`
	StopConfigured    bool   `json:"stop_configured,omitempty"`
	Output            string `json:"output,omitempty"`
}

type InstanceControlConfigurationInput struct {
	RestartScript  string
	StopScript     string
	TimeoutSeconds int
}

type InstanceControlConfiguration struct {
	DeploymentID   string `json:"deployment_id"`
	RestartScript  string `json:"restart_script"`
	StopScript     string `json:"stop_script"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (s *Service) RuntimeConfiguration(ctx context.Context, deploymentID string) (*InstanceControlConfiguration, error) {
	record, err := s.currentDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	if !isShellRuntime(record) {
		return nil, ErrRuntimeControlUnsupported
	}
	configuration, err := s.findInstanceControl(ctx, record)
	if err != nil {
		s.logger.Error("读取 Shell 部署实例运行命令失败", "operation", "deployment_runtime_configuration_read",
			"deployment_id", record.ID, "application_id", record.ApplicationID, "deployment_plan_id", record.DeploymentPlanID,
			"target_id", record.TargetID, "err", err)
		return nil, ErrRuntimeControlFailed
	}
	return instanceControlResponse(record.ID, configuration), nil
}

func (s *Service) SaveRuntimeConfiguration(
	ctx context.Context,
	deploymentID, actorID string,
	input InstanceControlConfigurationInput,
) (*InstanceControlConfiguration, error) {
	input.RestartScript = strings.TrimSpace(input.RestartScript)
	input.StopScript = strings.TrimSpace(input.StopScript)
	if strings.TrimSpace(actorID) == "" || len(input.RestartScript) > 256*1024 || len(input.StopScript) > 256*1024 ||
		input.TimeoutSeconds < 30 || input.TimeoutSeconds > 3600 {
		return nil, ErrRuntimeControlInvalid
	}
	record, err := s.currentDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	if !isShellRuntime(record) {
		return nil, ErrRuntimeControlUnsupported
	}
	now := time.Now().UTC()
	configuration, err := s.findInstanceControl(ctx, record)
	if err != nil {
		s.logger.Error("读取待更新的 Shell 部署实例运行命令失败", "operation", "deployment_runtime_configuration_load",
			"deployment_id", record.ID, "application_id", record.ApplicationID, "deployment_plan_id", record.DeploymentPlanID,
			"target_id", record.TargetID, "err", err)
		return nil, ErrRuntimeControlFailed
	}
	configuration.RestartScript = input.RestartScript
	configuration.StopScript = input.StopScript
	configuration.TimeoutSeconds = input.TimeoutSeconds
	configuration.UpdatedBy = actorID
	configuration.UpdatedAt = now
	if configuration.ID == "" {
		configuration.ID = uuid.NewString()
		configuration.ApplicationID = record.ApplicationID
		configuration.DeploymentPlanID = record.DeploymentPlanID
		configuration.TargetID = record.TargetID
		configuration.CreatedAt = now
		if err := s.db.WithContext(ctx).Create(&configuration).Error; err != nil {
			s.logger.Error("创建 Shell 部署实例运行命令失败", "operation", "deployment_runtime_configuration_create",
				"deployment_id", record.ID, "application_id", record.ApplicationID, "deployment_plan_id", record.DeploymentPlanID,
				"target_id", record.TargetID, "err", err)
			return nil, ErrRuntimeControlFailed
		}
	} else if err := s.db.WithContext(ctx).Model(&model.DeploymentInstanceControl{}).
		Where("id = ?", configuration.ID).
		Updates(map[string]any{
			"restart_script": input.RestartScript, "stop_script": input.StopScript,
			"timeout_seconds": input.TimeoutSeconds, "updated_by": actorID, "updated_at": now,
		}).Error; err != nil {
		s.logger.Error("更新 Shell 部署实例运行命令失败", "operation", "deployment_runtime_configuration_update",
			"deployment_id", record.ID, "control_id", configuration.ID, "err", err)
		return nil, ErrRuntimeControlFailed
	}
	s.logger.Info("Shell 部署实例运行命令已更新", "operation", "deployment_runtime_configuration_update",
		"deployment_id", record.ID, "application_id", record.ApplicationID, "deployment_plan_id", record.DeploymentPlanID,
		"target_id", record.TargetID, "actor_id", actorID)
	return instanceControlResponse(record.ID, configuration), nil
}

func (s *Service) RuntimeState(ctx context.Context, deploymentID string) (*RuntimeState, error) {
	record, err := s.currentDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	state, err := s.readRuntimeState(ctx, record)
	if err != nil {
		s.logger.Error("读取应用部署实例运行状态失败", "operation", "deployment_runtime_state",
			"deployment_id", record.ID, "application_id", record.ApplicationID, "target_id", record.TargetID,
			"platform", record.Platform, "err", err)
		return nil, ErrRuntimeStateUnavailable
	}
	return state, nil
}

func (s *Service) ControlRuntime(
	ctx context.Context,
	deploymentID, actorID string,
	input RuntimeControlInput,
) (*RuntimeState, error) {
	input.Action = strings.TrimSpace(input.Action)
	if strings.TrimSpace(actorID) == "" || (input.Action != "restart" && input.Action != "stop" && input.Action != "scale") {
		return nil, ErrRuntimeControlInvalid
	}
	if input.Action == "scale" && (input.Replicas == nil || *input.Replicas < 0 || *input.Replicas > 1000) {
		return nil, ErrRuntimeControlInvalid
	}
	record, err := s.currentDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	if input.Action == "scale" && record.Platform != model.DeploymentKubernetes {
		return nil, ErrRuntimeControlUnsupported
	}
	lockRecord := *record
	if isShellRuntime(record) {
		configuration, loadErr := s.findInstanceControl(ctx, record)
		if loadErr != nil {
			s.logger.Error("读取 Shell 运行控制锁超时配置失败", "operation", "deployment_runtime_control_lock_config",
				"deployment_id", record.ID, "application_id", record.ApplicationID, "action", input.Action, "err", loadErr)
			return nil, ErrRuntimeControlFailed
		}
		lockSeconds := configuration.TimeoutSeconds
		if lockSeconds < 30 || lockSeconds > 3600 {
			lockSeconds = 300
		}
		lockRecord.CommandTimeout, lockRecord.RolloutTimeout = lockSeconds, lockSeconds
	}
	lock, err := s.acquireTargetLock(ctx, &lockRecord)
	if err != nil {
		s.logger.Error("等待部署实例运行控制锁失败", "operation", "deployment_runtime_control_lock",
			"deployment_id", record.ID, "target_id", record.TargetID, "action", input.Action, "err", err)
		return nil, ErrRuntimeControlFailed
	}
	if lock != nil {
		defer func() {
			releaseContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if releaseErr := lock.Release(releaseContext); releaseErr != nil {
				s.logger.Error("释放部署实例运行控制锁失败", "operation", "deployment_runtime_control_unlock",
					"deployment_id", record.ID, "target_id", record.TargetID, "action", input.Action, "err", releaseErr)
			}
		}()
	}

	state, err := s.executeRuntimeControl(ctx, record, actorID, input)
	if err != nil {
		if errors.Is(err, ErrLifecycleScriptRequired) || errors.Is(err, ErrRuntimeControlUnsupported) || errors.Is(err, ErrRuntimeControlInvalid) {
			return nil, err
		}
		s.logger.Error("执行应用部署实例运行控制失败", "operation", "deployment_runtime_control",
			"deployment_id", record.ID, "application_id", record.ApplicationID, "target_id", record.TargetID,
			"platform", record.Platform, "plan_kind", record.DeploymentPlanKind, "action", input.Action, "err", err)
		return nil, ErrRuntimeControlFailed
	}
	s.logger.Info("应用部署实例运行控制完成", "operation", "deployment_runtime_control",
		"deployment_id", record.ID, "application_id", record.ApplicationID, "target_id", record.TargetID,
		"platform", record.Platform, "plan_kind", record.DeploymentPlanKind, "action", input.Action, "actor_id", actorID)
	return state, nil
}

// RemoveRuntime 删除当前最新成功部署对应的 Docker 单容器，并将这条历史记录退出运行监控。
// 历史发布记录继续保留；后续成功部署会生成新的记录并自动恢复监控。
func (s *Service) RemoveRuntime(ctx context.Context, deploymentID, actorID string) error {
	if strings.TrimSpace(actorID) == "" {
		return ErrRuntimeControlInvalid
	}
	record, err := s.currentDeployment(ctx, deploymentID)
	if err != nil {
		return err
	}
	if record.Platform != model.DeploymentDocker || record.DeploymentPlanKind != model.DeploymentPlanDocker || s.docker == nil {
		return ErrRuntimeControlUnsupported
	}
	lock, err := s.acquireTargetLock(ctx, record)
	if err != nil {
		s.logger.Error("等待删除容器实例锁失败", "operation", "deployment_runtime_remove_lock",
			"deployment_id", record.ID, "target_id", record.TargetID, "err", err)
		return ErrRuntimeRemovalFailed
	}
	if lock != nil {
		defer func() {
			releaseContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if releaseErr := lock.Release(releaseContext); releaseErr != nil {
				s.logger.Error("释放删除容器实例锁失败", "operation", "deployment_runtime_remove_unlock",
					"deployment_id", record.ID, "target_id", record.TargetID, "err", releaseErr)
			}
		}()
	}
	// 等锁期间可能已有新部署完成，删除前再次确认，避免移除刚上线的新容器。
	record, err = s.currentDeployment(ctx, deploymentID)
	if err != nil {
		return err
	}
	timeout := time.Duration(record.RolloutTimeout) * time.Second
	if timeout < 30*time.Second || timeout > time.Hour {
		timeout = 5 * time.Minute
	}
	removeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := s.docker.RemoveContainer(removeContext, record.RuntimeID, record.WorkloadName); err != nil {
		s.logger.Error("删除应用 Docker 容器实例失败", "operation", "deployment_runtime_remove",
			"deployment_id", record.ID, "application_id", record.ApplicationID, "target_id", record.TargetID,
			"runtime_id", record.RuntimeID, "container_name", record.WorkloadName, "err", err)
		return ErrRuntimeRemovalFailed
	}
	deletedAt := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&model.DeploymentRecord{}).
		Where("id = ? AND runtime_deleted_at IS NULL", record.ID).
		Update("runtime_deleted_at", deletedAt)
	if result.Error != nil || result.RowsAffected != 1 {
		s.logger.Error("保存容器实例退出监控状态失败", "operation", "deployment_runtime_remove_persist",
			"deployment_id", record.ID, "rows_affected", result.RowsAffected, "err", result.Error)
		return ErrRuntimeRemovalFailed
	}
	s.logger.Info("应用 Docker 容器实例已删除并退出监控", "operation", "deployment_runtime_remove",
		"deployment_id", record.ID, "application_id", record.ApplicationID, "target_id", record.TargetID,
		"runtime_id", record.RuntimeID, "container_name", record.WorkloadName, "actor_id", actorID)
	return nil
}

func (s *Service) executeRuntimeControl(
	ctx context.Context,
	record *model.DeploymentRecord,
	actorID string,
	input RuntimeControlInput,
) (*RuntimeState, error) {
	if isShellRuntime(record) {
		return s.executeLifecycleScript(ctx, record, actorID, input.Action)
	}
	timeout := time.Duration(record.RolloutTimeout) * time.Second
	if timeout < 30*time.Second || timeout > time.Hour {
		timeout = 5 * time.Minute
	}
	controlContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch {
	case record.Platform == model.DeploymentDocker && record.DeploymentPlanKind == model.DeploymentPlanCompose:
		if s.docker == nil {
			return nil, ErrRuntimeControlUnsupported
		}
		result, err := s.docker.ControlCompose(controlContext, record.RuntimeID, record.TargetID, record.ComposeService, input.Action)
		return dockerRuntimeState(record.ID, result), err
	case record.Platform == model.DeploymentDocker:
		if s.docker == nil {
			return nil, ErrRuntimeControlUnsupported
		}
		result, err := s.docker.ControlContainer(controlContext, record.RuntimeID, record.WorkloadName, input.Action)
		return dockerRuntimeState(record.ID, result), err
	case record.Platform == model.DeploymentKubernetes:
		if s.kube == nil {
			return nil, ErrRuntimeControlUnsupported
		}
		replicas := int32(0)
		if input.Replicas != nil {
			replicas = *input.Replicas
		}
		result, err := s.kube.ControlDeployment(
			controlContext, record.RuntimeID, record.Namespace, record.WorkloadName, input.Action, replicas, timeout,
		)
		return kubernetesRuntimeState(record.ID, result), err
	default:
		return nil, ErrRuntimeControlUnsupported
	}
}

func (s *Service) readRuntimeState(ctx context.Context, record *model.DeploymentRecord) (*RuntimeState, error) {
	switch {
	case record.Platform == model.DeploymentDocker && record.DeploymentPlanKind == model.DeploymentPlanCompose:
		if s.docker == nil {
			return nil, ErrRuntimeControlUnsupported
		}
		result, err := s.docker.ComposeRuntimeState(ctx, record.RuntimeID, record.TargetID, record.ComposeService)
		return dockerRuntimeState(record.ID, result), err
	case record.Platform == model.DeploymentDocker:
		if s.docker == nil {
			return nil, ErrRuntimeControlUnsupported
		}
		result, err := s.docker.ContainerRuntimeState(ctx, record.RuntimeID, record.WorkloadName)
		return dockerRuntimeState(record.ID, result), err
	case record.Platform == model.DeploymentKubernetes:
		if s.kube == nil {
			return nil, ErrRuntimeControlUnsupported
		}
		result, err := s.kube.DeploymentRuntimeState(ctx, record.RuntimeID, record.Namespace, record.WorkloadName)
		return kubernetesRuntimeState(record.ID, result), err
	case isShellRuntime(record):
		configuration, err := s.findInstanceControl(ctx, record)
		if err != nil {
			return nil, err
		}
		return &RuntimeState{
			DeploymentID: record.ID, Kind: "script", Name: record.TargetName, State: "unverified",
			RestartConfigured: strings.TrimSpace(configuration.RestartScript) != "",
			StopConfigured:    strings.TrimSpace(configuration.StopScript) != "",
		}, nil
	default:
		return nil, ErrRuntimeControlUnsupported
	}
}

func (s *Service) executeLifecycleScript(
	ctx context.Context,
	record *model.DeploymentRecord,
	actorID, action string,
) (*RuntimeState, error) {
	if s.ssh == nil || record.ApplicationID == "" || (action != "restart" && action != "stop") {
		return nil, ErrRuntimeControlUnsupported
	}
	configuration, err := s.findInstanceControl(ctx, record)
	if err != nil {
		return nil, fmt.Errorf("读取部署实例生命周期命令失败: %w", err)
	}
	script := configuration.RestartScript
	if action == "stop" {
		script = configuration.StopScript
	}
	if strings.TrimSpace(script) == "" {
		return nil, ErrLifecycleScriptRequired
	}
	timeout := time.Duration(configuration.TimeoutSeconds) * time.Second
	if timeout < 30*time.Second || timeout > time.Hour {
		timeout = 5 * time.Minute
	}
	output := &limitedControlOutput{maximum: 64 * 1024}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	applicationName := ""
	var application model.Application
	if err := s.db.WithContext(ctx).Select("name").First(&application, "id = ?", record.ApplicationID).Error; err == nil {
		applicationName = application.Name
	}
	result, err := s.ssh.RunHostDeploymentScript(commandContext, sshdeploy.Input{
		HostID: record.HostID, EnvironmentID: record.EnvironmentID, WorkingDirectory: record.WorkingDirectory,
		Script: script, Timeout: timeout,
		Environment: map[string]string{
			"EDO_APPLICATION_ID": record.ApplicationID, "EDO_APPLICATION_NAME": applicationName,
			"EDO_DEPLOYMENT_ID": record.ID, "EDO_LIFECYCLE_ACTION": action, "EDO_ACTOR_ID": actorID,
		},
		Stdout: output, Stderr: output,
	})
	if err != nil {
		return nil, fmt.Errorf("生命周期命令执行失败 exit_code=%d: %w", result.ExitCode, err)
	}
	return &RuntimeState{
		DeploymentID: record.ID, Kind: "script", Name: record.TargetName,
		State: action + "_command_completed", Output: output.String(),
	}, nil
}

func (s *Service) findInstanceControl(ctx context.Context, record *model.DeploymentRecord) (model.DeploymentInstanceControl, error) {
	configuration := model.DeploymentInstanceControl{TimeoutSeconds: 300}
	err := s.db.WithContext(ctx).Where(
		"application_id = ? AND deployment_plan_id = ? AND target_id = ?",
		record.ApplicationID, record.DeploymentPlanID, record.TargetID,
	).First(&configuration).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return configuration, nil
	}
	return configuration, err
}

func instanceControlResponse(deploymentID string, configuration model.DeploymentInstanceControl) *InstanceControlConfiguration {
	timeout := configuration.TimeoutSeconds
	if timeout < 30 || timeout > 3600 {
		timeout = 300
	}
	return &InstanceControlConfiguration{
		DeploymentID: deploymentID, RestartScript: configuration.RestartScript,
		StopScript: configuration.StopScript, TimeoutSeconds: timeout,
	}
}

func isShellRuntime(record *model.DeploymentRecord) bool {
	return record.Platform == model.DeploymentSSH && record.DeploymentPlanKind == model.DeploymentPlanScript
}

func (s *Service) currentDeployment(ctx context.Context, deploymentID string) (*model.DeploymentRecord, error) {
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" || len(deploymentID) > 36 {
		return nil, ErrDeploymentNotFound
	}
	var record model.DeploymentRecord
	if err := s.db.WithContext(ctx).First(&record, "id = ?", deploymentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDeploymentNotFound
		}
		return nil, fmt.Errorf("读取部署实例失败: %w", err)
	}
	if record.RuntimeDeletedAt != nil {
		return nil, ErrRuntimeInstanceRemoved
	}
	if record.Status != model.DeploymentSucceeded || record.PipelineRunID == "" || record.DeploymentPlanID == "" {
		return nil, ErrDeploymentNotCurrent
	}
	if record.ApplicationID == "" {
		var run model.PipelineRun
		if err := s.db.WithContext(ctx).Select("application_id").First(&run, "id = ?", record.PipelineRunID).Error; err != nil || run.ApplicationID == "" {
			return nil, ErrDeploymentNotCurrent
		}
		record.ApplicationID = run.ApplicationID
	}
	var latest model.DeploymentRecord
	query := s.db.WithContext(ctx).Model(&model.DeploymentRecord{}).
		Where("application_id = ? AND deployment_plan_id = ? AND target_id = ? AND status = ?",
			record.ApplicationID, record.DeploymentPlanID, record.TargetID, model.DeploymentSucceeded).
		Order("created_at DESC").Limit(1).Find(&latest)
	if query.Error != nil {
		return nil, fmt.Errorf("确认当前部署实例失败: %w", query.Error)
	}
	if latest.ID == "" {
		// 升级前的记录可能尚未持久化 application_id，使用流水线运行的稳定外键关联，
		// 但绝不能回退为应用名称匹配。
		query = s.db.WithContext(ctx).Table("deployment_records AS deployment").
			Select("deployment.*").
			Joins("JOIN pipeline_runs AS run ON run.id = deployment.pipeline_run_id").
			Where("deployment.department_id = ? AND run.department_id = ? AND run.application_id = ? AND deployment.deployment_plan_id = ? AND deployment.target_id = ? AND deployment.status = ?",
				record.DepartmentID, record.DepartmentID, record.ApplicationID, record.DeploymentPlanID, record.TargetID, model.DeploymentSucceeded).
			Order("deployment.created_at DESC").Limit(1).Scan(&latest)
		if query.Error != nil {
			return nil, fmt.Errorf("确认历史部署实例关联失败: %w", query.Error)
		}
	}
	if latest.ID != record.ID {
		return nil, ErrDeploymentNotCurrent
	}
	return &record, nil
}

func dockerRuntimeState(deploymentID string, input dockerengine.RuntimeState) *RuntimeState {
	return &RuntimeState{
		DeploymentID: deploymentID, Kind: input.Kind, ResourceID: input.ResourceID,
		Name: input.Name, State: input.State, Running: input.Running, Count: input.Count,
	}
}

func kubernetesRuntimeState(deploymentID string, input kube.RuntimeState) *RuntimeState {
	return &RuntimeState{
		DeploymentID: deploymentID, Kind: input.Kind, Name: input.Name, Namespace: input.Namespace,
		State: input.State, Running: input.Running, Replicas: input.Replicas,
		ReadyReplicas: input.ReadyReplicas, AvailableReplicas: input.AvailableReplicas,
	}
}

type limitedControlOutput struct {
	mu      sync.Mutex
	value   strings.Builder
	maximum int
}

func (w *limitedControlOutput) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.maximum - w.value.Len()
	if remaining > 0 {
		_, _ = w.value.Write(payload[:min(len(payload), remaining)])
	}
	return len(payload), nil
}

func (w *limitedControlOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(w.value.String())
}
