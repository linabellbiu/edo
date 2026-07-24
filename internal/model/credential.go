package model

import "time"

// GitCredential 保存单个用户私有的 Git 令牌或 SSH 私钥。
type GitCredential struct {
	ID               string      `gorm:"type:varchar(36);primaryKey"`
	UserID           string      `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_git_credentials_user_name,priority:1"`
	Name             string      `gorm:"type:varchar(128);not null;uniqueIndex:idx_git_credentials_user_name,priority:2"`
	Provider         GitProvider `gorm:"type:varchar(16);not null;index"`
	AuthType         GitAuthType `gorm:"type:varchar(16);not null"`
	Username         string      `gorm:"type:varchar(255);not null;default:''"`
	SecretCiphertext string      `gorm:"type:text;not null"`
	SecretHint       string      `gorm:"type:varchar(16);not null;default:''"`
	CreatedAt        time.Time   `gorm:"not null"`
	UpdatedAt        time.Time   `gorm:"not null"`
}

func (GitCredential) TableName() string { return "git_credentials" }
