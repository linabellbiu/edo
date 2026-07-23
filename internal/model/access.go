package model

import (
	"time"

	"gorm.io/datatypes"
)

type Role struct {
	ID          string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name        string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"name"`
	DisplayName string    `gorm:"type:varchar(64);not null" json:"display_name"`
	Description string    `gorm:"type:varchar(255);not null;default:''" json:"description"`
	CreatedAt   time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt   time.Time `gorm:"not null" json:"updated_at"`
}

func (Role) TableName() string { return "roles" }

type RolePermission struct {
	RoleID     string    `gorm:"type:varchar(36);primaryKey;index"`
	Permission string    `gorm:"type:varchar(96);primaryKey;index"`
	CreatedAt  time.Time `gorm:"not null"`
}

func (RolePermission) TableName() string { return "role_permissions" }

type UserRole struct {
	UserID    string    `gorm:"type:varchar(36);primaryKey;index"`
	RoleID    string    `gorm:"type:varchar(36);primaryKey;index"`
	CreatedAt time.Time `gorm:"not null"`
}

func (UserRole) TableName() string { return "user_roles" }

type AuditResult string

const (
	AuditSucceeded AuditResult = "succeeded"
	AuditFailed    AuditResult = "failed"
	AuditDenied    AuditResult = "denied"
)

type AuditLog struct {
	ID           string         `gorm:"type:varchar(36);primaryKey" json:"id"`
	ActorUserID  *string        `gorm:"type:varchar(36);index" json:"actor_user_id,omitempty"`
	Action       string         `gorm:"type:varchar(96);not null;index" json:"action"`
	ResourceType string         `gorm:"type:varchar(64);not null;index" json:"resource_type"`
	ResourceID   string         `gorm:"type:varchar(128);not null;default:'';index" json:"resource_id,omitempty"`
	Result       AuditResult    `gorm:"type:varchar(16);not null;index" json:"result"`
	RequestID    string         `gorm:"type:varchar(64);not null;default:'';index" json:"request_id,omitempty"`
	ClientIP     string         `gorm:"type:varchar(64);not null;default:''" json:"client_ip"`
	UserAgent    string         `gorm:"type:varchar(512);not null;default:''" json:"user_agent"`
	Metadata     datatypes.JSON `gorm:"not null" json:"metadata"`
	CreatedAt    time.Time      `gorm:"not null;index" json:"created_at"`
}

func (AuditLog) TableName() string { return "audit_logs" }
