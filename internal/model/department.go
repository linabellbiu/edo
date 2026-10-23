package model

import "time"

// Department 保存用户和资源所属的部门。
// 功能权限仍由 Casbin 管理；DepartmentID 只决定用户可以看到和操作哪一组数据。
type Department struct {
	RowID       uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ParentID    string    `gorm:"type:varchar(36);not null;default:'';index" json:"parent_id"`
	ID          string    `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex" json:"id"`
	Name        string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"name"`
	Description string    `gorm:"type:varchar(255);not null;default:''" json:"description"`
	IsActive    bool      `gorm:"not null;default:true;index" json:"is_active"`
	CreatedAt   time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt   time.Time `gorm:"not null" json:"updated_at"`
}

func (Department) TableName() string { return "departments" }
