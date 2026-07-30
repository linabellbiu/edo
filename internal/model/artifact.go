package model

import "time"

type BuildRunProducerKind string

const (
	BuildRunProducerDockerfile BuildRunProducerKind = "dockerfile"
	BuildRunProducerScript     BuildRunProducerKind = "script"
	BuildRunProducerUpload     BuildRunProducerKind = "upload"
)

type BuildRunStatus string

const (
	BuildRunStatusQueued    BuildRunStatus = "queued"
	BuildRunStatusRunning   BuildRunStatus = "running"
	BuildRunStatusSucceeded BuildRunStatus = "succeeded"
	BuildRunStatusFailed    BuildRunStatus = "failed"
	BuildRunStatusCanceled  BuildRunStatus = "canceled"
)

// BuildRun 保存一次制品生产过程。上传也作为一种生产方式进入同一条审计链路。
type BuildRun struct {
	ID             string `gorm:"type:varchar(36);primaryKey" json:"id"`
	ApplicationID  string `gorm:"type:varchar(36);not null;index" json:"application_id"`
	PipelineRunID  string `gorm:"type:varchar(36);not null;default:'';index" json:"pipeline_run_id,omitempty"`
	RepositoryID   string `gorm:"type:varchar(36);not null;default:'';index" json:"repository_id,omitempty"`
	WorkflowNodeID string `gorm:"type:varchar(64);not null;default:'';index" json:"workflow_node_id,omitempty"`
	// IdempotencyKey 只在 PipelineRunID 与 WorkflowNodeID 均非空时写入。
	// NULL 可跨 SQLite、PostgreSQL 和 MySQL 重复，非 NULL 值则由唯一索引阻止节点重投重复登记。
	IdempotencyKey *string              `gorm:"type:varchar(160);uniqueIndex" json:"-"`
	BuildPlanID    string               `gorm:"type:varchar(36);not null;default:'';index" json:"build_plan_id,omitempty"`
	ProducerKind   BuildRunProducerKind `gorm:"type:varchar(16);not null;index" json:"producer_kind"`
	Ref            string               `gorm:"type:varchar(512);not null;default:''" json:"ref,omitempty"`
	CommitSHA      string               `gorm:"type:varchar(64);not null;default:'';index" json:"commit_sha,omitempty"`
	PlanSnapshot   string               `gorm:"type:text;not null" json:"-"`
	PlanDigest     string               `gorm:"type:varchar(71);not null;default:''" json:"plan_digest,omitempty"`
	Status         BuildRunStatus       `gorm:"type:varchar(16);not null;index" json:"status"`
	JobID          string               `gorm:"type:varchar(36);not null;default:'';index" json:"job_id,omitempty"`
	ErrorCode      string               `gorm:"type:varchar(64);not null;default:''" json:"error_code,omitempty"`
	ErrorMessage   string               `gorm:"type:varchar(255);not null;default:''" json:"error_message,omitempty"`
	CreatedBy      string               `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt      time.Time            `gorm:"not null;index" json:"created_at"`
	UpdatedAt      time.Time            `gorm:"not null" json:"updated_at"`
	StartedAt      *time.Time           `json:"started_at,omitempty"`
	FinishedAt     *time.Time           `json:"finished_at,omitempty"`
}

func (BuildRun) TableName() string { return "build_runs" }

type ArtifactKind string

const (
	ArtifactKindOCIImage   ArtifactKind = "oci_image"
	ArtifactKindFileBundle ArtifactKind = "file_bundle"
)

type ArtifactStatus string

const (
	ArtifactStatusAvailable ArtifactStatus = "available"
	ArtifactStatusExpired   ArtifactStatus = "expired"
	ArtifactStatusCorrupt   ArtifactStatus = "corrupt"
)

type ArtifactStorageKind string

const (
	ArtifactStorageKindLocalFile    ArtifactStorageKind = "local_file"
	ArtifactStorageKindRegistry     ArtifactStorageKind = "registry"
	ArtifactStorageKindDockerDaemon ArtifactStorageKind = "docker_daemon"
)

// Artifact 是构建和部署之间的不可变边界。available 后只允许变更生命周期状态。
type Artifact struct {
	ID              string              `gorm:"type:varchar(36);primaryKey" json:"id"`
	ApplicationID   string              `gorm:"type:varchar(36);not null;index" json:"application_id"`
	BuildRunID      string              `gorm:"type:varchar(36);not null;index" json:"build_run_id"`
	PipelineRunID   string              `gorm:"type:varchar(36);not null;default:'';index" json:"pipeline_run_id,omitempty"`
	Kind            ArtifactKind        `gorm:"type:varchar(16);not null;index" json:"kind"`
	Status          ArtifactStatus      `gorm:"type:varchar(16);not null;index" json:"status"`
	Name            string              `gorm:"type:varchar(255);not null" json:"name"`
	OriginalName    string              `gorm:"type:varchar(255);not null;default:''" json:"original_name,omitempty"`
	MediaType       string              `gorm:"type:varchar(255);not null;default:''" json:"media_type,omitempty"`
	Digest          string              `gorm:"type:varchar(71);not null;index" json:"digest"`
	SizeBytes       int64               `gorm:"not null;default:0" json:"size_bytes"`
	StorageKind     ArtifactStorageKind `gorm:"type:varchar(24);not null;index" json:"storage_kind"`
	StorageKey      string              `gorm:"type:varchar(1024);not null;default:''" json:"-"`
	ImageRef        string              `gorm:"type:varchar(1024);not null;default:''" json:"image_ref,omitempty"`
	ImageRegistryID string              `gorm:"type:varchar(36);not null;default:'';index" json:"image_registry_id,omitempty"`
	RuntimeID       string              `gorm:"type:varchar(36);not null;default:'';index" json:"runtime_id,omitempty"`
	LocalImageID    string              `gorm:"type:varchar(71);not null;default:''" json:"local_image_id,omitempty"`
	CreatedBy       string              `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt       time.Time           `gorm:"not null;index" json:"created_at"`
	UpdatedAt       time.Time           `gorm:"not null" json:"updated_at"`
}

func (Artifact) TableName() string { return "artifacts" }
