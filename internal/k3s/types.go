package k3s

import (
	"context"
	"errors"
	"io"
	"time"

	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/sshclient"
)

var (
	ErrInvalidInput         = errors.New("K3s 安装配置无效")
	ErrPermission           = errors.New("没有执行 K3s 安装的权限，或当前项目权限已变化")
	ErrNotFound             = errors.New("K3s 安装计划不存在")
	ErrPlanExpired          = errors.New("安装计划已过期，请重新预览")
	ErrPlanConsumed         = errors.New("安装计划已执行，请查看任务结果")
	ErrHostBusy             = errors.New("目标主机或集群正在执行其他安装任务")
	ErrHostChanged          = errors.New("主机连接、集群配置或所属范围已变化，请重新预览")
	ErrTargetVersionChanged = errors.New("目标集群版本已变化或安装计划缺少版本记录，请重新预览")
	ErrExistingInstallation = errors.New("主机已有 Kubernetes 安装或配置，请接入现有集群")
	ErrRemote               = errors.New("主机安装命令执行失败，请检查主机连接和服务日志")
	ErrReadiness            = errors.New("等待集群节点就绪超时，请检查安装任务、节点状态和集群连接")
	ErrPreflightFailed      = errors.New("节点前置检查未通过，请处理阻断项后重新检查")
	ErrPreflightRequired    = errors.New("安装计划缺少当前前置检查，请重新检查并预览")
)

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return ErrInvalidInput.Error() + "：" + e.Message }
func (e *ValidationError) Unwrap() error { return ErrInvalidInput }
func ValidationMessage(err error) string {
	var detail *ValidationError
	if errors.As(err, &detail) {
		return detail.Message
	}
	return ""
}

type Input struct {
	targetVersion       string
	targetCAData        []byte
	serverJoin          *kube.ServerJoinInfo
	sources             *Sources
	Action              string         `json:"action"`
	Mode                string         `json:"mode,omitempty"`
	K3d                 *K3dOptions    `json:"k3d,omitempty"`
	DownloadSource      string         `json:"download_source,omitempty"`
	SystemImageRegistry *string        `json:"system_image_registry,omitempty"`
	Profile             string         `json:"profile"`
	Name                string         `json:"name"`
	ClusterID           string         `json:"cluster_id,omitempty"`
	ServerHostID        string         `json:"server_host_id,omitempty"`
	AgentHostIDs        []string       `json:"agent_host_ids"`
	JoinRole            string         `json:"join_role,omitempty"`
	JoinHostIDs         []string       `json:"join_host_ids,omitempty"`
	ServerToken         string         `json:"server_token,omitempty"`
	ServerOptions       map[string]any `json:"server_options"`
	AgentOptions        map[string]any `json:"agent_options"`
	Installer           map[string]any `json:"installer"`
	Endpoint            string         `json:"endpoint"`
	Overrides           string         `json:"overrides"`
}

type Sources struct {
	DownloadSource      string  `json:"download_source"`
	InstallerURL        string  `json:"installer_url"`
	ArtifactURL         string  `json:"artifact_url"`
	Mirror              string  `json:"mirror,omitempty"`
	SystemImageRegistry *string `json:"system_image_registry,omitempty"`
}

type K3dOptions struct {
	Agents      *int   `json:"agents,omitempty"`
	APIPort     int    `json:"api_port"`
	BindAddress string `json:"bind_address"`
}

type Host struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Address           string     `json:"address"`
	Role              string     `json:"role"`
	Config            string     `json:"config,omitempty"`
	Status            string     `json:"status,omitempty"`
	Message           string     `json:"message,omitempty"`
	Stage             string     `json:"stage,omitempty"`
	StageStartedAt    *time.Time `json:"stage_started_at,omitempty"`
	ProgressUpdatedAt *time.Time `json:"progress_updated_at,omitempty"`
}

type Plan struct {
	ID            string             `json:"id"`
	Action        string             `json:"action"`
	Mode          string             `json:"mode"`
	K3d           *K3dOptions        `json:"k3d,omitempty"`
	Sources       *Sources           `json:"sources,omitempty"`
	Name          string             `json:"name"`
	Status        string             `json:"status"`
	ClusterID     string             `json:"cluster_id,omitempty"`
	Endpoint      string             `json:"endpoint"`
	Version       string             `json:"version"`
	TargetVersion string             `json:"target_version,omitempty"`
	JoinRole      string             `json:"join_role,omitempty"`
	ServerJoin    *ServerJoinSummary `json:"server_join,omitempty"`
	ExpiresAt     time.Time          `json:"expires_at"`
	Hosts         []Host             `json:"hosts"`
	ServerConfig  string             `json:"server_config"`
	AgentConfig   string             `json:"agent_config"`
	Commands      []string           `json:"commands"`
	Warnings      []string           `json:"warnings"`
	Preflight     *PreflightReport   `json:"preflight,omitempty"`
}

type Cluster struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ServerJoinSummary struct {
	Datastore   string `json:"datastore"`
	EtcdMembers int    `json:"etcd_members"`
}

type Installation struct {
	ID         string      `json:"id"`
	Action     string      `json:"action"`
	Mode       string      `json:"mode"`
	K3d        *K3dOptions `json:"k3d,omitempty"`
	Sources    *Sources    `json:"sources,omitempty"`
	Name       string      `json:"name"`
	Status     string      `json:"status"`
	ClusterID  string      `json:"cluster_id,omitempty"`
	ExpiresAt  time.Time   `json:"expires_at"`
	CreatedAt  time.Time   `json:"created_at"`
	StartedAt  *time.Time  `json:"started_at,omitempty"`
	FinishedAt *time.Time  `json:"finished_at,omitempty"`
	Hosts      []Host      `json:"hosts"`
	Logs       string      `json:"logs"`
	Error      string      `json:"error,omitempty"`
	Cluster    *Cluster    `json:"cluster,omitempty"`
}

type Authorizer interface {
	HasPermission(context.Context, *model.User, string) (bool, error)
}

// Transport is replaceable by a fake SSH peer in integration tests. Production
// uses the saved host fingerprint and sends scripts/secrets via standard input.
type Transport interface {
	Run(context.Context, model.Host, sshclient.Bundle, string, io.Writer) error
	Read(context.Context, model.Host, sshclient.Bundle, string) (string, error)
}
