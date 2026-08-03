package model

import (
	"time"

	"gorm.io/datatypes"
)

type ScheduleAction string

const ScheduleNotification ScheduleAction = "notification"

type ScheduledTask struct {
	ID             string         `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name           string         `gorm:"type:varchar(128);not null;uniqueIndex:ux_scheduled_tasks_department_name,priority:2" json:"name"`
	CronExpression string         `gorm:"type:varchar(128);not null" json:"cron_expression"`
	Timezone       string         `gorm:"type:varchar(64);not null" json:"timezone"`
	Action         ScheduleAction `gorm:"type:varchar(32);not null;index" json:"action"`
	Payload        datatypes.JSON `gorm:"not null" json:"payload"`
	IsActive       bool           `gorm:"not null;default:true;index" json:"is_active"`
	NextRunAt      time.Time      `gorm:"not null;index" json:"next_run_at"`
	LastRunAt      *time.Time     `json:"last_run_at,omitempty"`
	LastJobID      string         `gorm:"type:varchar(36);not null;default:'';index" json:"last_job_id"`
	DepartmentID   string         `gorm:"type:varchar(36);not null;default:'00000000-0000-0000-0000-000000000001';index;uniqueIndex:ux_scheduled_tasks_department_name,priority:1" json:"department_id"`
	CreatedBy      string         `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt      time.Time      `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time      `gorm:"not null" json:"updated_at"`
}

func (ScheduledTask) TableName() string { return "scheduled_tasks" }

type MonitorStatus string

const (
	MonitorUnknown   MonitorStatus = "unknown"
	MonitorHealthy   MonitorStatus = "healthy"
	MonitorUnhealthy MonitorStatus = "unhealthy"
)

type MonitorRule struct {
	ID                    string        `gorm:"type:varchar(36);primaryKey"`
	Name                  string        `gorm:"type:varchar(128);not null;uniqueIndex:ux_monitor_rules_department_name,priority:2"`
	EndpointCiphertext    string        `gorm:"type:text;not null"`
	EndpointDisplay       string        `gorm:"type:varchar(1024);not null"`
	Method                string        `gorm:"type:varchar(8);not null;default:'GET'"`
	ExpectedStatusMin     int           `gorm:"not null;default:200"`
	ExpectedStatusMax     int           `gorm:"not null;default:399"`
	TimeoutSeconds        int           `gorm:"not null;default:10"`
	IntervalSeconds       int           `gorm:"not null;default:60"`
	FailureThreshold      int           `gorm:"not null;default:3"`
	RecoveryThreshold     int           `gorm:"not null;default:2"`
	NotificationChannelID string        `gorm:"type:varchar(36);not null;default:'';index"`
	Status                MonitorStatus `gorm:"type:varchar(16);not null;default:'unknown';index"`
	ConsecutiveFailures   int           `gorm:"not null;default:0"`
	ConsecutiveSuccesses  int           `gorm:"not null;default:0"`
	IsActive              bool          `gorm:"not null;default:true;index"`
	NextRunAt             time.Time     `gorm:"not null;index"`
	LastRunAt             *time.Time
	LastChangedAt         *time.Time
	LastJobID             string    `gorm:"type:varchar(36);not null;default:'';index"`
	DepartmentID          string    `gorm:"type:varchar(36);not null;default:'00000000-0000-0000-0000-000000000001';index;uniqueIndex:ux_monitor_rules_department_name,priority:1"`
	CreatedBy             string    `gorm:"type:varchar(36);not null;index"`
	CreatedAt             time.Time `gorm:"not null"`
	UpdatedAt             time.Time `gorm:"not null"`
}

func (MonitorRule) TableName() string { return "monitor_rules" }

type MonitorCheck struct {
	ID             string        `gorm:"type:varchar(36);primaryKey" json:"id"`
	RuleID         string        `gorm:"type:varchar(36);not null;index" json:"rule_id"`
	JobID          string        `gorm:"type:varchar(36);not null;uniqueIndex" json:"job_id"`
	Status         MonitorStatus `gorm:"type:varchar(16);not null;index" json:"status"`
	StatusCode     int           `gorm:"not null;default:0" json:"status_code"`
	LatencyMS      int64         `gorm:"not null;default:0" json:"latency_ms"`
	ErrorMessage   string        `gorm:"type:varchar(255);not null;default:''" json:"error_message"`
	AlertType      string        `gorm:"type:varchar(16);not null;default:''" json:"alert_type"`
	NotificationID string        `gorm:"type:varchar(36);not null;default:'';index" json:"notification_id"`
	CheckedAt      time.Time     `gorm:"not null;index" json:"checked_at"`
}

func (MonitorCheck) TableName() string { return "monitor_checks" }
