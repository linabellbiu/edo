package model

import "time"

type Configuration struct {
	ID               string          `gorm:"type:varchar(36);primaryKey"`
	Namespace        string          `gorm:"type:varchar(64);not null;uniqueIndex:ux_configuration_key"`
	Environment      EnvironmentType `gorm:"type:varchar(16);not null;uniqueIndex:ux_configuration_key;index"`
	Key              string          `gorm:"type:varchar(128);not null;uniqueIndex:ux_configuration_key"`
	Value            string          `gorm:"type:text;not null"`
	SecretCiphertext string          `gorm:"type:text;not null"`
	IsSecret         bool            `gorm:"not null;default:false"`
	Version          int             `gorm:"not null;default:1"`
	IsActive         bool            `gorm:"not null;default:true;index"`
	CreatedBy        string          `gorm:"type:varchar(36);not null;index"`
	UpdatedBy        string          `gorm:"type:varchar(36);not null;index"`
	CreatedAt        time.Time       `gorm:"not null"`
	UpdatedAt        time.Time       `gorm:"not null"`
}

func (Configuration) TableName() string { return "configurations" }

type ConfigurationRevision struct {
	ID               string          `gorm:"type:varchar(36);primaryKey"`
	ConfigurationID  string          `gorm:"type:varchar(36);not null;uniqueIndex:ux_configuration_revision;index"`
	Version          int             `gorm:"not null;uniqueIndex:ux_configuration_revision"`
	Namespace        string          `gorm:"type:varchar(64);not null"`
	Environment      EnvironmentType `gorm:"type:varchar(16);not null"`
	Key              string          `gorm:"type:varchar(128);not null"`
	Value            string          `gorm:"type:text;not null"`
	SecretCiphertext string          `gorm:"type:text;not null"`
	IsSecret         bool            `gorm:"not null"`
	IsActive         bool            `gorm:"not null"`
	ChangedBy        string          `gorm:"type:varchar(36);not null;index"`
	CreatedAt        time.Time       `gorm:"not null;index"`
}

func (ConfigurationRevision) TableName() string { return "configuration_revisions" }
