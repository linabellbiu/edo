package model

import "time"

type NotificationChannelType string

const NotificationChannelWebhook NotificationChannelType = "webhook"

type NotificationChannel struct {
	ID                 string                  `gorm:"type:varchar(36);primaryKey"`
	Name               string                  `gorm:"type:varchar(128);not null;uniqueIndex:ux_notification_channels_department_name,priority:2"`
	Type               NotificationChannelType `gorm:"type:varchar(16);not null;index"`
	EndpointCiphertext string                  `gorm:"type:text;not null"`
	TokenCiphertext    string                  `gorm:"type:text;not null"`
	IsActive           bool                    `gorm:"not null;default:true;index"`
	DepartmentID       string                  `gorm:"type:varchar(36);not null;default:'00000000-0000-0000-0000-000000000001';index;uniqueIndex:ux_notification_channels_department_name,priority:1"`
	CreatedBy          string                  `gorm:"type:varchar(36);not null;index"`
	CreatedAt          time.Time               `gorm:"not null"`
	UpdatedAt          time.Time               `gorm:"not null"`
}

func (NotificationChannel) TableName() string { return "notification_channels" }

type NotificationSeverity string

const (
	NotificationInfo     NotificationSeverity = "info"
	NotificationWarning  NotificationSeverity = "warning"
	NotificationCritical NotificationSeverity = "critical"
)

type NotificationStatus string

const (
	NotificationQueued    NotificationStatus = "queued"
	NotificationSucceeded NotificationStatus = "succeeded"
	NotificationFailed    NotificationStatus = "failed"
)

type Notification struct {
	ID           string               `gorm:"type:varchar(36);primaryKey" json:"id"`
	DepartmentID string               `gorm:"type:varchar(36);not null;default:'00000000-0000-0000-0000-000000000001';index" json:"department_id"`
	ChannelID    string               `gorm:"type:varchar(36);not null;index" json:"channel_id"`
	Title        string               `gorm:"type:varchar(255);not null" json:"title"`
	Message      string               `gorm:"type:text;not null" json:"message"`
	Severity     NotificationSeverity `gorm:"type:varchar(16);not null;index" json:"severity"`
	Source       string               `gorm:"type:varchar(64);not null;default:'';index" json:"source"`
	SourceID     string               `gorm:"type:varchar(128);not null;default:'';index" json:"source_id"`
	DedupeKey    *string              `gorm:"type:varchar(191);uniqueIndex" json:"-"`
	Status       NotificationStatus   `gorm:"type:varchar(16);not null;index" json:"status"`
	JobID        string               `gorm:"type:varchar(36);not null;default:'';index" json:"job_id"`
	Attempts     int                  `gorm:"not null;default:0" json:"attempts"`
	ErrorMessage string               `gorm:"type:varchar(255);not null;default:''" json:"error_message"`
	CreatedAt    time.Time            `gorm:"not null;index" json:"created_at"`
	UpdatedAt    time.Time            `gorm:"not null" json:"updated_at"`
	SentAt       *time.Time           `json:"sent_at,omitempty"`
}

func (Notification) TableName() string { return "notifications" }
