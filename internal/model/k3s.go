package model

import (
	"gorm.io/datatypes"
	"time"
)

// K3sInstallation stores reviewed plans and their durable execution state. Secrets
// remain in InputCiphertext; Preview and Logs must never contain credentials.
type K3sInstallation struct {
	RowID           uint64         `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID              string         `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ProjectID       string         `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID    string         `gorm:"type:varchar(36);not null;index" json:"department_id"`
	CreatedBy       string         `gorm:"type:varchar(36);not null;index" json:"created_by"`
	Action          string         `gorm:"type:varchar(16);not null" json:"action"`
	Name            string         `gorm:"type:varchar(128);not null" json:"name"`
	Status          string         `gorm:"type:varchar(16);not null;index" json:"status"`
	ClusterID       string         `gorm:"type:varchar(36);not null;default:'';index" json:"cluster_id"`
	InputCiphertext string         `gorm:"type:text;not null" json:"-"`
	Preview         datatypes.JSON `gorm:"not null" json:"-"`
	Hosts           datatypes.JSON `gorm:"not null" json:"-"`
	Logs            string         `gorm:"type:text;not null" json:"logs"`
	ErrorMessage    string         `gorm:"type:varchar(500);not null;default:''" json:"error"`
	LeaseOwner      string         `gorm:"type:varchar(36);not null;default:''" json:"-"`
	LeaseExpiresAt  *time.Time     `gorm:"index" json:"-"`
	ExpiresAt       time.Time      `gorm:"not null" json:"expires_at"`
	CreatedAt       time.Time      `gorm:"not null" json:"created_at"`
	UpdatedAt       time.Time      `gorm:"not null" json:"updated_at"`
	StartedAt       *time.Time     `json:"started_at,omitempty"`
	FinishedAt      *time.Time     `json:"finished_at,omitempty"`
}

func (K3sInstallation) TableName() string { return "k3s_installations" }

// A unique host lock prevents two application instances from installing the same
// machine concurrently. Expired executions are failed, never automatically replayed.
type K3sInstallationHostLock struct {
	RowID          uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	HostID         string    `gorm:"type:varchar(36);not null;uniqueIndex"`
	InstallationID string    `gorm:"type:varchar(36);not null;index"`
	ProjectID      string    `gorm:"type:varchar(36);not null;index"`
	DepartmentID   string    `gorm:"type:varchar(36);not null;index"`
	ExpiresAt      time.Time `gorm:"not null;index"`
}

func (K3sInstallationHostLock) TableName() string { return "k3s_installation_host_locks" }
