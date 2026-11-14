package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/model"
)

var ErrInvalidClusterHosts = errors.New("请选择当前项目中的远程主机")

var ErrHostClusterOccupied = errors.New("所选主机已关联其他集群，一台主机只能属于一个集群")

// RequireHostClusterAvailable 只核对 EDO 已保存的归属，不访问远程主机；调用方先验证主机数据范围。
func RequireHostClusterAvailable(ctx context.Context, db *gorm.DB, hostIDs []string, clusterID string) error {
	if len(hostIDs) == 0 {
		return nil
	}
	var occupied int64
	err := db.WithContext(ctx).Model(&model.HostCapability{}).Where("host_id IN ? AND kind = ? AND runtime_id <> ? AND runtime_id <> ?", hostIDs, model.HostCapabilityKubernetes, "", clusterID).Count(&occupied).Error
	if err != nil {
		slog.ErrorContext(ctx, "查询主机集群归属失败", "operation", "host_cluster_availability", "host_ids", hostIDs, "cluster_id", clusterID, "err", err)
		return ErrClusterHostCheckUnavailable
	}
	if occupied > 0 {
		slog.WarnContext(ctx, "主机已被其他集群占用", "operation", "host_cluster_availability", "host_ids", hostIDs, "cluster_id", clusterID)
		return ErrHostClusterOccupied
	}
	// k3d 宿主机不是容器节点，不能靠节点关联表达归属；复用仍存在连接的安装记录。
	var installations []model.K3sInstallation
	err = db.WithContext(ctx).Select("uuid", "cluster_id", "hosts").
		Where("action = ? AND cluster_id <> ? AND cluster_id IN (?)", "create", clusterID, db.WithContext(ctx).Model(&model.KubernetesCluster{}).Select("uuid")).
		Where(datatypes.JSONQuery("preview").Equals("k3d", "mode")).Find(&installations).Error
	if err != nil {
		slog.ErrorContext(ctx, "查询 k3d 安装主机归属失败", "operation", "host_cluster_availability", "host_ids", hostIDs, "err", err)
		return ErrClusterHostCheckUnavailable
	}
	for _, installation := range installations {
		var hosts []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(installation.Hosts, &hosts); err != nil {
			slog.ErrorContext(ctx, "读取 k3d 安装主机归属失败", "operation", "host_cluster_availability", "installation_id", installation.ID, "err", err)
			return ErrClusterHostCheckUnavailable
		}
		for _, host := range hosts {
			if slices.Contains(hostIDs, host.ID) {
				slog.WarnContext(ctx, "主机已有 k3d 集群", "operation", "host_cluster_availability", "host_id", host.ID, "cluster_id", installation.ClusterID)
				return ErrHostClusterOccupied
			}
		}
	}
	return nil
}

// LinkedHost 只返回关联主机的展示字段，不包含 SSH 凭据。
type LinkedHost struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	IsActive bool   `json:"is_active"`
}

// LinkedHosts 批量读取当前数据范围内的主机关联，不连接 SSH 或 Kubernetes API。
func (s *Service) LinkedHosts(ctx context.Context, clusterIDs []string) (map[string][]LinkedHost, error) {
	result := make(map[string][]LinkedHost, len(clusterIDs))
	for _, id := range clusterIDs {
		result[id] = []LinkedHost{}
	}
	if len(clusterIDs) == 0 {
		return result, nil
	}
	var capabilities []model.HostCapability
	if err := s.db.WithContext(ctx).Where("kind = ? AND runtime_id IN ?", model.HostCapabilityKubernetes, clusterIDs).
		Find(&capabilities).Error; err != nil {
		return nil, fmt.Errorf("查询集群主机关联失败: %w", err)
	}
	if len(capabilities) == 0 {
		return result, nil
	}
	hostIDs := make([]string, 0, len(capabilities))
	clusterByHost := make(map[string]string, len(capabilities))
	for _, capability := range capabilities {
		hostIDs = append(hostIDs, capability.HostID)
		clusterByHost[capability.HostID] = capability.RuntimeID
	}
	var hosts []model.Host
	if err := s.db.WithContext(ctx).Where("uuid IN ?", hostIDs).Order("name ASC").Order("uuid ASC").Find(&hosts).Error; err != nil {
		return nil, fmt.Errorf("查询关联主机失败: %w", err)
	}
	for _, host := range hosts {
		clusterID := clusterByHost[host.ID]
		result[clusterID] = append(result[clusterID], LinkedHost{ID: host.ID, Name: host.Name, Address: host.Address, IsActive: host.IsActive})
	}
	return result, nil
}

