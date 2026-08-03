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
	Name             string             `gorm:"type:varchar(128);not null;uniqueIndex:ux_deployment_targets_department_name,priority:2" json:"name"`
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
	DepartmentID     string             `gorm:"type:varchar(36);not null;default:'00000000-0000-0000-0000-000000000001';index;uniqueIndex:ux_deployment_targets_department_name,priority:1" json:"department_id"`
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
	DepartmentID       string                `gorm:"type:varchar(36);not null;default:'00000000-0000-0000-0000-000000000001';index" json:"department_id"`
	IdempotencyKey     *string               `gorm:"type:varchar(64);uniqueIndex:idx_deployment_records_idempotency_key" json:"-"`
	RollbackSourceID   string                `gorm:"type:varchar(36);not null;default:'';index:idx_deployment_records_rollback_source_attempt,priority:1" json:"rollback_source_id,omitempty"`
	RollbackAttempt    int                   `gorm:"not null;default:0;index:idx_deployment_records_rollback_source_attempt,priority:2" json:"rollback_attempt,omitempty"`
	PipelineRunID      string                `gorm:"type:varchar(36);not null;default:'';index" json:"pipeline_run_id,omitempty"`
	ApplicationID      string                `gorm:"type:varchar(36);not null;default:'';index" json:"application_id,omitempty"`
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
	ImageDisplay       string                `gorm:"type:varchar(255);not null;default:''" json:"image_display,omitempty"`
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
	RuntimeDeletedAt   *time.Time            `json:"runtime_deleted_at,omitempty"`
	CreatedAt          time.Time             `gorm:"not null;index" json:"created_at"`
	UpdatedAt          time.Time             `gorm:"not null" json:"updated_at"`
	StartedAt          *time.Time            `json:"started_at,omitempty"`
	FinishedAt         *time.Time            `json:"finished_at,omitempty"`
}

func (DeploymentRecord) TableName() string { return "deployment_records" }

// DeploymentInstanceControl 保存某个应用在一个部署方案及目标组合下的 Shell 运行控制命令。
// 它属于当前逻辑部署实例，不写入应用全局配置，也不篡改不可变的历史发布快照。
type DeploymentInstanceControl struct {
	ID               string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	ApplicationID    string    `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_deployment_instance_control,priority:1" json:"application_id"`
	DeploymentPlanID string    `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_deployment_instance_control,priority:2" json:"deployment_plan_id"`
	TargetID         string    `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_deployment_instance_control,priority:3" json:"target_id"`
	RestartScript    string    `gorm:"type:text" json:"restart_script"`
	StopScript       string    `gorm:"type:text" json:"stop_script"`
	TimeoutSeconds   int       `gorm:"not null;default:300" json:"timeout_seconds"`
	UpdatedBy        string    `gorm:"type:varchar(36);not null;index" json:"updated_by"`
	CreatedAt        time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"not null" json:"updated_at"`
}

func (DeploymentInstanceControl) TableName() string { return "deployment_instance_controls" }
