package model

import "time"

type GitProvider string

const (
	GitProviderGeneric GitProvider = "generic"
	GitProviderGitHub  GitProvider = "github"
	GitProviderGitLab  GitProvider = "gitlab"
	GitProviderGitea   GitProvider = "gitea"
	GitProviderGitee   GitProvider = "gitee"
)

type GitAuthType string

const (
	GitAuthNone   GitAuthType = "none"
	GitAuthToken  GitAuthType = "token"
	GitAuthSSHKey GitAuthType = "ssh_key"
)

type GitRepository struct {
	ID                      string      `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name                    string      `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Provider                GitProvider `gorm:"type:varchar(16);not null;index" json:"provider"`
	CloneURL                string      `gorm:"type:varchar(1024);not null" json:"clone_url"`
	DefaultBranch           string      `gorm:"type:varchar(255);not null;default:''" json:"default_branch"`
	AuthType                GitAuthType `gorm:"type:varchar(16);not null" json:"auth_type"`
	Username                string      `gorm:"type:varchar(255);not null;default:''" json:"username,omitempty"`
	CredentialID            *string     `gorm:"type:varchar(36);index" json:"credential_id,omitempty"`
	APICredentialID         *string     `gorm:"type:varchar(36);index" json:"api_credential_id,omitempty"`
	CredentialCiphertext    string      `gorm:"type:text;not null" json:"-"`
	WebhookSecretCiphertext string      `gorm:"type:text;not null" json:"-"`
	WebhookEnabled          bool        `gorm:"not null;default:false;index" json:"webhook_enabled"`
	AllowInsecureHTTP       bool        `gorm:"not null;default:false" json:"allow_insecure_http"`
	IsActive                bool        `gorm:"not null;default:true;index" json:"is_active"`
	CreatedBy               string      `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt               time.Time   `gorm:"not null" json:"created_at"`
	UpdatedAt               time.Time   `gorm:"not null" json:"updated_at"`
}

func (GitRepository) TableName() string { return "git_repositories" }

type WebhookDeliveryStatus string

const (
	WebhookReceived  WebhookDeliveryStatus = "received"
	WebhookProcessed WebhookDeliveryStatus = "processed"
	WebhookFailed    WebhookDeliveryStatus = "failed"
)

type GitWebhookDelivery struct {
	ID           string                `gorm:"type:varchar(36);primaryKey"`
	RepositoryID string                `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_git_delivery,priority:1"`
	DeliveryID   string                `gorm:"type:varchar(128);not null;uniqueIndex:idx_git_delivery,priority:2"`
	EventType    string                `gorm:"type:varchar(32);not null;index"`
	Ref          string                `gorm:"type:varchar(512);not null"`
	CommitSHA    string                `gorm:"type:varchar(64);not null;default:''"`
	Message      string                `gorm:"type:varchar(255);not null;default:''"`
	PayloadHash  string                `gorm:"type:varchar(64);not null"`
	Status       WebhookDeliveryStatus `gorm:"type:varchar(16);not null;index"`
	JobID        string                `gorm:"type:varchar(36);not null;default:'';index"`
	ReceivedAt   time.Time             `gorm:"not null;index"`
	ProcessedAt  *time.Time
}

func (GitWebhookDelivery) TableName() string { return "git_webhook_deliveries" }
