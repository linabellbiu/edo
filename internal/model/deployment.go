package model

import "time"

type DeploymentPlatform string

const (
	DeploymentDocker     DeploymentPlatform = "docker"
	DeploymentKubernetes DeploymentPlatform = "kubernetes"
)

type EnvironmentType string

const (
	EnvironmentGlobal      EnvironmentType = "global"
	EnvironmentDevelopment EnvironmentType = "development"
	EnvironmentStaging     EnvironmentType = "staging"
	EnvironmentProduction  EnvironmentType = "production"
)

type DeploymentTarget struct {
	ID             string             `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name           string             `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Description    string             `gorm:"type:varchar(500);not null;default:''" json:"description"`
	Platform       DeploymentPlatform `gorm:"type:varchar(16);not null;index" json:"platform"`
	Environment    EnvironmentType    `gorm:"type:varchar(16);not null;index" json:"environment"`
	RuntimeID      string             `gorm:"type:varchar(36);not null;index" json:"runtime_id"`
	Namespace      string             `gorm:"type:varchar(253);not null;default:''" json:"namespace"`
	WorkloadName   string             `gorm:"type:varchar(253);not null" json:"workload_name"`
	ContainerName  string             `gorm:"type:varchar(253);not null;default:''" json:"container_name"`
	RolloutTimeout int                `gorm:"not null;default:300" json:"rollout_timeout"`
	IsActive       bool               `gorm:"not null;default:true;index" json:"is_active"`
	CreatedBy      string             `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt      time.Time          `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time          `gorm:"not null" json:"updated_at"`
}

func (DeploymentTarget) TableName() string { return "deployment_targets" }

type DeploymentStatus string

const (
	DeploymentAwaitingApproval DeploymentStatus = "awaiting_approval"
	DeploymentQueued           DeploymentStatus = "queued"
	DeploymentRunning          DeploymentStatus = "running"
	DeploymentSucceeded        DeploymentStatus = "succeeded"
	DeploymentFailed           DeploymentStatus = "failed"
	DeploymentCanceled         DeploymentStatus = "canceled"
)

type DeploymentOperation string

const (
	DeploymentRelease  DeploymentOperation = "release"
	DeploymentRollback DeploymentOperation = "rollback"
)

type DeploymentRecord struct {
	ID             string              `gorm:"type:varchar(36);primaryKey" json:"id"`
	PipelineRunID  string              `gorm:"type:varchar(36);not null;default:'';index" json:"pipeline_run_id,omitempty"`
	WorkflowNodeID string              `gorm:"type:varchar(64);not null;default:'';index" json:"workflow_node_id,omitempty"`
	TargetID       string              `gorm:"type:varchar(36);not null;index" json:"target_id"`
	TargetName     string              `gorm:"type:varchar(128);not null" json:"target_name"`
	Platform       DeploymentPlatform  `gorm:"type:varchar(16);not null;index" json:"platform"`
	Environment    EnvironmentType     `gorm:"type:varchar(16);not null;index" json:"environment"`
	RuntimeID      string              `gorm:"type:varchar(36);not null;index" json:"runtime_id"`
	Namespace      string              `gorm:"type:varchar(253);not null;default:''" json:"namespace"`
	WorkloadName   string              `gorm:"type:varchar(253);not null" json:"workload_name"`
	ContainerName  string              `gorm:"type:varchar(253);not null;default:''" json:"container_name"`
	RolloutTimeout int                 `gorm:"not null;default:300" json:"rollout_timeout"`
	Operation      DeploymentOperation `gorm:"type:varchar(16);not null;index" json:"operation"`
	Image          string              `gorm:"type:varchar(1024);not null" json:"image"`
	PreviousImage  string              `gorm:"type:varchar(1024);not null;default:''" json:"previous_image"`
	Status         DeploymentStatus    `gorm:"type:varchar(32);not null;index" json:"status"`
	JobID          string              `gorm:"type:varchar(36);not null;default:'';index" json:"job_id"`
	RequestedBy    string              `gorm:"type:varchar(36);not null;index" json:"requested_by"`
	ApprovedBy     *string             `gorm:"type:varchar(36);index" json:"approved_by,omitempty"`
	ApprovedAt     *time.Time          `json:"approved_at,omitempty"`
	ErrorCode      string              `gorm:"type:varchar(64);not null;default:''" json:"error_code"`
	ErrorMessage   string              `gorm:"type:varchar(255);not null;default:''" json:"error_message"`
	WarningMessage string              `gorm:"type:varchar(255);not null;default:''" json:"warning_message"`
	CreatedAt      time.Time           `gorm:"not null;index" json:"created_at"`
	UpdatedAt      time.Time           `gorm:"not null" json:"updated_at"`
	StartedAt      *time.Time          `json:"started_at,omitempty"`
	FinishedAt     *time.Time          `json:"finished_at,omitempty"`
}

func (DeploymentRecord) TableName() string { return "deployment_records" }
