package k3s

import (
	"context"
	"errors"
	"io"
	"time"

	"edo/internal/model"
	"edo/internal/sshclient"
)

var (
	ErrInvalidInput         = errors.New("K3s 安装配置无效")
	ErrPermission           = errors.New("没有执行 K3s 安装的权限，或当前项目权限已变化")
	ErrNotFound             = errors.New("K3s 安装计划不存在")
	ErrPlanExpired          = errors.New("安装计划已过期，请重新预览")
	ErrPlanConsumed         = errors.New("安装计划已执行，请查看任务结果")
	ErrHostBusy             = errors.New("所选主机正在执行其他安装任务")
	ErrHostChanged          = errors.New("主机连接或所属范围已变化，请重新预览")
	ErrTargetVersionChanged = errors.New("目标集群版本已变化或安装计划缺少版本记录，请重新预览")
	ErrExistingInstallation = errors.New("主机已有 Kubernetes 安装或配置，请接入现有集群")
	ErrRemote               = errors.New("主机安装命令执行失败，请检查主机连接和服务日志")
	ErrReadiness            = errors.New("等待 K3s 节点就绪超时，请检查主机上的 K3s 服务")
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
	targetVersion string
	targetCAData  []byte
	Action        string         `json:"action"`
	Profile       string         `json:"profile"`
	Name          string         `json:"name"`
	ClusterID     string         `json:"cluster_id,omitempty"`
	ServerHostID  string         `json:"server_host_id,omitempty"`
	AgentHostIDs  []string       `json:"agent_host_ids"`
	ServerOptions map[string]any `json:"server_options"`
	AgentOptions  map[string]any `json:"agent_options"`
	Installer     map[string]any `json:"installer"`
	Endpoint      string         `json:"endpoint"`
	Overrides     string         `json:"overrides"`
}

type Host struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Role    string `json:"role"`
	Config  string `json:"config,omitempty"`
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

type Plan struct {
	ID            string           `json:"id"`
	Action        string           `json:"action"`
	Name          string           `json:"name"`
	Status        string           `json:"status"`
	ClusterID     string           `json:"cluster_id,omitempty"`
	Endpoint      string           `json:"endpoint"`
	Version       string           `json:"version"`
	TargetVersion string           `json:"target_version,omitempty"`
	ExpiresAt     time.Time        `json:"expires_at"`
	Hosts         []Host           `json:"hosts"`
	ServerConfig  string           `json:"server_config"`
	AgentConfig   string           `json:"agent_config"`
	Commands      []string         `json:"commands"`
	Warnings      []string         `json:"warnings"`
	Preflight     *PreflightReport `json:"preflight,omitempty"`
}

type Cluster struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Installation struct {
	ID         string     `json:"id"`
	Action     string     `json:"action"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	ClusterID  string     `json:"cluster_id,omitempty"`
	ExpiresAt  time.Time  `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Hosts      []Host     `json:"hosts"`
	Logs       string     `json:"logs"`
	Error      string     `json:"error,omitempty"`
	Cluster    *Cluster   `json:"cluster,omitempty"`
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
