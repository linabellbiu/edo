package model

import (
	"time"

	"gorm.io/gorm"
)

// DeliveryVariable 的版本内容保存在独立修订记录中；密钥从不随接口回显。
type DeliveryVariable struct {
	RowID         uint64         `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID            string         `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ProjectID     string         `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID  string         `gorm:"type:varchar(36);not null;index" json:"department_id"`
	ApplicationID string         `gorm:"type:varchar(36);not null;index" json:"application_id"`
	EnvironmentID string         `gorm:"type:varchar(36);not null;index" json:"environment_id"`
	ScopeKey      string         `gorm:"type:varchar(64);not null;uniqueIndex" json:"-"`
	Name          string         `gorm:"type:varchar(128);not null" json:"name"`
	Description   string         `gorm:"type:varchar(500);not null" json:"description"`
	Kind          string         `gorm:"type:varchar(16);not null" json:"kind"`
	Usage         string         `gorm:"type:varchar(16);not null" json:"usage"`
	IsSecret      bool           `gorm:"not null" json:"is_secret"`
	ProtectedRefs []string       `gorm:"serializer:json;type:text;not null" json:"protected_refs"`
	Version       uint64         `gorm:"not null" json:"version"`
	IsActive      bool           `gorm:"not null;index" json:"is_active"`
	CreatedBy     string         `gorm:"type:varchar(36);not null" json:"created_by"`
	UpdatedBy     string         `gorm:"type:varchar(36);not null" json:"updated_by"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}

func (DeliveryVariable) TableName() string { return "delivery_variables" }

type DeliveryVariableRevision struct {
	RowID        uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID           string    `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ProjectID    string    `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID string    `gorm:"type:varchar(36);not null;index" json:"department_id"`
	VariableID   string    `gorm:"type:varchar(36);not null;uniqueIndex:ux_delivery_variable_revision,priority:1" json:"variable_id"`
	Version      uint64    `gorm:"not null;uniqueIndex:ux_delivery_variable_revision,priority:2" json:"version"`
	Value        string    `gorm:"type:text;not null" json:"-"`
	Ciphertext   string    `gorm:"type:text;not null" json:"-"`
	IsSecret     bool      `gorm:"not null" json:"is_secret"`
	CreatedBy    string    `gorm:"type:varchar(36);not null" json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
}

func (DeliveryVariableRevision) TableName() string { return "delivery_variable_revisions" }

// PipelineNodeRun 持久化每个任务的实际状态，阶段推进只依据这些记录。
type PipelineNodeRun struct {
	RowID         uint64            `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID            string            `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ProjectID     string            `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID  string            `gorm:"type:varchar(36);not null;index" json:"department_id"`
	PipelineRunID string            `gorm:"type:varchar(36);not null;uniqueIndex:ux_pipeline_node_run,priority:1;index" json:"pipeline_run_id"`
	NodeID        string            `gorm:"type:varchar(64);not null;uniqueIndex:ux_pipeline_node_run,priority:2" json:"node_id"`
	StageID       string            `gorm:"type:varchar(64);not null" json:"stage_id"`
	Name          string            `gorm:"type:varchar(128);not null" json:"name"`
	Type          WorkflowNodeType  `gorm:"type:varchar(24);not null" json:"type"`
	Status        PipelineRunStatus `gorm:"type:varchar(24);not null;index" json:"status"`
	Stage         string            `gorm:"type:varchar(64);not null" json:"stage"`
	Message       string            `gorm:"type:varchar(500);not null" json:"message"`
	JobID         string            `gorm:"type:varchar(36);not null;index" json:"job_id"`
	ArtifactID    string            `gorm:"type:varchar(36);not null" json:"artifact_id"`
	DeploymentID  string            `gorm:"type:varchar(36);not null" json:"deployment_id"`
	StartedAt     *time.Time        `json:"started_at,omitempty"`
	FinishedAt    *time.Time        `json:"finished_at,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

func (PipelineNodeRun) TableName() string { return "pipeline_node_runs" }

type PipelineCheckReport struct {
	RowID         uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID            string    `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ProjectID     string    `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID  string    `gorm:"type:varchar(36);not null;index" json:"department_id"`
	PipelineRunID string    `gorm:"type:varchar(36);not null;index" json:"pipeline_run_id"`
	NodeID        string    `gorm:"type:varchar(64);not null;index" json:"node_id"`
	ReportKey     string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"-"`
	Kind          string    `gorm:"type:varchar(24);not null" json:"kind"`
	Name          string    `gorm:"type:varchar(128);not null" json:"name"`
	Status        string    `gorm:"type:varchar(24);not null" json:"status"`
	Summary       string    `gorm:"type:text;not null" json:"summary"`
	Findings      string    `gorm:"type:text;not null" json:"findings"`
	CreatedAt     time.Time `json:"created_at"`
}

func (PipelineCheckReport) TableName() string { return "pipeline_check_reports" }

