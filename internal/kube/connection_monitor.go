package kube

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"edo/internal/database"
	"edo/internal/model"
)

const connectionMonitorInterval = 2 * time.Second

type connectionMonitorContextKey struct{}

type connectionMonitorTarget struct {
	cluster   *model.KubernetesCluster
	subject   *model.KubernetesRBACSubject
	key       string
	checked   time.Time
	requested bool
}

// RunConnectionMonitoring 检测已保存的连接和已同步的原生授权身份。
// 扫描与探测都随服务运行，不依赖浏览器读取或登录。
func (s *Service) RunConnectionMonitoring(ctx context.Context) error {
	ticker := time.NewTicker(connectionMonitorInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		s.refreshConnectionMonitoring(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}

func (s *Service) refreshConnectionMonitoring(ctx context.Context) {
	readCtx, cancel := context.WithTimeout(ctx, connectionCheckTimeout)
	defer cancel()
	var clusters []model.KubernetesCluster
	if err := s.db.WithContext(readCtx).Where("is_active = ?", true).
		Where("EXISTS (SELECT 1 FROM projects WHERE projects.uuid = kubernetes_clusters.project_id AND projects.department_id = kubernetes_clusters.department_id AND projects.is_active = ?)", true).
		Order("id ASC").Find(&clusters).Error; err != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "读取待检测的集群失败", "operation", "kubernetes_connection_monitor_clusters", "err", err)
		}
		return
	}

	var subjects []model.KubernetesRBACSubject
	if len(clusters) != 0 {
		if err := s.db.WithContext(readCtx).
			Where("status = ?", "synced").
			Where("EXISTS (SELECT 1 FROM kubernetes_rbac_policies AS policy WHERE policy.cluster_id = kubernetes_rbac_subjects.cluster_id AND policy.revision = kubernetes_rbac_subjects.revision AND policy.enabled = ? AND policy.desired_enabled = ? AND policy.status = ?)", true, true, "synced").
			Order("id ASC").Find(&subjects).Error; err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "读取待检测的集群授权失败", "operation", "kubernetes_connection_monitor_subjects", "err", err)
		}
	}

	cache := s.connections
	byID := make(map[string]*model.KubernetesCluster, len(clusters))
	activeKeys := make(map[string]bool, len(clusters))
	due := make([]connectionMonitorTarget, 0, len(clusters))
	now := time.Now().UTC()
	addDue := func(cluster *model.KubernetesCluster, subject *model.KubernetesRBACSubject, key string, entry *connectionStatusEntry) {
		if entry != nil && entry.checking {
			if now.Sub(entry.startedAt) <= connectionCheckStaleAfter {
				return
			}
			cache.abandonCheck(entry, now)
		}
		target := connectionMonitorTarget{cluster: cluster, subject: subject, key: key}
		if entry != nil {
			if !entry.requested && entry.value.ExpiresAt != nil && now.Before(*entry.value.ExpiresAt) {
				return
			}
			target.requested = entry.requested
			if entry.value.CheckedAt != nil {
				target.checked = *entry.value.CheckedAt
			}
		}
		due = append(due, target)
	}
	cache.mu.Lock()
	for key, until := range cache.subjectRetries {
		if !now.Before(until) {
			delete(cache.subjectRetries, key)
		}
	}
	for i := range clusters {
		cluster := &clusters[i]
		byID[cluster.ID] = cluster
		key := connectionStatusKey(cluster, nil)
		activeKeys[key] = true
		addDue(cluster, nil, key, cache.monitorEntries[key])
	}
	for key, entry := range cache.monitorEntries {
		// 扫描开始后人工刷新可能已写入新目标，下轮再核对该快照。
		if !activeKeys[key] && now.Sub(entry.lastUsed) > connectionMonitorInterval {
			if entry.cancel != nil {
				entry.cancel()
			}
			cache.checks.release(entry.lease)
			delete(cache.monitorEntries, key)
		}
	}
	for i := range subjects {
		subject := &subjects[i]
		cluster := byID[subject.ClusterID]
		if cluster == nil || cluster.ProjectID != subject.ProjectID || cluster.DepartmentID != subject.DepartmentID {
			continue
		}
		// 同步快照只用于查找候选及排序，探测前仍复核真实授权。
		authorization := &clusterAuthorization{CacheKey: subject.ProjectID + "/" + subject.UserID, Version: subject.PolicyHash}
		key := connectionStatusKey(cluster, authorization)
		if _, delayed := cache.subjectRetries[key]; !delayed {
			addDue(cluster, subject, key, cache.entries[key])
		}
	}
	cache.mu.Unlock()

	sort.SliceStable(due, func(i, j int) bool {
		if due[i].requested != due[j].requested {
			return due[i].requested
		}
		return due[i].checked.Before(due[j].checked)
	})
	available := maxConnectionChecks - cache.checks.activeCount()
	if available <= 0 || ctx.Err() != nil {
		return
	}
	probeCtx := context.WithValue(ctx, connectionMonitorContextKey{}, ctx)
	actors := map[database.DepartmentScope][]connectionMonitorTarget{}
	for _, target := range due[:min(len(due), available)] {
		if target.subject == nil {
			s.cachedConnectionStatus(probeCtx, target.cluster, nil, false)
			continue
		}
		subject := target.subject
		scope := database.DepartmentScope{ProjectID: subject.ProjectID, DepartmentID: subject.DepartmentID, UserID: subject.UserID}
		actors[scope] = append(actors[scope], target)
	}
	for scope, targets := range actors {
		ids := make([]string, 0, len(targets))
		for _, target := range targets {
			ids = append(ids, target.cluster.ID)
		}
		current, auth, err := s.loadConnectionTargets(database.WithDepartmentScope(readCtx, scope), ids)
		started := make(map[string]bool, len(current))
		if err == nil {
			for i := range current {
				cluster := &current[i]
				decision := auth[cluster.ID]
				if cluster.IsActive && decision.err == nil && decision.authorization.enforced() {
					s.cachedConnectionStatus(probeCtx, cluster, decision.authorization, false)
					started[connectionStatusKey(cluster, decision.authorization)] = true
				}
			}
		}
		// 撤权或待同步的旧身份不应在每轮排到最前面，占用其他集群的检测机会。
		cache.mu.Lock()
		for _, target := range targets {
			if !started[target.key] {
				cache.subjectRetries[target.key] = now.Add(connectionStatusTTL)
			}
		}
		cache.mu.Unlock()
	}
}
