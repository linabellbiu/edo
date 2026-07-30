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
	ID               string             `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name             string             `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Description      string             `gorm:"type:varchar(500);not null;default:''" json:"description"`
	Platform         DeploymentPlatform `gorm:"type:varchar(16);not null;index" json:"platform"`
	EnvironmentID    string             `gorm:"type:varchar(36);not null;default:'';index" json:"environment_id,omitempty"`
	HostID           string             `gorm:"type:varchar(36);not null;default:'';index" json:"host_id,omitempty"`
	RuntimeID        string             `gorm:"type:varchar(36);not null;index" json:"runtime_id"`
	WorkingDirectory string             `gorm:"type:varchar(1024);not null;default:''" json:"working_directory,omitempty"`
	Namespace        string             `gorm:"type:varchar(253);not null;default:''" json:"namespace"`
	WorkloadName     string             `gorm:"type:varchar(253);not null" json:"workload_name"`
	ContainerName    string             `gorm:"type:varchar(253);not null;default:''" json:"container_name"`
	RolloutTimeout   int                `gorm:"not null;default:300" json:"rollout_timeout"`
	IsActive         bool               `gorm:"not null;default:true;index" json:"is_active"`
	CreatedBy        string             `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt        time.Time          `gorm:"not null" json:"created_at"`
	UpdatedAt        time.Time          `gorm:"not null" json:"updated_at"`
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
	ID                 string                `gorm:"type:varchar(36);primaryKey" json:"id"`
	IdempotencyKey     *string               `gorm:"type:varchar(64);uniqueIndex:idx_deployment_records_idempotency_key" json:"-"`
	RollbackSourceID   string                `gorm:"type:varchar(36);not null;default:'';index:idx_deployment_records_rollback_source_attempt,priority:1" json:"rollback_source_id,omitempty"`
	RollbackAttempt    int                   `gorm:"not null;default:0;index:idx_deployment_records_rollback_source_attempt,priority:2" json:"rollback_attempt,omitempty"`
	PipelineRunID      string                `gorm:"type:varchar(36);not null;default:'';index" json:"pipeline_run_id,omitempty"`
	WorkflowNodeID     string                `gorm:"type:varchar(64);not null;default:'';index" json:"workflow_node_id,omitempty"`
	ArtifactID         string                `gorm:"type:varchar(36);not null;default:'';index" json:"artifact_id,omitempty"`
	TargetID           string                `gorm:"type:varchar(36);not null;index" json:"target_id"`
	TargetName         string                `gorm:"type:varchar(128);not null" json:"target_name"`
	Platform           DeploymentPlatform    `gorm:"type:varchar(16);not null;index" json:"platform"`
	EnvironmentID      string                `gorm:"type:varchar(36);not null;default:'';index" json:"environment_id,omitempty"`
	HostID             string                `gorm:"type:varchar(36);not null;default:'';index" json:"host_id,omitempty"`
	RuntimeID          string                `gorm:"type:varchar(36);not null;index" json:"runtime_id"`
	WorkingDirectory   string                `gorm:"type:varchar(1024);not null;default:''" json:"working_directory,omitempty"`
	Namespace          string                `gorm:"type:varchar(253);not null;default:''" json:"namespace"`
	WorkloadName       string                `gorm:"type:varchar(253);not null" json:"workload_name"`
	ContainerName      string                `gorm:"type:varchar(253);not null;default:''" json:"container_name"`
	RolloutTimeout     int                   `gorm:"not null;default:300" json:"rollout_timeout"`
	Operation          DeploymentOperation   `gorm:"type:varchar(16);not null;index" json:"operation"`
	Image              string                `gorm:"type:varchar(1024);not null" json:"image"`
	ExpectedImageID    string                `gorm:"type:varchar(71);not null;default:''" json:"expected_image_id,omitempty"`
	PreviousImage      string                `gorm:"type:varchar(1024);not null;default:''" json:"previous_image"`
	PreviousImageID    string                `gorm:"type:varchar(71);not null;default:''" json:"previous_image_id,omitempty"`
	DeploymentPlanID   string                `gorm:"type:varchar(36);not null;default:'';index" json:"deployment_plan_id,omitempty"`
	DeploymentPlanKind DeploymentPlanKind    `gorm:"type:varchar(16);not null;default:''" json:"deployment_plan_kind,omitempty"`
	CommandScript      string                `gorm:"type:text;not null" json:"-"`
	CommandDigest      string                `gorm:"type:varchar(64);not null;default:''" json:"command_digest,omitempty"`
	CommandTimeout     int                   `gorm:"not null;default:0" json:"command_timeout,omitempty"`
	CommandExitCode    *int                  `json:"command_exit_code,omitempty"`
	ComposeYAML        string                `gorm:"type:text;not null" json:"-"`
	ComposeService     string                `gorm:"type:varchar(128);not null;default:''" json:"compose_service,omitempty"`
	ComposeDigest      string                `gorm:"type:varchar(64);not null;default:''" json:"compose_digest,omitempty"`
	ComposeTimeout     int                   `gorm:"not null;default:0" json:"compose_timeout,omitempty"`
	DockerConfig       DockerContainerConfig `gorm:"serializer:json;type:text;not null" json:"-"`
	DockerConfigDigest string                `gorm:"type:varchar(64);not null;default:''" json:"docker_config_digest,omitempty"`
	Status             DeploymentStatus      `gorm:"type:varchar(32);not null;index" json:"status"`
	JobID              string                `gorm:"type:varchar(36);not null;default:'';index" json:"job_id"`
	RequestedBy        string                `gorm:"type:varchar(36);not null;index" json:"requested_by"`
	ApprovedBy         *string               `gorm:"type:varchar(36);index" json:"approved_by,omitempty"`
	ApprovedAt         *time.Time            `json:"approved_at,omitempty"`
	ErrorCode          string                `gorm:"type:varchar(64);not null;default:''" json:"error_code"`
	ErrorMessage       string                `gorm:"type:varchar(255);not null;default:''" json:"error_message"`
	WarningMessage     string                `gorm:"type:varchar(255);not null;default:''" json:"warning_message"`
	CreatedAt          time.Time             `gorm:"not null;index" json:"created_at"`
	UpdatedAt          time.Time             `gorm:"not null" json:"updated_at"`
	StartedAt          *time.Time            `json:"started_at,omitempty"`
	FinishedAt         *time.Time            `json:"finished_at,omitempty"`
}

func (DeploymentRecord) TableName() string { return "deployment_records" }