// EnvironmentReleasePolicy 仅在用户显式启用后生效，不从环境名称推导规则。
type EnvironmentReleasePolicy struct {
	RowID            uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID               string    `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ProjectID        string    `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID     string    `gorm:"type:varchar(36);not null;index" json:"department_id"`
	EnvironmentID    string    `gorm:"type:varchar(36);not null;uniqueIndex" json:"environment_id"`
	AllowedRefs      []string  `gorm:"serializer:json;type:text;not null" json:"allowed_refs"`
	RequireApproval  bool      `gorm:"not null" json:"require_approval"`
	ReviewerIDs      []string  `gorm:"serializer:json;type:text;not null" json:"reviewer_ids"`
	MinimumApprovals int       `gorm:"not null" json:"minimum_approvals"`
	WindowStart      string    `gorm:"type:varchar(5);not null" json:"window_start"`
	WindowEnd        string    `gorm:"type:varchar(5);not null" json:"window_end"`
	IsActive         bool      `gorm:"not null" json:"is_active"`
	Version          uint64    `gorm:"not null" json:"version"`
	UpdatedBy        string    `gorm:"type:varchar(36);not null" json:"updated_by"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (EnvironmentReleasePolicy) TableName() string { return "environment_release_policies" }

type ApplicationDatabase struct {
	RowID         uint64         `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID            string         `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ProjectID     string         `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID  string         `gorm:"type:varchar(36);not null;index" json:"department_id"`
	ApplicationID string         `gorm:"type:varchar(36);not null;index" json:"application_id"`
	EnvironmentID string         `gorm:"type:varchar(36);not null;index" json:"environment_id"`
	Name          string         `gorm:"type:varchar(128);not null" json:"name"`
	Driver        string         `gorm:"type:varchar(16);not null" json:"driver"`
	Host          string         `gorm:"type:varchar(253);not null" json:"host"`
	Port          int            `gorm:"not null" json:"port"`
	Database      string         `gorm:"type:varchar(128);not null" json:"database"`
	Username      string         `gorm:"type:varchar(128);not null" json:"username"`
	TLSMode       string         `gorm:"type:varchar(24);not null" json:"tls_mode"`
	Ciphertext    string         `gorm:"type:text;not null" json:"-"`
	Fingerprint   string         `gorm:"type:varchar(64);not null" json:"fingerprint"`
	IsActive      bool           `gorm:"not null" json:"is_active"`
	Version       uint64         `gorm:"not null" json:"version"`
	CreatedBy     string         `gorm:"type:varchar(36);not null" json:"created_by"`
	UpdatedBy     string         `gorm:"type:varchar(36);not null" json:"updated_by"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}

func (ApplicationDatabase) TableName() string { return "application_databases" }

type ApplicationDatabaseMigration struct {
	RowID         uint64     `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID            string     `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ProjectID     string     `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID  string     `gorm:"type:varchar(36);not null;index" json:"department_id"`
	DatabaseID    string     `gorm:"type:varchar(36);not null;index" json:"database_id"`
	PipelineRunID string     `gorm:"type:varchar(36);not null;index" json:"pipeline_run_id"`
	NodeID        string     `gorm:"type:varchar(64);not null" json:"node_id"`
	CommitSHA     string     `gorm:"type:varchar(64);not null" json:"commit_sha"`
	Directory     string     `gorm:"type:varchar(512);not null" json:"directory"`
	Digest        string     `gorm:"type:varchar(64);not null" json:"digest"`
	Manifest      string     `gorm:"type:text;not null" json:"manifest"`
	ExecutionKey  string     `gorm:"type:varchar(64);not null;uniqueIndex" json:"-"`
	Status        string     `gorm:"type:varchar(24);not null;index" json:"status"`
	Message       string     `gorm:"type:varchar(500);not null" json:"message"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

func (ApplicationDatabaseMigration) TableName() string { return "application_database_migrations" }

type RepositoryCheckDelivery struct {
	RowID         uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID            string    `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ProjectID     string    `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID  string    `gorm:"type:varchar(36);not null;index" json:"department_id"`
	PipelineRunID string    `gorm:"type:varchar(36);not null;index" json:"pipeline_run_id"`
	RepositoryID  string    `gorm:"type:varchar(36);not null" json:"repository_id"`
	CommitSHA     string    `gorm:"type:varchar(64);not null" json:"commit_sha"`
	Context       string    `gorm:"type:varchar(128);not null" json:"context"`
	State         string    `gorm:"type:varchar(24);not null" json:"state"`
	DeliveryKey   string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"-"`
	Status        string    `gorm:"type:varchar(24);not null;index" json:"status"`
	Attempts      int       `gorm:"not null" json:"attempts"`
	Message       string    `gorm:"type:varchar(500);not null" json:"message"`
	NextAttemptAt time.Time `gorm:"index" json:"next_attempt_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (RepositoryCheckDelivery) TableName() string { return "repository_check_deliveries" }

type DeliveryVariableBinding struct {
	Name       string `json:"name"`
	VariableID string `json:"variable_id"`
	Version    uint64 `json:"version"`
	Kind       string `json:"kind"`
	IsSecret   bool   `json:"is_secret"`
}

type WorkflowReportConfig struct {
	Format          string   `json:"format"`
	Paths           []string `json:"paths"`
	Required        bool     `json:"required"`
	MinimumCoverage *float64 `json:"minimum_coverage,omitempty"`
	FailSeverity    string   `json:"fail_severity,omitempty"`
}

type WorkflowCheckConfig struct {
	Tool       string `json:"tool"`
	Dialect    string `json:"dialect,omitempty"`
	Path       string `json:"path"`
	ConfigPath string `json:"config_path,omitempty"`
}

type WorkflowCacheConfig struct {
	Key       string   `json:"key"`
	LockFiles []string `json:"lock_files"`
	Paths     []string `json:"paths"`
}

type WorkflowMigrationConfig struct {
	Version     uint64 `json:"version,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	DatabaseID  string `json:"database_id"`
	Directory   string `json:"directory"`
}
