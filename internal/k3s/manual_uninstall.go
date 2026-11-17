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

var reviewedK3dName = regexp.MustCompile(`^edo_cli cluster create '([a-z0-9][a-z0-9-]{0,62})'(?: |$)`)

type ManualUninstallGuide struct {
	ClusterID    string                     `json:"cluster_id"`
	ClusterName  string                     `json:"cluster_name"`
	ResourceName string                     `json:"resource_name,omitempty"`
	Host         *Host                      `json:"host,omitempty"`
	Local        bool                       `json:"local"`
	DockerID     string                     `json:"docker_id,omitempty"`
	Executions   []ManualUninstallExecution `json:"executions"`
}

// ManualUninstall 始终提供通用指引；安装快照只作为可选预填来源。
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
	guide = &ManualUninstallGuide{ClusterID: cluster.ID, ClusterName: cluster.Name, Executions: s.manualUninstallExecutions(ctx)}
	var records []model.K3sInstallation
	if lookupErr := s.db.WithContext(ctx).Where("cluster_id = ? AND project_id = ? AND department_id = ? AND action = ? AND status = ?", cluster.ID, scope.ProjectID, scope.DepartmentID, "create", "succeeded").Limit(2).Find(&records).Error; lookupErr != nil {
		slog.WarnContext(ctx, "读取手动卸载预填记录失败，使用通用指引", "operation", "k3d_manual_uninstall_prefill", "cluster_id", cluster.ID, "err", lookupErr)
		return guide, nil
	}
	// 不从历史列表任取一项，也不把用户的显示名称当作 Docker 中的资源名称。
	if len(records) != 1 {
		return guide, nil
	}
	record := records[0]
	snapshot, decodeErr := s.decode(record)
	if decodeErr != nil {
		slog.ErrorContext(ctx, "读取手动卸载安装快照失败", "operation", "k3d_manual_uninstall_snapshot", "installation_id", record.ID, "err", decodeErr)
		return guide, nil
	}
	var plan Plan
	if json.Unmarshal(record.Preview, &plan) != nil || plan.ID != record.ID || plan.Mode != "k3d" || len(plan.Commands) != 1 {
		return guide, nil
	}
	matched := reviewedK3dName.FindStringSubmatch(plan.Commands[0])
	if len(matched) != 2 {
		return guide, nil
	}
	name := matched[1]
	var hosts []Host
	if snapshot.Input.Mode != "k3d" || snapshot.Input.Action != "create" || snapshot.Input.K3d == nil || json.Unmarshal(record.Hosts, &hosts) != nil || len(hosts) != 1 || hosts[0].ID != snapshot.Input.ServerHostID {
		return guide, nil
	}
	// 只呈现目标身份，不返回快照中的配置、认证信息或终端日志。
	host := Host{ID: hosts[0].ID, Name: hosts[0].Name, Address: hosts[0].Address, Role: "host"}
	guide.ResourceName, guide.Host, guide.Local = name, &host, host.ID == model.BuiltinLocalHostID
	if snapshot.K3dCleanup != nil && snapshot.K3dCleanup.InstallationID == record.ID && snapshot.K3dCleanup.HostID == host.ID {
		guide.DockerID = snapshot.K3dCleanup.DaemonID
	} else if guide.Local && snapshot.LocalK3d != nil {
		guide.DockerID = snapshot.LocalK3d.DaemonID
	}
	return guide, nil
}
