package model

import "encoding/json"

// WorkflowDeploymentPlanSnapshot 固定选用方案时的默认配置，不保存项目或凭据身份。
type WorkflowDeploymentPlanSnapshot struct {
	Name               string                     `json:"name"`
	Kind               DeploymentPlanKind         `json:"kind"`
	TimeoutSeconds     int                        `json:"timeout_seconds"`
	ExecutionConfig    *DeploymentExecutionConfig `json:"execution_config,omitempty"`
	DeploymentTargetID string                     `json:"deployment_target_id,omitempty"`
	DeploymentTarget   *DeploymentTarget          `json:"deployment_target,omitempty"`
}

func NewWorkflowDeploymentPlanSnapshot(plan DeploymentPlan, target *DeploymentTarget) *WorkflowDeploymentPlanSnapshot {
	result := &WorkflowDeploymentPlanSnapshot{Name: plan.Name, Kind: plan.Kind, TimeoutSeconds: plan.TimeoutSeconds, DeploymentTargetID: plan.DeploymentTargetID}
	if plan.ExecutionConfig != nil {
		result.ExecutionConfig = DeploymentExecutionFromNode(plan.ExecutionConfig.NodeConfig())
	}
	if target != nil {
		result.DeploymentTarget = &DeploymentTarget{Platform: target.Platform, EnvironmentID: target.EnvironmentID, RuntimeID: target.RuntimeID, Namespace: target.Namespace, WorkloadName: target.WorkloadName, ContainerName: target.ContainerName, RolloutTimeout: target.RolloutTimeout}
	}
	return result
}

func ValidateWorkflowDeploymentPlanSnapshot(snapshot *WorkflowDeploymentPlanSnapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil || len(data) > 256*1024 || snapshot.Kind != DeploymentPlanKubernetes || snapshot.Name == "" || len(snapshot.Name) > 512 || snapshot.TimeoutSeconds < 30 || snapshot.TimeoutSeconds > 3600 || len(snapshot.DeploymentTargetID) > 36 {
		return invalidDeploymentOverride(err)
	}
	if target := snapshot.DeploymentTarget; target != nil {
		if target.ID != "" || target.RowID != 0 || target.ProjectID != "" || target.DepartmentID != "" || target.CreatedBy != "" || !target.CreatedAt.IsZero() || !target.UpdatedAt.IsZero() || target.IsActive || target.Platform != DeploymentKubernetes || target.HostID != "" || target.HostIDs != nil || target.WorkingDirectory != "" {
			return invalidDeploymentOverride(nil)
		}
	}
	if (snapshot.DeploymentTargetID != "") != (snapshot.DeploymentTarget != nil) {
		return invalidDeploymentOverride(nil)
	}
	if snapshot.ExecutionConfig != nil {
		return ValidateKubernetesDeploymentSettings(snapshot.ExecutionConfig.Kubernetes)
	}
	return nil
}

// WorkflowDeploymentSnapshotBase 只替换配置；当前项目、来源方案身份和有效授权仍由服务端核验。
func WorkflowDeploymentSnapshotBase(base DeploymentPlan, override *WorkflowDeploymentOverride) DeploymentPlan {
	if override == nil || override.PlanSnapshot == nil {
		return base
	}
	snapshot := override.PlanSnapshot
	base.Name, base.Kind, base.TimeoutSeconds = snapshot.Name, snapshot.Kind, snapshot.TimeoutSeconds
	base.ExecutionConfig = nil
	if snapshot.ExecutionConfig != nil {
		base.ExecutionConfig = DeploymentExecutionFromNode(snapshot.ExecutionConfig.NodeConfig())
	}
	base.DeploymentTargetID = snapshot.DeploymentTargetID
	base.DeploymentTarget = nil
	if snapshot.DeploymentTarget != nil {
		target := *snapshot.DeploymentTarget
		target.ID = snapshot.DeploymentTargetID
		target.ProjectID = base.ProjectID
		target.DepartmentID = base.DepartmentID
		base.DeploymentTarget = &target
	}
	return base
}
