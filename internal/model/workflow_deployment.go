package model

import "github.com/google/uuid"

// WorkflowDeploymentPlan 只随流水线定义保存，不创建独立部署方案或发布目标。
type WorkflowDeploymentPlan struct {
	ExecutionConfig  *DeploymentExecutionConfig `json:"execution_config,omitempty"`
	Name             string                     `json:"name"`
	Kind             DeploymentPlanKind         `json:"kind"`
	Description      string                     `json:"description,omitempty"`
	Script           string                     `json:"script,omitempty"`
	ComposeYAML      string                     `json:"compose_yaml,omitempty"`
	ServiceName      string                     `json:"service_name,omitempty"`
	DockerConfig     DockerContainerConfig      `json:"docker_config"`
	TimeoutSeconds   int                        `json:"timeout_seconds"`
	DeploymentTarget *DeploymentTarget          `json:"deployment_target"`
}

// InlineDeploymentIdentity 让同一流水线任务的后续发布继续命中原实例。
func InlineDeploymentIdentity(workflowID, nodeID string) (string, string) {
	identity := "edo.workflow.deployment:" + workflowID + ":" + nodeID
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity+":plan")).String(), uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity+":target")).String()
}

// DeploymentHostNodeID 为每个任务与主机生成稳定身份，重试或调整勾选顺序不会重复接管实例。
func DeploymentHostNodeID(nodeID, hostID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("edo.deployment.host:"+nodeID+":"+hostID)).String()
}
