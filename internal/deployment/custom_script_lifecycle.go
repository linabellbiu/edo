package deployment

import (
	"context"
	"encoding/json"

	"edo/internal/configfile"
	"edo/internal/model"
)

// 自定义脚本实例沿用原运行的目录和配置引用，重启时只刷新这些引用的正文。
func (s *Service) prepareCustomLifecycle(ctx context.Context, record *model.DeploymentRecord, actorID, action, script string) (string, map[string]string, error) {
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND application_id = ?", record.PipelineRunID, record.ProjectID, record.ApplicationID).First(&run).Error; err != nil {
		return "", nil, s.configurationError(record, "script_lifecycle_run", err)
	}
	var snapshot struct {
		Directory string                                     `json:"application_config_directory"`
		Bindings  map[string][]model.DeploymentConfigBinding `json:"configuration_bindings"`
	}
	if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		return "", nil, s.configurationError(record, "script_lifecycle_snapshot", err)
	}
	paths := []string{}
	environment := map[string]string{}
	if (action == "restart" || action == "recreate") && len(snapshot.Bindings[record.WorkflowNodeID]) > 0 {
		if err := s.requireCurrentActorPermission(ctx, record, actorID, "delivery_variable.execute"); err != nil {
			return "", nil, err
		}
		set, err := s.ResolveDeploymentConfiguration(ctx, record, true)
		if err != nil {
			return "", nil, err
		}
		for _, binding := range set.Bindings {
			paths = append(paths, binding.Path)
		}
		for path, body := range set.Files {
			environment[configfile.TransportKey(path)] = body
		}
	}
	prefix, err := configfile.WriteScript(snapshot.Directory, paths)
	if err != nil {
		return "", nil, s.configurationError(record, "script_lifecycle_directory", err)
	}
	if record.WorkingDirectory == "" {
		prefix += "cd \"${EDO_APP_CONFIG_PATH:?缺少应用配置目录}\"\n"
	}
	return prefix + script, environment, nil
}
