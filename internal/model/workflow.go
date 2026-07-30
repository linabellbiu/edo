package model

import "time"

type WorkflowNodeType string

const WorkflowSchemaVersion uint16 = 1

const (
	WorkflowNodeTrigger  WorkflowNodeType = "trigger"
	WorkflowNodeBuild    WorkflowNodeType = "build"
	WorkflowNodeShell    WorkflowNodeType = "shell"
	WorkflowNodeManual   WorkflowNodeType = "manual"
	WorkflowNodeApproval WorkflowNodeType = "approval"
	WorkflowNodeDeploy   WorkflowNodeType = "deploy"
)

type WorkflowNodeConfig struct {
	Branch               string            `json:"branch,omitempty"`
	Events               []string          `json:"events,omitempty"`
	TagPattern           string            `json:"tag_pattern,omitempty"`
	PRTargetPattern      string            `json:"pr_target_pattern,omitempty"`
	PRSourcePattern      string            `json:"pr_source_pattern,omitempty"`
	PRActions            []string          `json:"pr_actions,omitempty"`
	BuildPlanID          string            `json:"build_plan_id,omitempty"`
	DeploymentPlanID     string            `json:"deployment_plan_id,omitempty"`
	Script               string            `json:"script,omitempty"`
	RuntimeImage         string            `json:"runtime_image,omitempty"`
	WorkingDirectory     string            `json:"working_directory,omitempty"`
	TimeoutSeconds       int               `json:"timeout_seconds,omitempty"`
	EnvironmentVariables map[string]string `json:"environment_variables,omitempty"`
	Description          string            `json:"description,omitempty"`
}

type WorkflowNode struct {
	ID     string             `json:"id"`
	Type   WorkflowNodeType   `json:"type"`
	Name   string             `json:"name"`
	Config WorkflowNodeConfig `json:"config"`
}

type WorkflowStage struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Tasks []WorkflowNode `gorm:"serializer:json" json:"tasks"`
}

type ReleaseWorkflow struct {
	ID                       string          `gorm:"type:varchar(36);primaryKey" json:"id"`
	ApplicationID            string          `gorm:"type:varchar(36);not null;uniqueIndex" json:"application_id"`
	WorkflowTemplateID       string          `gorm:"type:varchar(36);not null;default:'';index" json:"workflow_template_id,omitempty"`
	WorkflowTemplateRevision uint64          `gorm:"not null;default:0" json:"workflow_template_revision,omitempty"`
	SchemaVersion            uint16          `gorm:"not null;default:1" json:"schema_version"`
	Name                     string          `gorm:"type:varchar(128);not null" json:"name"`
	Revision                 uint64          `gorm:"not null;default:1" json:"revision"`
	IsActive                 bool            `gorm:"not null;default:false;index" json:"is_active"`
	Source                   WorkflowNode    `gorm:"serializer:json;type:text;not null" json:"source"`
	Stages                   []WorkflowStage `gorm:"serializer:json;type:text;not null" json:"stages"`
	CreatedBy                string          `gorm:"type:varchar(36);not null;index" json:"created_by"`
	UpdatedBy                string          `gorm:"type:varchar(36);not null;index" json:"updated_by"`
	CreatedAt                time.Time       `gorm:"not null" json:"created_at"`
	UpdatedAt                time.Time       `gorm:"not null" json:"updated_at"`
}

func (ReleaseWorkflow) TableName() string { return "release_workflows" }

// ReleaseWorkflowTemplate 是可以被多个应用复用的流水线方案。
// 已关联的应用跟随启用版本；直接修改应用流水线时解除关联，保留独立配置。
type ReleaseWorkflowTemplate struct {
	ID            string          `gorm:"type:varchar(36);primaryKey" json:"id"`
	SchemaVersion uint16          `gorm:"not null;default:1" json:"schema_version"`
	Name          string          `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Description   string          `gorm:"type:varchar(500);not null;default:''" json:"description"`
	Revision      uint64          `gorm:"not null;default:1" json:"revision"`
	IsActive      bool            `gorm:"not null;default:false;index" json:"is_active"`
	Source        WorkflowNode    `gorm:"serializer:json;type:text;not null" json:"source"`
	Stages        []WorkflowStage `gorm:"serializer:json;type:text;not null" json:"stages"`
	CreatedBy     string          `gorm:"type:varchar(36);not null;index" json:"created_by"`
	UpdatedBy     string          `gorm:"type:varchar(36);not null;index" json:"updated_by"`
	CreatedAt     time.Time       `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time       `gorm:"not null" json:"updated_at"`
}

func (ReleaseWorkflowTemplate) TableName() string { return "release_workflow_templates" }

type PipelineRunApproval struct {
	ID            string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	PipelineRunID string    `gorm:"type:varchar(36);not null;uniqueIndex:idx_run_approval_node" json:"pipeline_run_id"`
	NodeID        string    `gorm:"type:varchar(64);not null;uniqueIndex:idx_run_approval_node" json:"node_id"`
	RequestedBy   string    `gorm:"type:varchar(36);not null;default:'';index" json:"requested_by,omitempty"`
	ApprovedBy    string    `gorm:"type:varchar(36);not null;index" json:"approved_by"`
	ApprovedAt    time.Time `gorm:"not null;index" json:"approved_at"`
}

func (PipelineRunApproval) TableName() string { return "pipeline_run_approvals" }
