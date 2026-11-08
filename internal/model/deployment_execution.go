package model

import "encoding/json"

// DeploymentExecutionConfig 归部署方案所有，不包含通知、流水线身份或发布目标。
// nil 表示尚未迁移的旧方案，继续读取原任务参数；空对象表示明确清空部署参数。
type DeploymentExecutionConfig struct {
	DeploymentRegistryID   string                     `json:"deployment_registry_id,omitempty"`
	DeploymentInputVersion int                        `json:"deployment_input_version,omitempty"`
	DeploymentInputs       []DeploymentInput          `json:"deployment_inputs,omitempty"`
	DeploymentScript       string                     `json:"deployment_script,omitempty"`
	DeploymentTagReference string                     `json:"deployment_tag_reference,omitempty"`
	ConfigurationFiles     []DeploymentConfigFile     `json:"configuration_files,omitempty"`
	DeploymentParameters   string                     `json:"deployment_parameters,omitempty"`
	EnvironmentVariables   map[string]string          `json:"environment_variables,omitempty"`
	VariableNames          []string                   `json:"variable_names,omitempty"`
	ArtifactNodeID         string                     `json:"artifact_node_id,omitempty"`
	ArtifactBuildPlanID    string                     `json:"artifact_build_plan_id,omitempty"`
	ArtifactNodeIDs        []string                   `json:"artifact_node_ids,omitempty"`
	ComposeImages          []ComposeImageBinding      `json:"compose_images,omitempty"`
	Compose                *ComposeDeploymentSettings `json:"compose,omitempty"`
}

// NodeConfig 返回独立副本，运行解析不能修改共享方案或其他任务。
func (c DeploymentExecutionConfig) NodeConfig() WorkflowNodeConfig {
	data, _ := json.Marshal(c)
	var config WorkflowNodeConfig
	_ = json.Unmarshal(data, &config)
	return config
}

func DeploymentExecutionFromNode(config WorkflowNodeConfig) *DeploymentExecutionConfig {
	data, _ := json.Marshal(config)
	var result DeploymentExecutionConfig
	_ = json.Unmarshal(data, &result)
	return &result
}

// ApplyDeploymentExecution 只在生成当前有效配置时使用；历史运行不重新应用当前方案。
func ApplyDeploymentExecution(config *WorkflowNodeConfig, execution *DeploymentExecutionConfig) {
	if execution == nil {
		config.DeploymentRegistryID = ""
		return
	}
	value := execution.NodeConfig()
	config.DeploymentRegistryID = value.DeploymentRegistryID
	config.DeploymentInputVersion, config.DeploymentInputs = value.DeploymentInputVersion, value.DeploymentInputs
	config.DeploymentScript, config.DeploymentTagReference = value.DeploymentScript, value.DeploymentTagReference
	config.ConfigurationFiles, config.DeploymentParameters = value.ConfigurationFiles, value.DeploymentParameters
	config.ConfigurationDirectory = ""
	config.EnvironmentVariables, config.VariableNames = value.EnvironmentVariables, value.VariableNames
	config.ArtifactNodeID, config.ArtifactNodeIDs, config.ArtifactBuildPlanID = value.ArtifactNodeID, value.ArtifactNodeIDs, value.ArtifactBuildPlanID
	config.ComposeImages, config.Compose = value.ComposeImages, value.Compose
}

// SavedDeploymentRegistryID 只读取已保存的仓库选择；旧文件来源中的显式仓库同样有效，不推断上游构建。
func SavedDeploymentRegistryID(config WorkflowNodeConfig) string {
	if config.DeploymentRegistryID != "" {
		return config.DeploymentRegistryID
	}
	for _, input := range config.DeploymentInputs {
		if input.Kind == "registry" && input.RegistryID != "" {
			return input.RegistryID
		}
	}
	return ""
}

func RequiresDeploymentRegistry(kind DeploymentPlanKind) bool {
	return kind == DeploymentPlanDocker || kind == DeploymentPlanCompose || kind == DeploymentPlanComposeStack
}
