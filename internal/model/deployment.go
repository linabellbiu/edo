package model

import "time"

type DeploymentPlatform string

const (
	DeploymentSSH        DeploymentPlatform = "ssh"
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
	ID          string             `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name        string             `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Description string             `gorm:"type:varchar(500);not null;default:''" json:"description"`
	Platform    DeploymentPlatform `gorm:"type:varchar(16);not null;index" json:"platform"`
	// Environment 仅保留旧数据库列兼容。发布约束由发布方式决定，不再依赖安全级别。
	// Deprecated: 新记录只写空值，旧值不迁移且不再参与业务或通过接口暴露。
	Environment      EnvironmentType `gorm:"type:varchar(16);not null;index" json:"-"`
	EnvironmentID    string          `gorm:"type:varchar(36);not null;default:'';index" json:"environment_id,omitempty"`
	HostID           string          `gorm:"type:varchar(36);not null;default:'';index" json:"host_id,omitempty"`
	RuntimeID        string          `gorm:"type:varchar(36);not null;index" json:"runtime_id"`
	WorkingDirectory string          `gorm:"type:varchar(1024);not null;default:''" json:"working_directory,omitempty"`
	Namespace        string          `gorm:"type:varchar(253);not null;default:''" json:"namespace"`
	WorkloadName     string          `gorm:"type:varchar(253);not null" json:"workload_name"`
	ContainerName    string          `gorm:"type:varchar(253);not null;default:''" json:"container_name"`
	RolloutTimeout   int             `gorm:"not null;default:300" json:"rollout_timeout"`
	IsActive         bool            `gorm:"not null;default:true;index" json:"is_active"`
	CreatedBy        string          `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt        time.Time       `gorm:"not null" json:"created_at"`
	UpdatedAt        time.Time       `gorm:"not null" json:"updated_at"`
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
	ID             string             `gorm:"type:varchar(36);primaryKey" json:"id"`
	PipelineRunID  string             `gorm:"type:varchar(36);not null;default:'';index" json:"pipeline_run_id,omitempty"`
	WorkflowNodeID string             `gorm:"type:varchar(64);not null;default:'';index" json:"workflow_node_id,omitempty"`
	TargetID       string             `gorm:"type:varchar(36);not null;index" json:"target_id"`
	TargetName     string             `gorm:"type:varchar(128);not null" json:"target_name"`
	Platform       DeploymentPlatform `gorm:"type:varchar(16);not null;index" json:"platform"`
	// Environment 仅保留历史发布快照的数据库兼容，不参与执行逻辑。
	// Deprecated: 新记录只写空值，旧值不迁移且不再通过接口暴露。
	Environment        EnvironmentType     `gorm:"type:varchar(16);not null;index" json:"-"`
	EnvironmentID      string              `gorm:"type:varchar(36);not null;default:'';index" json:"environment_id,omitempty"`
	HostID             string              `gorm:"type:varchar(36);not null;default:'';index" json:"host_id,omitempty"`
	RuntimeID          string              `gorm:"type:varchar(36);not null;index" json:"runtime_id"`
	WorkingDirectory   string              `gorm:"type:varchar(1024);not null;default:''" json:"working_directory,omitempty"`
	Namespace          string              `gorm:"type:varchar(253);not null;default:''" json:"namespace"`
	WorkloadName       string              `gorm:"type:varchar(253);not null" json:"workload_name"`
	ContainerName      string              `gorm:"type:varchar(253);not null;default:''" json:"container_name"`
	RolloutTimeout     int                 `gorm:"not null;default:300" json:"rollout_timeout"`
	Operation          DeploymentOperation `gorm:"type:varchar(16);not null;index" json:"operation"`
	Image              string              `gorm:"type:varchar(1024);not null" json:"image"`
	PreviousImage      string              `gorm:"type:varchar(1024);not null;default:''" json:"previous_image"`
	DeploymentPlanID   string              `gorm:"type:varchar(36);not null;default:'';index" json:"deployment_plan_id,omitempty"`
	DeploymentPlanKind DeploymentPlanKind  `gorm:"type:varchar(16);not null;default:''" json:"deployment_plan_kind,omitempty"`
	CommandScript      string              `gorm:"type:text;not null;default:''" json:"-"`
	CommandDigest      string              `gorm:"type:varchar(64);not null;default:''" json:"command_digest,omitempty"`
	CommandTimeout     int                 `gorm:"not null;default:0" json:"command_timeout,omitempty"`
	CommandExitCode    *int                `json:"command_exit_code,omitempty"`
	Status             DeploymentStatus    `gorm:"type:varchar(32);not null;index" json:"status"`
	JobID              string              `gorm:"type:varchar(36);not null;default:'';index" json:"job_id"`
	RequestedBy        string              `gorm:"type:varchar(36);not null;index" json:"requested_by"`
	ApprovedBy         *string             `gorm:"type:varchar(36);index" json:"approved_by,omitempty"`
	ApprovedAt         *time.Time          `json:"approved_at,omitempty"`
	ErrorCode          string              `gorm:"type:varchar(64);not null;default:''" json:"error_code"`
	ErrorMessage       string              `gorm:"type:varchar(255);not null;default:''" json:"error_message"`
	WarningMessage     string              `gorm:"type:varchar(255);not null;default:''" json:"warning_message"`
	CreatedAt          time.Time           `gorm:"not null;index" json:"created_at"`
	UpdatedAt          time.Time           `gorm:"not null" json:"updated_at"`
	StartedAt          *time.Time          `json:"started_at,omitempty"`
	FinishedAt         *time.Time          `json:"finished_at,omitempty"`
}

func (DeploymentRecord) TableName() string { return "deployment_records" }
