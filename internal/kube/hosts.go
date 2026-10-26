package kube

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/model"
)

var ErrInvalidClusterHosts = errors.New("请选择当前项目中的远程主机")

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

// SetHosts 只修改主机与集群的关系。每台主机最多关联一个集群，选中已关联的主机时原子转移关系。
// 空列表解除当前集群的全部主机关联，集群连接与主机的 SSH/Docker 能力均保留。
func (s *Service) SetHosts(ctx context.Context, id string, hostIDs []string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cluster model.KubernetesCluster
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cluster, "uuid = ?", strings.TrimSpace(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrClusterNotFound
			}
			return fmt.Errorf("查询 Kubernetes 集群失败: %w", err)
		}
		return replaceClusterHosts(tx, &cluster, hostIDs)
	})
}

func replaceClusterHosts(tx *gorm.DB, cluster *model.KubernetesCluster, requestedIDs []string) error {
	if len(requestedIDs) > 500 {
		return ErrInvalidClusterHosts
	}
	hostIDs := make([]string, 0, len(requestedIDs))
	for _, requestedID := range requestedIDs {
		id := strings.TrimSpace(requestedID)
		if id == "" || len(id) > 36 {
			return ErrInvalidClusterHosts
		}
		hostIDs = append(hostIDs, id)
	}
	slices.Sort(hostIDs)
	hostIDs = slices.Compact(hostIDs)
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
		}
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
