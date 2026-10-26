package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/model"
)

// AppendHosts verifies and associates only the requested hosts. Existing links
// are left untouched, including links added while remote verification runs.
func (s *Service) AppendHosts(ctx context.Context, clusterID string, hostIDs []string) error {
	ctx, cancel := s.hostValidationContext(ctx)
	defer cancel()
	cluster, err := s.Find(ctx, strings.TrimSpace(clusterID))
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
		// Selection snapshots are ordered by host ID, matching SetHosts' lock
		// order. A credential or scope change after probing must abort the write.
		if err := verifyHostSnapshots(tx, verified); err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, host := range verified {
			var existing model.HostCapability
			err := tx.Where("host_id = ? AND kind = ?", host.ID, model.HostCapabilityKubernetes).First(&existing).Error
			if err == nil && existing.RuntimeID == current.ID {
				continue
			}
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("查询主机集群关联失败: %w", err)
			}
			capability := model.HostCapability{
				HostID: host.ID, Kind: model.HostCapabilityKubernetes, RuntimeID: current.ID,
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
	})
}
