package model

import (
	"time"

	"gorm.io/datatypes"
)

// KubernetesRBACPolicy is opt-in. Missing rows retain the existing EDO-only
// authorization mode; enabled policies never fall back on synchronization errors.
type KubernetesRBACPolicy struct {
	RowID          uint64     `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID             string     `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ClusterID      string     `gorm:"type:varchar(36);not null;uniqueIndex" json:"cluster_id"`
	ProjectID      string     `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID   string     `gorm:"type:varchar(36);not null;index" json:"department_id"`
	Enabled        bool       `gorm:"not null;default:false" json:"enabled"`
	DesiredEnabled bool       `gorm:"not null;default:false" json:"desired_enabled"`
	Revision       uint64     `gorm:"not null;default:0" json:"revision"`
	Status         string     `gorm:"type:varchar(16);not null;default:'disabled'" json:"status"`
	LastError      string     `gorm:"type:varchar(255);not null;default:''" json:"last_error"`
	UpdatedAt      time.Time  `gorm:"not null" json:"updated_at"`
	LeaseOwner     string     `gorm:"type:varchar(36);not null;default:''" json:"-"`
	LeaseUntil     *time.Time `json:"-"`
}

func (KubernetesRBACPolicy) TableName() string { return "kubernetes_rbac_policies" }

type KubernetesRoleGrant struct {
	RowID        uint64         `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID           string         `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ClusterID    string         `gorm:"type:varchar(36);not null;index;uniqueIndex:ux_kubernetes_role_grant,priority:1" json:"cluster_id"`
	ProjectID    string         `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID string         `gorm:"type:varchar(36);not null;index" json:"department_id"`
	RoleID       string         `gorm:"type:varchar(36);not null;index;uniqueIndex:ux_kubernetes_role_grant,priority:2" json:"role_id"`
	Namespace    string         `gorm:"type:varchar(63);not null;default:'';uniqueIndex:ux_kubernetes_role_grant,priority:3" json:"namespace"`
	ClusterScope bool           `gorm:"not null;default:false;uniqueIndex:ux_kubernetes_role_grant,priority:4" json:"cluster_scope"`
	Permissions  datatypes.JSON `gorm:"not null" json:"permissions"`
	CreatedAt    time.Time      `gorm:"not null" json:"created_at"`
	UpdatedAt    time.Time      `gorm:"not null" json:"updated_at"`
}

func (KubernetesRoleGrant) TableName() string { return "kubernetes_role_grants" }

// KubernetesRBACSubject stores only hashes, never bearer tokens or credentials.
type KubernetesRBACSubject struct {
	RowID        uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID           string    `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	ClusterID    string    `gorm:"type:varchar(36);not null;index;uniqueIndex:ux_kubernetes_rbac_subject,priority:1" json:"cluster_id"`
	ProjectID    string    `gorm:"type:varchar(36);not null;index" json:"project_id"`
	DepartmentID string    `gorm:"type:varchar(36);not null;index" json:"department_id"`
	UserID       string    `gorm:"type:varchar(36);not null;index;uniqueIndex:ux_kubernetes_rbac_subject,priority:2" json:"user_id"`
	PolicyHash   string    `gorm:"type:varchar(64);not null" json:"policy_hash"`
	Revision     uint64    `gorm:"not null" json:"revision"`
	Status       string    `gorm:"type:varchar(16);not null" json:"status"`
	UpdatedAt    time.Time `gorm:"not null" json:"updated_at"`
}

func (KubernetesRBACSubject) TableName() string { return "kubernetes_rbac_subjects" }
