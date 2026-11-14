package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/audit"
	"edo/internal/database"
	"edo/internal/model"
)

func (s *Service) uninstallCluster(ctx context.Context, id string, lock bool) (*model.KubernetesCluster, error) {
	scope, _ := database.DepartmentScopeFromContext(ctx)
	query := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND department_id = ?", id, scope.ProjectID, scope.DepartmentID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var cluster model.KubernetesCluster
	if err := query.First(&cluster).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var exists int64
			// 只确认 UUID 是否仍存在，不能把范围之外的连接当成已删除。
			if err := s.db.WithContext(ctx).Table("kubernetes_clusters").Where("uuid = ?", id).Count(&exists).Error; err != nil {
				return nil, err
			}
			if exists != 0 {
				return nil, ErrUninstallChanged
			}
			return nil, nil
		}
		return nil, err
	}
	return &cluster, nil
}

// CleanupCompletedUninstall 仅补处理已成功卸载的 EDO 记录，绝不连接主机或重放卸载。
func (s *Service) CleanupCompletedUninstall(ctx context.Context, actorID, taskID string) (err error) {
	defer func() {
		if err != nil {
			slog.ErrorContext(ctx, "补处理 K3s 卸载关联记录失败", "operation", "k3s_uninstall_cleanup_completed", "installation_id", taskID, "actor_id", actorID, "err", err)
			err = installationError("uninstall_cleanup", err)
		}
	}()
	if _, err = s.authorizeUninstall(ctx, actorID); err != nil {
		return err
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	var record model.K3sInstallation
	if err = s.db.WithContext(ctx).First(&record, "uuid = ? AND created_by = ? AND project_id = ? AND department_id = ? AND action = ? AND status = ?", taskID, actorID, scope.ProjectID, scope.DepartmentID, "uninstall", "succeeded").Error; err != nil {
		return err
	}
	snapshot, err := s.decode(record)
	if err != nil {
		return err
	}
	message, err := s.cleanupUninstallRecords(ctx, record, snapshot)
	if err != nil {
		return err
	}
	logs := appendInstallationLog(record.Logs, time.Now().Format("15:04:05")+" "+message+"\n")
	return s.db.WithContext(ctx).Model(&model.K3sInstallation{}).Where("uuid = ? AND status = ?", record.ID, "succeeded").Update("logs", logs).Error
}

func (s *Service) cleanupUninstallRecords(ctx context.Context, record model.K3sInstallation, snapshot savedInput) (message string, err error) {
	defer func() {
		if err != nil {
			slog.ErrorContext(ctx, "移除 K3s 卸载关联记录失败", "operation", "k3s_uninstall_cleanup", "installation_id", record.ID, "cluster_id", record.ClusterID, "err", err)
		}
	}()
	if _, err = s.authorizeUninstall(ctx, record.CreatedBy); err != nil {
		return "", err
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	if record.ProjectID != scope.ProjectID || record.DepartmentID != scope.DepartmentID || snapshot.Uninstall == nil || snapshot.Uninstall.TaskID != record.ID {
		return "", ErrUninstallChanged
	}
	removed := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var project model.Project
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&project, "uuid = ?", record.ProjectID).Error; err != nil {
			return err
		}
		var current model.K3sInstallation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "uuid = ? AND project_id = ? AND department_id = ? AND created_by = ?", record.ID, record.ProjectID, record.DepartmentID, record.CreatedBy).Error; err != nil {
			return err
		}
		var hosts []Host
		if json.Unmarshal(current.Hosts, &hosts) != nil || len(hosts) != 1 || hosts[0].ID != snapshot.Uninstall.Summary.HostID || current.Action != "uninstall" || current.InputCiphertext != record.InputCiphertext || current.ClusterID != record.ClusterID {
			return ErrUninstallChanged
		}
		// 只有已持久化的官方成功结果，或当前任务进入结果验证之后的清理阶段，才能移除记录。
		completed := current.Status == "succeeded" && hosts[0].Status == "succeeded" && current.FinishedAt != nil
		running := current.Status == "running" && current.LeaseOwner == s.owner && hosts[0].Stage == "uninstall_cleanup"
		if !completed && !running {
			return ErrUninstallIncomplete
		}
		validation := *s
		validation.db = tx
		if _, err := validation.authorizeUninstall(ctx, record.CreatedBy); err != nil {
			return err
		}
		source, summary, err := validation.uninstallSource(ctx, UninstallInput{InstallationID: snapshot.Uninstall.Summary.SourceInstallationID, HostID: hosts[0].ID})
		if err != nil {
			return err
		}
		if summary != snapshot.Uninstall.Summary || uninstallSourceDigest(source) != snapshot.Uninstall.SourceDigest || source.ClusterID != current.ClusterID {
			return ErrUninstallChanged
		}
		if current.ClusterID == "" {
			return nil
		}
		cluster, err := validation.uninstallCluster(ctx, current.ClusterID, true)
		if err != nil {
			return err
		}
		if connection := snapshot.Uninstall.Connection; connection != nil {
			if connection.ID != current.ClusterID || cluster != nil && clusterDigest(cluster) != connection.Digest {
				return ErrUninstallChanged
			}
		} else if cluster != nil && (!completed || cluster.UpdatedAt.After(*current.FinishedAt)) {
			// 旧成功任务没有连接快照；确认连接在卸载结束后未被重新配置，才能补处理。
			return ErrUninstallChanged
		}
		var host model.Host
		if err := tx.First(&host, "uuid = ? AND project_id = ? AND department_id = ?", hosts[0].ID, record.ProjectID, record.DepartmentID).Error; err != nil {
			return err
		}
		var link model.HostCapability
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("host_id = ? AND kind = ?", host.ID, model.HostCapabilityKubernetes).First(&link).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil && (link.RuntimeID != current.ClusterID || completed && link.UpdatedAt.After(*current.FinishedAt)) {
			return ErrUninstallChanged
		}
		var active int64
		if err := tx.Model(&model.K3sInstallationHostLock{}).Where("host_id = ? AND installation_id <> ?", host.ID, current.ID).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return ErrHostBusy
		}
		if err := tx.Model(&model.K3sInstallation{}).Where("cluster_id = ? AND action IN ? AND status IN ?", current.ClusterID, []string{"create", "join"}, []string{"pending", "running"}).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return ErrHostBusy
		}
		detached := tx.Where("host_id = ? AND kind = ? AND runtime_id = ?", host.ID, model.HostCapabilityKubernetes, current.ClusterID).Delete(&model.HostCapability{})
		if detached.Error != nil {
			return detached.Error
		}
		var remaining int64
		if err := tx.Model(&model.HostCapability{}).Where("kind = ? AND runtime_id = ?", model.HostCapabilityKubernetes, current.ClusterID).Count(&remaining).Error; err != nil {
			return err
		}
		if remaining == 0 && cluster != nil {
			for _, value := range []any{&model.KubernetesRBACSubject{}, &model.KubernetesRoleGrant{}, &model.KubernetesRBACPolicy{}} {
				if err := tx.Where("cluster_id = ? AND project_id = ? AND department_id = ?", cluster.ID, record.ProjectID, record.DepartmentID).Delete(value).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("uuid = ? AND project_id = ? AND department_id = ?", cluster.ID, record.ProjectID, record.DepartmentID).Delete(&model.KubernetesCluster{}).Error; err != nil {
				return err
			}
			removed = true
		}
		if detached.RowsAffected > 0 || removed {
			return audit.NewService(tx).Record(ctx, audit.RecordInput{ActorUserID: record.CreatedBy, Action: "kubernetes.k3s.uninstall.cleanup", ResourceType: "k3s_installation", ResourceID: record.ID, Result: model.AuditSucceeded, Metadata: map[string]any{"host_id": host.ID, "cluster_id": current.ClusterID, "connection_removed": removed}})
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if removed {
		if cache, ok := s.kubernetes.(interface{ InvalidateClusterConnection(string) }); ok {
			cache.InvalidateClusterConnection(record.ClusterID)
		}
		return "已移除此主机的集群关联及集群连接，安装与卸载历史已保留。", nil
	}
	return "已移除此主机的集群关联，安装与卸载历史已保留。", nil
}
