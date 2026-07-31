package model

import "time"

type DockerEndpoint struct {
	ID                      string    `gorm:"type:varchar(36);primaryKey"`
	Name                    string    `gorm:"type:varchar(128);not null;uniqueIndex"`
	HostID                  string    `gorm:"type:varchar(36);not null;default:'';index"`
	Host                    string    `gorm:"type:varchar(1024);not null"`
	TLSCiphertext           string    `gorm:"type:text;not null"`
	SSHCredentialCiphertext string    `gorm:"type:text;not null"`
	SSHHostKeyFingerprint   string    `gorm:"type:varchar(128);not null;default:''"`
	IsActive                bool      `gorm:"not null;default:true;index"`
	CreatedBy               string    `gorm:"type:varchar(36);not null;index"`
	CreatedAt               time.Time `gorm:"not null"`
	UpdatedAt               time.Time `gorm:"not null"`
}

func (DockerEndpoint) TableName() string { return "docker_endpoints" }

type KubernetesMode string

const (
	KubernetesKubeconfig KubernetesMode = "kubeconfig"
)

type KubernetesCluster struct {
	ID                   string         `gorm:"type:varchar(36);primaryKey"`
	Name                 string         `gorm:"type:varchar(128);not null;uniqueIndex"`
	Mode                 KubernetesMode `gorm:"type:varchar(16);not null"`
	APIServer            string         `gorm:"type:varchar(1024);not null;default:''"`
	DefaultNamespace     string         `gorm:"type:varchar(253);not null;default:'default'"`
	KubeconfigCiphertext string         `gorm:"type:text;not null"`
	IsActive             bool           `gorm:"not null;default:true;index"`
	CreatedBy            string         `gorm:"type:varchar(36);not null;index"`
	CreatedAt            time.Time      `gorm:"not null"`
	UpdatedAt            time.Time      `gorm:"not null"`
}

func (KubernetesCluster) TableName() string { return "kubernetes_clusters" }
