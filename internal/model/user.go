package model

import "time"

type User struct {
	RowID               uint64 `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID                  string `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex"`
	Username            string `gorm:"type:varchar(32);not null;uniqueIndex"`
	Nickname            string `gorm:"type:varchar(64);not null"`
	DepartmentID        string `gorm:"type:varchar(36);not null;default:'00000000-0000-0000-0000-000000000001';index"`
	PasswordHash        string `gorm:"type:varchar(255);not null"`
	IsActive            bool   `gorm:"not null;default:true;index"`
	IsSuperuser         bool   `gorm:"not null;default:false"`
	// MustChangePassword 为 true 时，该账户必须先修改密码才能使用除改密、身份信息和登出之外的接口。
	// 仅用于首次创建的默认管理员账户，由 EnsureInitialAdmin 设置，修改密码成功后清除。
	MustChangePassword bool   `gorm:"not null;default:false"`
	AuthVersion         uint64 `gorm:"not null;default:1"`
	LastLoginAt         *time.Time
	CreatedAt           time.Time `gorm:"not null"`
	UpdatedAt           time.Time `gorm:"not null"`
}

func (User) TableName() string { return "users" }
