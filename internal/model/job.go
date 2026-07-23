package model

import (
	"time"

	"gorm.io/datatypes"
)

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobCanceled  JobStatus = "canceled"
)

type Job struct {
	ID             string         `gorm:"type:varchar(36);primaryKey"`
	Kind           string         `gorm:"type:varchar(64);not null;index"`
	Subject        string         `gorm:"type:varchar(128);not null"`
	Status         JobStatus      `gorm:"type:varchar(16);not null;index"`
	Payload        datatypes.JSON `gorm:"not null"`
	IdempotencyKey *string        `gorm:"type:varchar(128);uniqueIndex"`
	Attempt        int            `gorm:"not null;default:0"`
	MaxAttempts    int            `gorm:"not null"`
	IsIdempotent   bool           `gorm:"not null;default:false"`
	ErrorCode      string         `gorm:"type:varchar(64)"`
	ErrorMessage   string         `gorm:"type:varchar(255)"`
	CreatedAt      time.Time      `gorm:"not null"`
	UpdatedAt      time.Time      `gorm:"not null"`
	StartedAt      *time.Time
	FinishedAt     *time.Time
	LeaseOwner     string     `gorm:"type:varchar(64);not null;default:'';index"`
	LeaseExpiresAt *time.Time `gorm:"index"`
}

func (Job) TableName() string { return "jobs" }

type OutboxEvent struct {
	ID              uint64         `gorm:"primaryKey;autoIncrement"`
	EventID         string         `gorm:"type:varchar(36);not null;uniqueIndex"`
	AggregateID     string         `gorm:"type:varchar(36);not null;index"`
	Subject         string         `gorm:"type:varchar(128);not null;index"`
	Payload         datatypes.JSON `gorm:"not null"`
	PublishAttempts int            `gorm:"not null;default:0"`
	NextAttemptAt   time.Time      `gorm:"not null;index"`
	PublishedAt     *time.Time     `gorm:"index"`
	FailedAt        *time.Time     `gorm:"index"`
	LastError       string         `gorm:"type:text"`
	CreatedAt       time.Time      `gorm:"not null"`
}

func (OutboxEvent) TableName() string { return "outbox_events" }

type SchemaMigration struct {
	Version   string    `gorm:"type:varchar(32);primaryKey"`
	AppliedAt time.Time `gorm:"not null"`
}

func (SchemaMigration) TableName() string { return "schema_migrations" }
