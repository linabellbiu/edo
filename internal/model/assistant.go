package model

import "time"

// AssistantSettings 独立保存全局模型配置，普通配置中心无法读取或覆盖。
type AssistantSettings struct {
	ID               string    `gorm:"type:varchar(36);primaryKey" json:"-"`
	Enabled          bool      `gorm:"not null" json:"-"`
	BaseURL          string    `gorm:"type:varchar(1024);not null" json:"-"`
	Model            string    `gorm:"type:varchar(128);not null" json:"-"`
	APIKeyCiphertext string    `gorm:"type:text;not null" json:"-"`
	Version          uint64    `gorm:"not null" json:"-"`
	MaxSteps         int       `gorm:"not null" json:"-"`
	TimeoutSeconds   int       `gorm:"not null" json:"-"`
	UpdatedBy        string    `gorm:"type:varchar(36);not null" json:"-"`
	UpdatedAt        time.Time `gorm:"not null" json:"-"`
}

func (AssistantSettings) TableName() string { return "assistant_settings" }

type AssistantConversation struct {
	ID           string `gorm:"type:varchar(36);primaryKey"`
	UserID       string `gorm:"type:varchar(36);not null;index:idx_assistant_conversation_scope,priority:1"`
	ProjectID    string `gorm:"type:varchar(36);not null;index:idx_assistant_conversation_scope,priority:2"`
	DepartmentID string `gorm:"type:varchar(36);not null;index"`
	Title        string `gorm:"type:varchar(128);not null"`
	LeaseToken   string `gorm:"type:varchar(36);not null"`
	LeaseUntil   *time.Time
	CreatedAt    time.Time `gorm:"not null"`
	UpdatedAt    time.Time `gorm:"not null;index"`
}

func (AssistantConversation) TableName() string { return "assistant_conversations" }

type AssistantMessage struct {
	ID                  string    `gorm:"type:varchar(36);primaryKey"`
	ConversationID      string    `gorm:"type:varchar(36);not null;index:idx_assistant_message_conversation,priority:1"`
	UserID              string    `gorm:"type:varchar(36);not null;index"`
	ProjectID           string    `gorm:"type:varchar(36);not null;index"`
	DepartmentID        string    `gorm:"type:varchar(36);not null;index"`
	Role                string    `gorm:"type:varchar(16);not null"`
	Content             string    `gorm:"type:text;not null"`
	RequiredPermissions string    `gorm:"type:text;not null"`
	Status              string    `gorm:"type:varchar(16);not null"`
	CreatedAt           time.Time `gorm:"not null;index:idx_assistant_message_conversation,priority:2"`
}

func (AssistantMessage) TableName() string { return "assistant_messages" }

type AssistantAction struct {
	ID             string    `gorm:"type:varchar(36);primaryKey"`
	ConversationID string    `gorm:"type:varchar(36);not null;index"`
	UserID         string    `gorm:"type:varchar(36);not null;index"`
	ProjectID      string    `gorm:"type:varchar(36);not null;index"`
	DepartmentID   string    `gorm:"type:varchar(36);not null;index"`
	Kind           string    `gorm:"type:varchar(32);not null"`
	Title          string    `gorm:"type:varchar(128);not null"`
	Summary        string    `gorm:"type:varchar(1000);not null"`
	TargetID       string    `gorm:"type:varchar(36);not null"`
	TargetName     string    `gorm:"type:varchar(128);not null"`
	Snapshot       string    `gorm:"type:text;not null"`
	Status         string    `gorm:"type:varchar(16);not null;index"`
	Result         string    `gorm:"type:varchar(1000);not null"`
	ExpiresAt      time.Time `gorm:"not null;index"`
	CreatedAt      time.Time `gorm:"not null"`
	UpdatedAt      time.Time `gorm:"not null"`
}

func (AssistantAction) TableName() string { return "assistant_actions" }
