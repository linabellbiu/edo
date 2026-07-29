package model

import "time"

type HostMode string

const (
	BuiltinLocalHostID = "zrt-local-host"

	HostModeLocal HostMode = "local"
	HostModeSSH   HostMode = "ssh"
)

type SSHAuthType string

const (
	SSHAuthPassword   SSHAuthType = "password"
	SSHAuthPrivateKey SSHAuthType = "private_key"
)

type HostCapabilityKind string

const (
	HostCapabilitySSH        HostCapabilityKind = "ssh"
	HostCapabilityDocker     HostCapabilityKind = "docker"
	HostCapabilityKubernetes HostCapabilityKind = "kubernetes"
	HostCapabilityLocalExec  HostCapabilityKind = "local_exec"
)

type HostCapabilityStatus string

const (
	HostCapabilityUnchecked   HostCapabilityStatus = "unchecked"
	HostCapabilityReady       HostCapabilityStatus = "ready"
	HostCapabilityUnreachable HostCapabilityStatus = "unreachable"
)

type Environment struct {
	ID          string `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name        string `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Description string `gorm:"type:varchar(500);not null;default:''" json:"description"`
	// Level 仅保留旧数据库列兼容。环境现在只负责基础设施分组，不再具有安全级别。
	// Deprecated: 新记录只写空值，旧值不迁移且不再参与业务或通过接口暴露。
	Level     EnvironmentType `gorm:"type:varchar(16);not null;index" json:"-"`
	IsActive  bool            `gorm:"not null;default:true;index" json:"is_active"`
	CreatedBy string          `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt time.Time       `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time       `gorm:"not null" json:"updated_at"`
}

func (Environment) TableName() string { return "environments" }

// EnvironmentHost 保存环境与主机的多对多关系。环境只是用户定义的基础设施分组，
// 同一台主机可以被多个环境复用；具体部署配置仍需各自避免服务端口冲突。
type EnvironmentHost struct {
	EnvironmentID string    `gorm:"type:varchar(36);primaryKey;index" json:"environment_id"`
	HostID        string    `gorm:"type:varchar(36);primaryKey;index" json:"host_id"`
	CreatedAt     time.Time `gorm:"not null" json:"created_at"`
}

func (EnvironmentHost) TableName() string { return "environment_hosts" }

type Host struct {
	ID                      string      `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name                    string      `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Mode                    HostMode    `gorm:"type:varchar(16);not null;index" json:"mode"`
	Address                 string      `gorm:"type:varchar(1024);not null;default:''" json:"address"`
	SSHPort                 int         `gorm:"not null;default:22" json:"ssh_port"`
	SSHUsername             string      `gorm:"type:varchar(128);not null;default:''" json:"ssh_username"`
	SSHAuthType             SSHAuthType `gorm:"type:varchar(16);not null;default:''" json:"ssh_auth_type"`
	SSHCredentialCiphertext string      `gorm:"type:text;not null" json:"-"`
	SSHHostKeyFingerprint   string      `gorm:"type:varchar(128);not null;default:''" json:"ssh_host_key_fingerprint"`
	// EnvironmentID 仅保留旧数据库列兼容，新代码使用 environment_hosts 关联表。
	// Deprecated: 不再读取或写入该字段的业务语义。
	EnvironmentID string    `gorm:"type:varchar(36);not null;default:'';index" json:"-"`
	IsBuiltin     bool      `gorm:"not null;default:false;index" json:"is_builtin"`
	IsActive      bool      `gorm:"not null;default:true;index" json:"is_active"`
	CreatedBy     string    `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt     time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time `gorm:"not null" json:"updated_at"`
}

func (Host) TableName() string { return "hosts" }

type HostCapability struct {
	HostID    string               `gorm:"type:varchar(36);primaryKey" json:"host_id"`
	Kind      HostCapabilityKind   `gorm:"type:varchar(16);primaryKey" json:"kind"`
	RuntimeID string               `gorm:"type:varchar(36);not null;default:'';index" json:"runtime_id"`
	Status    HostCapabilityStatus `gorm:"type:varchar(16);not null;default:'unchecked';index" json:"status"`
	Version   string               `gorm:"type:varchar(128);not null;default:''" json:"version"`
	UseSudo   bool                 `gorm:"not null;default:false" json:"use_sudo"`
	CreatedAt time.Time            `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time            `gorm:"not null" json:"updated_at"`
}

func (HostCapability) TableName() string { return "host_capabilities" }
