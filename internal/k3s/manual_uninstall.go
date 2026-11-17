package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

var ErrManualUninstallUnavailable = errors.New("未找到可核对的 k3d 安装记录，无法生成卸载命令")

var reviewedK3dName = regexp.MustCompile(`^edo_cli cluster create '([a-z0-9][a-z0-9-]{0,62})'(?: |$)`)

type ManualUninstallGuide struct {
	ClusterID    string `json:"cluster_id"`
	ClusterName  string `json:"cluster_name"`
	ResourceName string `json:"resource_name"`
	Host         Host   `json:"host"`
	Local        bool   `json:"local"`
	DockerID     string `json:"docker_id,omitempty"`
	Command      string `json:"command"`
	ListCommand  string `json:"list_command"`
}

// ManualUninstall 只读取当前连接与安装快照；主机断连、地址改变均不妨碍取得指引。
func (s *Service) ManualUninstall(ctx context.Context, actorID, clusterID string) (guide *ManualUninstallGuide, err error) {
	defer func() {
		if err != nil {
			slog.WarnContext(ctx, "读取 k3d 手动卸载指引失败", "operation", "k3d_manual_uninstall", "actor_id", actorID, "cluster_id", clusterID, "err", err)
		}
	}()
	if _, err = s.authorizeUninstall(ctx, actorID); err != nil {
		return nil, err
	}
	if _, err = uuid.Parse(clusterID); err != nil {
		return nil, ErrInvalidInput
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	var cluster model.KubernetesCluster
	if err = s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND department_id = ?", clusterID, scope.ProjectID, scope.DepartmentID).First(&cluster).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var records []model.K3sInstallation
	if err = s.db.WithContext(ctx).Where("cluster_id = ? AND project_id = ? AND department_id = ? AND action = ? AND status = ?", cluster.ID, scope.ProjectID, scope.DepartmentID, "create", "succeeded").Limit(2).Find(&records).Error; err != nil {
		return nil, err
	}
	// 不从历史列表任取一项，也不把用户的显示名称当作 Docker 中的资源名称。
	if len(records) != 1 {
		return nil, ErrManualUninstallUnavailable
	}
	record := records[0]
	snapshot, decodeErr := s.decode(record)
	if decodeErr != nil {
		slog.ErrorContext(ctx, "读取手动卸载安装快照失败", "operation", "k3d_manual_uninstall_snapshot", "installation_id", record.ID, "err", decodeErr)
		return nil, ErrManualUninstallUnavailable
	}
	var plan Plan
	if json.Unmarshal(record.Preview, &plan) != nil || plan.ID != record.ID || plan.Mode != "k3d" || len(plan.Commands) != 1 {
		return nil, ErrManualUninstallUnavailable
	}
	matched := reviewedK3dName.FindStringSubmatch(plan.Commands[0])
	if len(matched) != 2 {
		return nil, ErrManualUninstallUnavailable
	}
	name := matched[1]
	var hosts []Host
	if snapshot.Input.Mode != "k3d" || snapshot.Input.Action != "create" || snapshot.Input.K3d == nil || json.Unmarshal(record.Hosts, &hosts) != nil || len(hosts) != 1 || hosts[0].ID != snapshot.Input.ServerHostID {
		return nil, ErrManualUninstallUnavailable
	}
	// 只呈现目标身份，不返回快照中的配置、认证信息或终端日志。
	host := Host{ID: hosts[0].ID, Name: hosts[0].Name, Address: hosts[0].Address, Role: "host"}
	guide = &ManualUninstallGuide{ClusterID: cluster.ID, ClusterName: cluster.Name, ResourceName: name, Host: host, Local: host.ID == model.BuiltinLocalHostID, Command: "k3d cluster delete " + shellQuote(name), ListCommand: "k3d cluster list"}
	if snapshot.K3dCleanup != nil && snapshot.K3dCleanup.InstallationID == record.ID && snapshot.K3dCleanup.HostID == host.ID {
		guide.DockerID = snapshot.K3dCleanup.DaemonID
	} else if guide.Local && snapshot.LocalK3d != nil {
		guide.DockerID = snapshot.LocalK3d.DaemonID
	}
	return guide, nil
}
