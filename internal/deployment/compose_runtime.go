package deployment

import (
	"context"
	"encoding/json"
	"path"

	"go.yaml.in/yaml/v3"

	"edo/internal/composeconfig"
	"edo/internal/configfile"
	"edo/internal/dockerengine"
	"edo/internal/model"
)

// 只读取冻结快照中的身份及 Compose 范围，不从现在可编辑的流水线或方案推断运行对象。
func (s *Service) composeRuntimeScope(ctx context.Context, record *model.DeploymentRecord) (string, dockerengine.ComposeProjectScope, error) {
	invalid := ErrRuntimeControlInvalid
	if s.docker == nil || record.Platform != model.DeploymentSSH || record.DeploymentPlanKind != model.DeploymentPlanComposeStack || record.HostID == "" || !path.IsAbs(record.WorkingDirectory) {
		return "", dockerengine.ComposeProjectScope{}, invalid
	}
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).Where("uuid = ? AND application_id = ? AND project_id = ? AND department_id = ?", record.PipelineRunID, record.ApplicationID, record.ProjectID, record.DepartmentID).First(&run).Error; err != nil {
		return "", dockerengine.ComposeProjectScope{}, err
	}
	var snapshot struct {
		ApplicationConfigDirectory string                `json:"application_config_directory"`
		SchemaVersion              uint16                `json:"schema_version"`
		Stages                     []model.WorkflowStage `json:"stages"`
		DeploymentPlans            map[string]struct {
			ID          string                   `json:"id"`
			Kind        model.DeploymentPlanKind `json:"kind"`
			ComposeYAML string                   `json:"compose_yaml"`
		} `json:"deployment_plans"`
		DeploymentTargets map[string]struct {
			ID               string                   `json:"id"`
			ProjectID        string                   `json:"project_id"`
			DepartmentID     string                   `json:"department_id"`
			HostID           string                   `json:"host_id"`
			EnvironmentID    string                   `json:"environment_id"`
			WorkingDirectory string                   `json:"working_directory"`
			Platform         model.DeploymentPlatform `json:"platform"`
		} `json:"deployment_targets"`
	}
	if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		return "", dockerengine.ComposeProjectScope{}, invalid
	}
	plan, target := snapshot.DeploymentPlans[record.WorkflowNodeID], snapshot.DeploymentTargets[record.WorkflowNodeID]
	if snapshot.SchemaVersion != model.WorkflowSchemaVersion || plan.ID != record.DeploymentPlanID || plan.Kind != record.DeploymentPlanKind || target.ID != record.TargetID || target.ProjectID != record.ProjectID || target.DepartmentID != record.DepartmentID || target.Platform != record.Platform || target.HostID != record.HostID || target.EnvironmentID != record.EnvironmentID || path.Clean(target.WorkingDirectory) != path.Clean(record.WorkingDirectory) {
		return "", dockerengine.ComposeProjectScope{}, invalid
	}
	var settings *model.ComposeDeploymentSettings
	var configuration model.WorkflowNodeConfig
	found := false
	for _, stage := range snapshot.Stages {
		for _, node := range stage.Tasks {
			if node.ID == record.WorkflowNodeID {
				if found || node.Type != model.WorkflowNodeDeploy || node.Config.DeploymentPlanID != record.DeploymentPlanID {
					return "", dockerengine.ComposeProjectScope{}, invalid
				}
				found = true
				settings = node.Config.Compose
				configuration = node.Config
			}
		}
	}
	if settings == nil || !composeconfig.ValidProjectName(settings.ProjectName) || len(plan.ComposeYAML) == 0 || len(plan.ComposeYAML) > 128*1024 {
		return "", dockerengine.ComposeProjectScope{}, invalid
	}
	var document map[string]any
	if err := yaml.Unmarshal([]byte(plan.ComposeYAML), &document); err != nil {
		return "", dockerengine.ComposeProjectScope{}, invalid
	}
	services, err := composeconfig.ServiceScope(document, settings.Services)
	if err != nil {
		return "", dockerengine.ComposeProjectScope{}, invalid
	}
	completedServices, err := composeconfig.CompletionServices(document, services)
	if err != nil {
		return "", dockerengine.ComposeProjectScope{}, invalid
	}
	var host model.Host
	if err := s.db.WithContext(ctx).Where("uuid = ? AND is_active = ?", record.HostID, true).First(&host).Error; err != nil {
		return "", dockerengine.ComposeProjectScope{}, err
	}
	if host.ProjectID != record.ProjectID || host.DepartmentID != record.DepartmentID {
		s.logger.WarnContext(ctx, "运行主机归属与发布快照不一致", "operation", "compose_runtime_scope", "host_id", record.HostID, "err", invalid)
		return "", dockerengine.ComposeProjectScope{}, invalid
	}

	var capability model.HostCapability
	if err := s.db.WithContext(ctx).Where("host_id = ? AND kind = ? AND status = ?", record.HostID, model.HostCapabilityDocker, model.HostCapabilityReady).First(&capability).Error; err != nil {
		return "", dockerengine.ComposeProjectScope{}, err
	}
	configurationDirectory := record.WorkingDirectory
	if snapshot.ApplicationConfigDirectory != "" {
		if !configfile.ValidDirectory(snapshot.ApplicationConfigDirectory) {
			return "", dockerengine.ComposeProjectScope{}, invalid
		}
		configurationDirectory = snapshot.ApplicationConfigDirectory
	}
	return capability.RuntimeID, dockerengine.ComposeProjectScope{ConfigurationDirectory: configurationDirectory, ConfigurationParameters: configuration.DeploymentParameters, ConfigurationFiles: configuration.ConfigurationFiles, HostID: record.HostID, ProjectName: settings.ProjectName, WorkingDirectory: record.WorkingDirectory, Services: services, CompletionServices: completedServices, UseSudo: settings.UseSudo, EnvFile: settings.EnvFile, TagVariable: settings.TagVariable}, nil
}