// SetHosts 只修改主机与集群的关系，不能覆盖主机已有的其他集群归属。
// 空列表解除当前集群的全部主机关联，集群连接与主机的 SSH/Docker 能力均保留。
func (s *Service) SetHosts(ctx context.Context, id string, hostIDs []string) error {
	ctx, cancel := s.hostValidationContext(ctx)
	defer cancel()
	cluster, err := s.Find(ctx, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	verified, err := s.validateHostSelection(ctx, cluster, hostIDs, nil)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.KubernetesCluster
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "uuid = ?", cluster.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrClusterNotFound
			}
			return fmt.Errorf("查询 Kubernetes 集群失败: %w", err)
		}
		if !sameClusterConnection(*cluster, current) {
			return ErrClusterHostCheckUnavailable
		}
		return replaceClusterHosts(tx, &current, hostIDs, verified)
	})
}

func replaceClusterHosts(tx *gorm.DB, cluster *model.KubernetesCluster, requestedIDs []string, verified []model.Host) error {
	hostIDs, err := normalizedHostIDs(requestedIDs)
	if err != nil {
		return err
	}
	if len(hostIDs) > 0 {
		var hosts []model.Host
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("uuid IN ?", hostIDs).
			Order("uuid ASC").Find(&hosts).Error; err != nil {
			return fmt.Errorf("查询待关联主机失败: %w", err)
		}
		if len(hosts) != len(hostIDs) {
			return ErrInvalidClusterHosts
		}
		for _, host := range hosts {
			if host.IsBuiltin || host.Mode != model.HostModeSSH || host.ProjectID != cluster.ProjectID || host.DepartmentID != cluster.DepartmentID {
				return ErrInvalidClusterHosts
			}
			matched := slices.IndexFunc(verified, func(snapshot model.Host) bool { return snapshot.ID == host.ID })
			if matched < 0 || !sameHostConnection(verified[matched], host) {
				return ErrClusterHostCheckUnavailable
			}
		}
	}
	if err := RequireHostClusterAvailable(tx.Statement.Context, tx, hostIDs, cluster.ID); err != nil {
		return err
	}
	removed := tx.Where("kind = ? AND runtime_id = ?", model.HostCapabilityKubernetes, cluster.ID)
	if len(hostIDs) > 0 {
		removed = removed.Where("host_id NOT IN ?", hostIDs)
	}
	if err := removed.Delete(&model.HostCapability{}).Error; err != nil {
		return fmt.Errorf("解除集群主机关联失败: %w", err)
	}
	now := time.Now().UTC()
	for _, hostID := range hostIDs {
		var existing model.HostCapability
		err := tx.Where("host_id = ? AND kind = ?", hostID, model.HostCapabilityKubernetes).First(&existing).Error
		if err == nil && existing.RuntimeID == cluster.ID {
			continue
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("查询主机集群关联失败: %w", err)
		}
		capability := model.HostCapability{
			HostID: hostID, Kind: model.HostCapabilityKubernetes, RuntimeID: cluster.ID,
			Status: model.HostCapabilityUnchecked, Version: "", UseSudo: false, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "host_id"}, {Name: "kind"}},
			DoUpdates: clause.AssignmentColumns([]string{"runtime_id", "status", "version", "use_sudo", "updated_at"}),
		}).Create(&capability).Error; err != nil {
			return fmt.Errorf("保存集群主机关联失败: %w", err)
		}
	}
	return nil
}
