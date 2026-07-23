package model

import "time"

type IdentityProvider struct {
	ID            string `gorm:"type:varchar(36);primaryKey" json:"id"`
	Type          string `gorm:"type:varchar(24);not null;index" json:"type"`
	Name          string `gorm:"type:varchar(64);not null;uniqueIndex" json:"name"`
	DisplayName   string `gorm:"type:varchar(64);not null" json:"display_name"`
	IsActive      bool   `gorm:"not null;index" json:"is_active"`
	AutoCreate    bool   `gorm:"not null;default:false" json:"auto_create"`
	DefaultRoleID string `gorm:"type:varchar(36);not null;default:''" json:"default_role_id"`

	ClientID               string `gorm:"type:varchar(255);not null;default:''" json:"client_id"`
	ClientSecretCiphertext string `gorm:"type:text;not null" json:"-"`
	AuthorizationURL       string `gorm:"type:varchar(1024);not null;default:''" json:"authorization_url"`
	TokenURL               string `gorm:"type:varchar(1024);not null;default:''" json:"token_url"`
	UserInfoURL            string `gorm:"type:varchar(1024);not null;default:''" json:"user_info_url"`
	RedirectURL            string `gorm:"type:varchar(1024);not null;default:''" json:"redirect_url"`
	Scopes                 string `gorm:"type:varchar(512);not null;default:''" json:"scopes"`
	SubjectField           string `gorm:"type:varchar(64);not null;default:''" json:"subject_field"`
	UsernameField          string `gorm:"type:varchar(64);not null;default:''" json:"username_field"`
	NicknameField          string `gorm:"type:varchar(64);not null;default:''" json:"nickname_field"`
	EmailField             string `gorm:"type:varchar(64);not null;default:''" json:"email_field"`
	EmailVerifiedField     string `gorm:"type:varchar(64);not null;default:''" json:"email_verified_field"`

	LDAPURL                    string `gorm:"type:varchar(1024);not null;default:''" json:"ldap_url"`
	LDAPBaseDN                 string `gorm:"type:varchar(512);not null;default:''" json:"ldap_base_dn"`
	LDAPBindDN                 string `gorm:"type:varchar(512);not null;default:''" json:"ldap_bind_dn"`
	LDAPBindPasswordCiphertext string `gorm:"type:text;not null" json:"-"`
	LDAPUserFilter             string `gorm:"type:varchar(512);not null;default:''" json:"ldap_user_filter"`
	LDAPUsernameAttribute      string `gorm:"type:varchar(64);not null;default:''" json:"ldap_username_attribute"`
	LDAPNicknameAttribute      string `gorm:"type:varchar(64);not null;default:''" json:"ldap_nickname_attribute"`
	LDAPEmailAttribute         string `gorm:"type:varchar(64);not null;default:''" json:"ldap_email_attribute"`
	LDAPStartTLS               bool   `gorm:"not null;default:false" json:"ldap_start_tls"`
	AllowInsecure              bool   `gorm:"not null;default:false" json:"allow_insecure"`

	CreatedBy string    `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
}

func (IdentityProvider) TableName() string { return "identity_providers" }

type ExternalIdentity struct {
	ID             string     `gorm:"type:varchar(36);primaryKey" json:"id"`
	ProviderID     string     `gorm:"type:varchar(36);not null;uniqueIndex:idx_external_identity,priority:1;index" json:"provider_id"`
	Subject        string     `gorm:"type:varchar(512);not null;uniqueIndex:idx_external_identity,priority:2" json:"subject"`
	UserID         string     `gorm:"type:varchar(36);not null;index" json:"user_id"`
	RemoteUsername string     `gorm:"type:varchar(255);not null;default:''" json:"remote_username"`
	Email          string     `gorm:"type:varchar(320);not null;default:''" json:"email"`
	LastLoginAt    *time.Time `json:"last_login_at,omitempty"`
	CreatedAt      time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"not null" json:"updated_at"`
}

func (ExternalIdentity) TableName() string { return "external_identities" }
