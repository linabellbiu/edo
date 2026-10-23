package model

import (
	"time"

	"gorm.io/datatypes"
)

type Role struct {
	RowID       uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID          string    `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	Name        string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"name"`
	DisplayName string    `gorm:"type:varchar(64);not null" json:"display_name"`
	Description string    `gorm:"type:varchar(255);not null;default:''" json:"description"`
	CreatedAt   time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt   time.Time `gorm:"not null" json:"updated_at"`
}

func (Role) TableName() string { return "roles" }

type RolePermission struct {
	RowID      uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	RoleID     string    `gorm:"type:varchar(36);not null;uniqueIndex:ux_role_permission_identity;index"`
	Permission string    `gorm:"type:varchar(96);not null;uniqueIndex:ux_role_permission_identity;index"`
	CreatedAt  time.Time `gorm:"not null"`
}

func (RolePermission) TableName() string { return "role_permissions" }

type UserRole struct {
	RowID     uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	UserID    string    `gorm:"type:varchar(36);not null;uniqueIndex:ux_user_role_identity;index"`
	RoleID    string    `gorm:"type:varchar(36);not null;uniqueIndex:ux_user_role_identity;index"`
	CreatedAt time.Time `gorm:"not null"`
}

func (UserRole) TableName() string { return "user_roles" }

type PermissionEffect string

const (
	PermissionAllow PermissionEffect = "allow"
	PermissionDeny  PermissionEffect = "deny"
)

// UserPermission 是角色权限之外的用户级例外；显式拒绝优先于角色授权。
type UserPermission struct {
	RowID      uint64           `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	UserID     string           `gorm:"type:varchar(36);not null;uniqueIndex:ux_user_permission_identity;index"`
	Permission string           `gorm:"type:varchar(96);not null;uniqueIndex:ux_user_permission_identity;index"`
	Effect     PermissionEffect `gorm:"type:varchar(8);not null;index"`
	CreatedAt  time.Time        `gorm:"not null"`
	UpdatedAt  time.Time        `gorm:"not null"`
}

func (UserPermission) TableName() string { return "user_permissions" }

type AuditResult string

const (
	AuditSucceeded AuditResult = "succeeded"
	AuditFailed    AuditResult = "failed"
	AuditDenied    AuditResult = "denied"
)

type AuditLog struct {
	RowID        uint64         `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID           string         `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	DepartmentID string         `gorm:"type:varchar(36);not null;default:'00000000-0000-0000-0000-000000000001';index" json:"department_id"`
	ProjectID    string         `gorm:"type:varchar(36);not null;default:'';index" json:"project_id"`
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
