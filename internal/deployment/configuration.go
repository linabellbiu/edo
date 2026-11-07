package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"edo/internal/configfile"
	"edo/internal/envtext"
	"edo/internal/model"
)

type releaseConfigurationKey struct{}
type ConfigurationSet struct {
	Bindings   []model.DeploymentConfigBinding
	Files      map[string]string
	Values     map[string]string
	Redactions []string
	Directory  string
	EnvFiles   []string
}

func WithReleaseConfiguration(ctx context.Context, set *ConfigurationSet) context.Context {
	return context.WithValue(ctx, releaseConfigurationKey{}, set)
}
func releaseConfiguration(ctx context.Context) *ConfigurationSet {
	set, _ := ctx.Value(releaseConfigurationKey{}).(*ConfigurationSet)
	return set
}

// ResolveDeploymentConfiguration 从原发布运行获取选择和路径；latest 只刷新正文版本。
func (s *Service) ResolveDeploymentConfiguration(ctx context.Context, record *model.DeploymentRecord, latest bool) (*ConfigurationSet, error) {
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND application_id = ?", record.PipelineRunID, record.ProjectID, record.ApplicationID).First(&run).Error; err != nil {
		return nil, s.configurationError(record, "configuration_run", err)
	}
	var snapshot struct {
		ConfigurationBindings map[string][]model.DeploymentConfigBinding `json:"configuration_bindings"`
		Stages                []model.WorkflowStage                      `json:"stages"`
	}
	if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		return nil, s.configurationError(record, "configuration_snapshot", err)
	}
	bindings := snapshot.ConfigurationBindings[record.WorkflowNodeID]
	if len(bindings) == 0 {
		return nil, nil
	}
	if s.variables == nil || s.variables.service == nil {
		return nil, s.configurationError(record, "configuration_service", ErrExecutionAuthorization)
	}
	var config *model.WorkflowNodeConfig
	for _, stage := range snapshot.Stages {
		for _, node := range stage.Tasks {
			if node.ID == record.WorkflowNodeID {
				if config != nil || node.Type != model.WorkflowNodeDeploy {
					return nil, s.configurationError(record, "configuration_node", ErrRuntimeControlInvalid)
				}
				copy := node.Config
				config = &copy
			}
		}
	}
	if config == nil || config.DeploymentPlanID != record.DeploymentPlanID || len(config.ConfigurationFiles) != len(bindings) {
		return nil, s.configurationError(record, "configuration_identity", ErrRuntimeControlInvalid)
	}
	for i, ref := range config.ConfigurationFiles {
		if ref.ID != bindings[i].VariableID || ref.Path != bindings[i].Path {
			return nil, s.configurationError(record, "configuration_binding", ErrRuntimeControlInvalid)
		}
	}
	var err error
	if latest {
		bindings, err = s.variables.service.LatestConfigFiles(ctx, run.ProjectID, run.ApplicationID, record.EnvironmentID, run.Ref, bindings)
		if err != nil {
			return nil, s.configurationError(record, "configuration_latest", err)
		}
	}
	files, redactions, err := s.variables.service.ResolveConfigFiles(ctx, run.ProjectID, run.ApplicationID, record.EnvironmentID, run.Ref, bindings)
	if err != nil {
		return nil, s.configurationError(record, "configuration_resolve", err)
	}
	envFiles, err := configfile.EnvFileParameters(config.DeploymentParameters, config.ConfigurationFiles)
	if err != nil {
		return nil, s.configurationError(record, "configuration_parameters", err)
	}
	set := &ConfigurationSet{Bindings: bindings, Files: files, Values: map[string]string{}, Redactions: redactions, Directory: configfile.Directory(config.ConfigurationDirectory, run.ProjectID, run.ApplicationID, record.EnvironmentID, run.ID, record.WorkflowNodeID), EnvFiles: envFiles}
	// 显式 --env-file 按参数顺序覆盖，未指定时所选 ENV 按选择顺序注入。
	paths := []string{}
	for _, b := range bindings {
		if b.Kind == "config_env" {
			paths = append(paths, b.Path)
		}
	}
	paths = append(paths, envFiles...)
	for _, name := range paths {
		values, parseErr := envtext.Parse(files[name])
		if parseErr != nil {
			// 空文件或只有注释表示清除该文件原先提供的全部变量。
			empty := true
			for _, line := range strings.Split(files[name], "\n") {
				if text := strings.TrimSpace(line); text != "" && !strings.HasPrefix(text, "#") {
					empty = false
					break
				}
			}
			if empty {
				values = map[string]string{}
				parseErr = nil
			}
		}
		if parseErr != nil {
			return nil, s.configurationError(record, "configuration_env_parse", parseErr)
		}
		for key, value := range values {
			set.Values[key] = value
			if value != "" {
				set.Redactions = append(set.Redactions, value)
			}
		}
	}
	// 配置正文永不作为日志输出，即使配置未标为密钥。
	for _, value := range files {
		if value != "" {
			set.Redactions = append(set.Redactions, value)
			for _, line := range strings.Split(value, "\n") {
				if strings.TrimSpace(line) != "" {
					set.Redactions = append(set.Redactions, line)
				}
			}
		}
	}
	return set, nil
}
func (s *Service) configurationError(record *model.DeploymentRecord, operation string, err error) error {
	s.logger.Error("读取部署配置失败", "operation", operation, "deployment_id", record.ID, "run_id", record.PipelineRunID, "node_id", record.WorkflowNodeID, "err", err)
	return errors.New("部署配置不可用，请检查配置内容、适用范围和使用权限")
}
func configurationPaths(set *ConfigurationSet) []string {
	paths := make([]string, 0, len(set.Bindings))
	for _, binding := range set.Bindings {
		paths = append(paths, binding.Path)
	}
	return paths
}

// 只读取引用元数据以决定是否需要新配置权限，旧变量实例的普通重启保持原行为。
func (s *Service) recordUsesConfiguration(ctx context.Context, record *model.DeploymentRecord) (bool, error) {
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).Select("workflow_snapshot").Where("uuid = ? AND project_id = ? AND application_id = ?", record.PipelineRunID, record.ProjectID, record.ApplicationID).First(&run).Error; err != nil {
		return false, s.configurationError(record, "configuration_presence", err)
	}
	var snapshot struct {
		Bindings map[string][]model.DeploymentConfigBinding `json:"configuration_bindings"`
	}
	if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		return false, s.configurationError(record, "configuration_presence_decode", err)
	}
	return len(snapshot.Bindings[record.WorkflowNodeID]) > 0, nil
}
