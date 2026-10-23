package model

import "time"

// DeliveryDefault 保存项目内各部署方式的新建预选，不改变既有应用配置。
type DeliveryDefault struct {
	ID                 string             `gorm:"type:varchar(36);primaryKey" json:"id"`
	ProjectID          string             `gorm:"type:varchar(36);not null;uniqueIndex:ux_delivery_default,priority:1" json:"project_id"`
	DepartmentID       string             `gorm:"type:varchar(36);not null;index" json:"department_id"`
	Kind               DeploymentPlanKind `gorm:"type:varchar(16);not null;uniqueIndex:ux_delivery_default,priority:2" json:"kind"`
	WorkflowTemplateID string             `gorm:"type:varchar(36);not null" json:"workflow_template_id"`
	DeploymentPlanID   string             `gorm:"type:varchar(36);not null" json:"deployment_plan_id"`
	Version            uint64             `gorm:"not null" json:"version"`
	UpdatedBy          string             `gorm:"type:varchar(36);not null" json:"updated_by"`
	CreatedAt          time.Time          `gorm:"not null" json:"created_at"`
	UpdatedAt          time.Time          `gorm:"not null" json:"updated_at"`
}

func (DeliveryDefault) TableName() string { return "delivery_defaults" }
