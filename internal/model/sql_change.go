package model

import (
	"time"

	"gorm.io/gorm"
)

// SQL 工单绑定一次发布和一个真实目标；成功记录不能通过换修订重复执行。
type SQLChange struct {
	RowID               uint64         `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID                  string         `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ProjectID           string         `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID        string         `gorm:"type:varchar(36);not null;index" json:"department_id"`
	Name                string         `gorm:"type:varchar(128);not null" json:"name"`
	ApplicationID       string         `gorm:"type:varchar(36);not null;index" json:"application_id"`
	RepositoryID        string         `gorm:"type:varchar(36);not null" json:"repository_id"`
	Branch              string         `gorm:"type:varchar(512);not null" json:"branch"`
	PipelineRunID       string         `gorm:"type:varchar(36);not null;index" json:"pipeline_run_id"`
	DatabaseID          string         `gorm:"type:varchar(36);not null;index" json:"database_id"`
	EnvironmentID       string         `gorm:"type:varchar(36);not null" json:"environment_id"`
	DatabaseVersion     uint64         `gorm:"not null" json:"database_version"`
	DatabaseFingerprint string         `gorm:"type:varchar(64);not null" json:"database_fingerprint"`
	Phase               string         `gorm:"type:varchar(16);not null" json:"phase"`
	ReviewerID          string         `gorm:"type:varchar(36);not null;index" json:"reviewer_id"`
	Version             uint64         `gorm:"not null" json:"version"`
	RevisionID          string         `gorm:"type:varchar(36);not null" json:"revision_id"`
	Status              string         `gorm:"type:varchar(24);not null;index" json:"status"`
	CreatedBy           string         `gorm:"type:varchar(36);not null" json:"created_by"`
	SuccessAt           *time.Time     `json:"success_at,omitempty"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	DeletedAt           gorm.DeletedAt `gorm:"index" json:"-"`
}

func (SQLChange) TableName() string { return "sql_changes" }

type SQLChangeRevision struct {
	RowID           uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID              string    `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ProjectID       string    `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID    string    `gorm:"type:varchar(36);not null;index" json:"department_id"`
	ChangeID        string    `gorm:"type:varchar(36);not null;uniqueIndex:ux_sql_change_revision,priority:1" json:"change_id"`
	Version         uint64    `gorm:"not null;uniqueIndex:ux_sql_change_revision,priority:2" json:"version"`
	Ciphertext      string    `gorm:"type:text;not null" json:"-"`
	Digest          string    `gorm:"type:varchar(64);not null" json:"digest"`
	CheckJobID      string    `gorm:"type:varchar(36);not null;index" json:"-"`
	CheckPermission string    `gorm:"type:varchar(48);not null" json:"-"`
	CheckStatus     string    `gorm:"type:varchar(24);not null" json:"check_status"`
	CheckSummary    string    `gorm:"type:varchar(500);not null" json:"check_summary"`
	CheckReport     string    `gorm:"type:text;not null" json:"-"`
	CreatedBy       string    `gorm:"type:varchar(36);not null" json:"created_by"`
	AuthorID        string    `gorm:"type:varchar(36);not null" json:"author_id"`
	CreatedAt       time.Time `json:"created_at"`
}

func (SQLChangeRevision) TableName() string { return "sql_change_revisions" }

type SQLChangeReview struct {
	RowID        uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID           string    `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ProjectID    string    `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID string    `gorm:"type:varchar(36);not null;index" json:"department_id"`
	ChangeID     string    `gorm:"type:varchar(36);not null;index" json:"change_id"`
	RevisionID   string    `gorm:"type:varchar(36);not null;index" json:"revision_id"`
	ReviewerID   string    `gorm:"type:varchar(36);not null" json:"reviewer_id"`
	Decision     string    `gorm:"type:varchar(16);not null" json:"decision"`
	Reason       string    `gorm:"type:varchar(1000);not null" json:"reason"`
	CreatedAt    time.Time `json:"created_at"`
}

func (SQLChangeReview) TableName() string { return "sql_change_reviews" }

type SQLChangeExecution struct {
	RowID               uint64     `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID                  string     `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ProjectID           string     `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID        string     `gorm:"type:varchar(36);not null;index" json:"department_id"`
	ChangeID            string     `gorm:"type:varchar(36);not null;index" json:"change_id"`
	RevisionID          string     `gorm:"type:varchar(36);not null" json:"revision_id"`
	PipelineRunID       string     `gorm:"type:varchar(36);not null;index" json:"pipeline_run_id"`
	DatabaseID          string     `gorm:"type:varchar(36);not null" json:"database_id"`
	DatabaseFingerprint string     `gorm:"type:varchar(64);not null" json:"database_fingerprint"`
	EnvironmentID       string     `gorm:"type:varchar(36);not null" json:"environment_id"`
	Phase               string     `gorm:"type:varchar(16);not null" json:"phase"`
	Digest              string     `gorm:"type:varchar(64);not null" json:"digest"`
	ExecutionKey        string     `gorm:"type:varchar(64);not null;uniqueIndex" json:"-"`
	Status              string     `gorm:"type:varchar(24);not null;index" json:"status"`
	ActorID             string     `gorm:"type:varchar(36);not null" json:"actor_id"`
	JobID               string     `gorm:"type:varchar(36);not null;index" json:"job_id"`
	Reason              string     `gorm:"type:varchar(1000);not null" json:"reason"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
}

func (SQLChangeExecution) TableName() string { return "sql_change_executions" }
