package model

import "time"

type ApplicationEnvironment struct {
	ID                 string            `gorm:"type:varchar(36);primaryKey" json:"id"`
	ApplicationID      string            `gorm:"type:varchar(36);not null;uniqueIndex:idx_app_environment" json:"application_id"`
	Key                string            `gorm:"type:varchar(16);not null;uniqueIndex:idx_app_environment" json:"key"`
	Name               string            `gorm:"type:varchar(64);not null" json:"name"`
	Branch             string            `gorm:"type:varchar(255);not null" json:"branch"`
	PollEnabled        bool              `gorm:"not null;default:false" json:"poll_enabled"`
	WatchPush          bool              `gorm:"not null;default:false" json:"watch_push"`
	WatchPullRequest   bool              `gorm:"not null;default:false" json:"watch_pull_request"`
	WatchTags          bool              `gorm:"not null;default:false" json:"watch_tags"`
	TagPattern         string            `gorm:"type:varchar(255);not null;default:''" json:"tag_pattern"`
	ReleasePlanID      string            `gorm:"type:varchar(36);not null;default:'';index" json:"release_plan_id,omitempty"`
	DeploymentTargetID string            `gorm:"type:varchar(36);not null;default:'';index" json:"deployment_target_id,omitempty"`
	SortOrder          int               `gorm:"not null;default:0" json:"sort_order"`
	LastObservedRef    string            `gorm:"type:varchar(512);not null;default:''" json:"last_observed_ref,omitempty"`
	LastObservedCommit string            `gorm:"type:varchar(64);not null;default:''" json:"last_observed_commit,omitempty"`
	LastCheckedAt      *time.Time        `json:"last_checked_at,omitempty"`
	CreatedAt          time.Time         `gorm:"not null" json:"created_at"`
	UpdatedAt          time.Time         `gorm:"not null" json:"updated_at"`
	ReleasePlan        *ReleasePlan      `gorm:"foreignKey:ReleasePlanID;-:migration" json:"release_plan,omitempty"`
	DeploymentTarget   *DeploymentTarget `gorm:"foreignKey:DeploymentTargetID;-:migration" json:"deployment_target,omitempty"`
}

func (ApplicationEnvironment) TableName() string { return "application_environments" }

type WorkflowNodeType string

const (
	WorkflowNodeTrigger  WorkflowNodeType = "trigger"
	WorkflowNodeManual   WorkflowNodeType = "manual"
	WorkflowNodeApproval WorkflowNodeType = "approval"
	WorkflowNodeDeploy   WorkflowNodeType = "deploy"
)

type WorkflowPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type WorkflowNodeConfig struct {
	Environment        string   `json:"environment,omitempty"`
	Branch             string   `json:"branch,omitempty"`
	Events             []string `json:"events,omitempty"`
	TagPattern         string   `json:"tag_pattern,omitempty"`
	ReleasePlanID      string   `json:"release_plan_id,omitempty"`
	DeploymentTargetID string   `json:"deployment_target_id,omitempty"`
	Description        string   `json:"description,omitempty"`
}

type WorkflowNode struct {
	ID       string             `json:"id"`
	Type     WorkflowNodeType   `json:"type"`
	Name     string             `json:"name"`
	Position WorkflowPosition   `json:"position"`
	Config   WorkflowNodeConfig `json:"config"`
}

type WorkflowEdge struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Label  string `json:"label,omitempty"`
}

type WorkflowViewport struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Zoom float64 `json:"zoom"`
}

type ReleaseWorkflow struct {
	ID            string           `gorm:"type:varchar(36);primaryKey" json:"id"`
	ApplicationID string           `gorm:"type:varchar(36);not null;uniqueIndex" json:"application_id"`
	Name          string           `gorm:"type:varchar(128);not null" json:"name"`
	Revision      uint64           `gorm:"not null;default:1" json:"revision"`
	IsActive      bool             `gorm:"not null;default:false;index" json:"is_active"`
	Nodes         []WorkflowNode   `gorm:"serializer:json;type:text;not null" json:"nodes"`
	Edges         []WorkflowEdge   `gorm:"serializer:json;type:text;not null" json:"edges"`
	Viewport      WorkflowViewport `gorm:"serializer:json;type:text;not null" json:"viewport"`
	CreatedBy     string           `gorm:"type:varchar(36);not null;index" json:"created_by"`
	UpdatedBy     string           `gorm:"type:varchar(36);not null;index" json:"updated_by"`
	CreatedAt     time.Time        `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time        `gorm:"not null" json:"updated_at"`
}

func (ReleaseWorkflow) TableName() string { return "release_workflows" }

type PipelineRunApproval struct {
	ID            string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	PipelineRunID string    `gorm:"type:varchar(36);not null;uniqueIndex:idx_run_approval_node" json:"pipeline_run_id"`
	NodeID        string    `gorm:"type:varchar(64);not null;uniqueIndex:idx_run_approval_node" json:"node_id"`
	RequestedBy   string    `gorm:"type:varchar(36);not null;default:'';index" json:"requested_by,omitempty"`
	ApprovedBy    string    `gorm:"type:varchar(36);not null;index" json:"approved_by"`
	ApprovedAt    time.Time `gorm:"not null;index" json:"approved_at"`
}

func (PipelineRunApproval) TableName() string { return "pipeline_run_approvals" }
