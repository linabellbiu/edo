package model

import "time"

type DockerEndpoint struct {
	RowID                   uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID                      string    `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex"`
	Name                    string    `gorm:"type:varchar(128);not null;uniqueIndex:ux_docker_endpoints_department_name,priority:2;uniqueIndex:ux_docker_endpoints_project_name,priority:2"`
	HostID                  string    `gorm:"type:varchar(36);not null;default:'';index"`
	Host                    string    `gorm:"type:varchar(1024);not null"`
	TLSCiphertext           string    `gorm:"type:text;not null"`
	SSHCredentialCiphertext string    `gorm:"type:text;not null"`
	SSHHostKeyFingerprint   string    `gorm:"type:varchar(128);not null;default:''"`
	IsActive                bool      `gorm:"not null;default:true;index"`
	DepartmentID            string    `gorm:"type:varchar(36);not null;default:'00000000-0000-0000-0000-000000000001';index;uniqueIndex:ux_docker_endpoints_department_name,priority:1"`
	ProjectID               string    `gorm:"type:varchar(36);not null;default:'';index;uniqueIndex:ux_docker_endpoints_project_name,priority:1" json:"project_id"`
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
	RowID                uint64         `gorm:"column:id;primaryKey;autoIncrement" json:"row_id"`
	ID                   string         `gorm:"column:uuid;type:varchar(36);not null;uniqueIndex"`
	Name                 string         `gorm:"type:varchar(128);not null;uniqueIndex:ux_kubernetes_clusters_department_name,priority:2;uniqueIndex:ux_kubernetes_clusters_project_name,priority:2"`
	Mode                 KubernetesMode `gorm:"type:varchar(16);not null"`
	APIServer            string         `gorm:"type:varchar(1024);not null;default:''"`
	DefaultNamespace     string         `gorm:"type:varchar(253);not null;default:'default'"`
	KubeconfigCiphertext string         `gorm:"type:text;not null"`
	IsActive             bool           `gorm:"not null;default:true;index"`
	DepartmentID         string         `gorm:"type:varchar(36);not null;default:'00000000-0000-0000-0000-000000000001';index;uniqueIndex:ux_kubernetes_clusters_department_name,priority:1"`
	ProjectID            string         `gorm:"type:varchar(36);not null;default:'';index;uniqueIndex:ux_kubernetes_clusters_project_name,priority:1" json:"project_id"`
	CreatedBy            string         `gorm:"type:varchar(36);not null;index"`
	CreatedAt            time.Time      `gorm:"not null"`
	UpdatedAt            time.Time      `gorm:"not null"`
}

func (KubernetesCluster) TableName() string { return "kubernetes_clusters" }
