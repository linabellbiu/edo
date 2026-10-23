package model

import (
	"time"

	"gorm.io/datatypes"
)

type DNSProvider string

const (
	DNSProviderCloudflare   DNSProvider = "cloudflare"
	DNSProviderAliDNS       DNSProvider = "alidns"
	DNSProviderTencentCloud DNSProvider = "tencentcloud"
	DNSProviderRoute53      DNSProvider = "route53"
	DNSProviderHuaweiCloud  DNSProvider = "huaweicloud"
	DNSProviderAzure        DNSProvider = "azure"
	DNSProviderGoogleCloud  DNSProvider = "googleclouddns"
	DNSProviderDigitalOcean DNSProvider = "digitalocean"
	DNSProviderGandi        DNSProvider = "gandi"
	DNSProviderGoDaddy      DNSProvider = "godaddy"
	DNSProviderNamecheap    DNSProvider = "namecheap"
	DNSProviderHetzner      DNSProvider = "hetzner"
	DNSProviderPowerDNS     DNSProvider = "powerdns"
	DNSProviderRFC2136      DNSProvider = "rfc2136"
)

// DNSProviderAccount 保存 DNS 厂商接入配置。公开配置与加密凭据分开存储，接口不得返回密文。
type DNSProviderAccount struct {
	RowID                  uint64         `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID                     string         `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex"`
	Name                   string         `gorm:"type:varchar(128);not null;uniqueIndex:ux_dns_provider_accounts_department_name,priority:2;uniqueIndex:ux_dns_provider_accounts_project_name,priority:2"`
	Provider               DNSProvider    `gorm:"type:varchar(32);not null;index"`
	PublicConfig           datatypes.JSON `gorm:"type:text;not null"`
	SecretCiphertext       string         `gorm:"type:text;not null"`
	ConfiguredSecretFields datatypes.JSON `gorm:"type:text;not null"`
	IsActive               bool           `gorm:"not null;default:true;index"`
	DepartmentID           string         `gorm:"type:varchar(36);not null;default:'00000000-0000-0000-0000-000000000001';index;uniqueIndex:ux_dns_provider_accounts_department_name,priority:1"`
	ProjectID              string         `gorm:"type:varchar(36);not null;default:'';index;uniqueIndex:ux_dns_provider_accounts_project_name,priority:1" json:"project_id"`
	CreatedBy              string         `gorm:"type:varchar(36);not null;index"`
	CreatedAt              time.Time      `gorm:"not null"`
	UpdatedAt              time.Time      `gorm:"not null"`
}

func (DNSProviderAccount) TableName() string { return "dns_provider_accounts" }

// DNSDomain 是 EDO 管理的权威 DNS Zone 引用；删除该记录不会删除厂商侧 Zone。
type DNSDomain struct {
	RowID        uint64             `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID           string             `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex"`
	AccountID    string             `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_dns_domains_account_name,priority:1"`
	Name         string             `gorm:"type:varchar(253);not null;index;uniqueIndex:idx_dns_domains_account_name,priority:2"`
	Description  string             `gorm:"type:varchar(512);not null;default:''"`
	IsActive     bool               `gorm:"not null;default:true;index"`
	DepartmentID string             `gorm:"type:varchar(36);not null;default:'00000000-0000-0000-0000-000000000001';index"`
	ProjectID    string             `gorm:"type:varchar(36);not null;default:'';index" json:"project_id"`
	CreatedBy    string             `gorm:"type:varchar(36);not null;index"`
	CreatedAt    time.Time          `gorm:"not null"`
	UpdatedAt    time.Time          `gorm:"not null"`
	Account      DNSProviderAccount `gorm:"foreignKey:AccountID;references:ID" json:"-"`
}

func (DNSDomain) TableName() string { return "dns_domains" }
