package model

import "time"

// Organization 只保存当前公司的资料，不提供跨公司租户切换。
type Organization struct {
	ID          string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name        string    `gorm:"type:varchar(96);not null" json:"name"`
	Description string    `gorm:"type:varchar(255);not null" json:"description"`
	UpdatedAt   time.Time `gorm:"not null" json:"updated_at"`
}

func (Organization) TableName() string { return "organizations" }

type Project struct {
	DepartmentName string    `gorm:"-" json:"department_name"`
	ID             string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	DepartmentID   string    `gorm:"type:varchar(36);not null;index;uniqueIndex:ux_projects_department_name,priority:1" json:"department_id"`
	Name           string    `gorm:"type:varchar(96);not null;uniqueIndex:ux_projects_department_name,priority:2" json:"name"`
	Description    string    `gorm:"type:varchar(255);not null" json:"description"`
	IsActive       bool      `gorm:"not null" json:"is_active"`
	IsDefault      bool      `gorm:"not null" json:"is_default"`
	CreatedAt      time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time `gorm:"not null" json:"updated_at"`
}

func (Project) TableName() string { return "projects" }

type ProjectMember struct {
	ProjectID string    `gorm:"type:varchar(36);primaryKey" json:"project_id"`
	UserID    string    `gorm:"type:varchar(36);primaryKey;index" json:"user_id"`
	RoleID    string    `gorm:"type:varchar(36);not null;index" json:"role_id"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
}

func (ProjectMember) TableName() string { return "project_members" }
