package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

const (
	connectionStatusTTL        = 30 * time.Second
	connectionCheckTimeout     = 5 * time.Second
	maxConnectionChecks        = 4
	maxConnectionStatusEntries = 512
)

var ErrConnectionStatusLimit = errors.New("每次最多检查 500 个集群，请缩小选择范围后重试")

// ConnectionStatus 表示当前操作者的 API 连通性，不代替部署或资源操作授权。
type ConnectionStatus struct {
	ClusterID string     `json:"cluster_id"`
	Status    string     `json:"status"`
	Reason    string     `json:"reason"`
	Message   string     `json:"message"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type connectionStatusEntry struct {
	value    ConnectionStatus
	checking bool
	lastUsed time.Time
	cancel   context.CancelFunc
}

type connectionStatusCache struct {
	mu      sync.Mutex
	entries map[string]*connectionStatusEntry
	slots   chan struct{}
}

func newConnectionStatusCache() *connectionStatusCache {
	return &connectionStatusCache{entries: map[string]*connectionStatusEntry{}, slots: make(chan struct{}, maxConnectionChecks)}
}

type connectionAuthorization struct {
	authorization *clusterAuthorization
	err           error
}

// ConnectionStatuses 批量读取当前权限和快照，网络检查独立、有界运行。
// 请求不等待远程集群；单个慢集群不会阻塞其他状态或集群列表。
func (s *Service) ConnectionStatuses(ctx context.Context, ids []string, refresh bool) ([]ConnectionStatus, error) {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.UserID == "" || scope.ProjectID == "" || scope.DepartmentID == "" {
		slog.WarnContext(ctx, "查询集群连接状态缺少操作者范围", "operation", "kubernetes_connection_status", "err", ErrRBACForbidden)
		return nil, ErrRBACForbidden
	}
	var clusters []model.KubernetesCluster
	var authorizations map[string]connectionAuthorization
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fresh, err := access.FreshProjectPermissions(tx, ctx, scope.UserID, scope.ProjectID)
		if err != nil {
			slog.WarnContext(ctx, "核验集群连接状态读取权限失败", "operation", "kubernetes_connection_status_authorization", "user_id", scope.UserID, "project_id", scope.ProjectID, "err", err)
			return ErrRBACForbidden
		}
		if !canReadConnectionStatuses(fresh) {
			return ErrRBACForbidden
		}
		query := tx.Where("project_id = ? AND department_id = ?", scope.ProjectID, scope.DepartmentID).Order("name ASC")
		if len(ids) > 0 {
			query = query.Where("uuid IN ?", ids)
		}
		if err := query.Limit(501).Find(&clusters).Error; err != nil {
			return err
		}
		if len(ids) > 500 || len(clusters) > 500 {
			return ErrConnectionStatusLimit
		}
		authorizations, err = connectionAuthorizations(tx, clusters, fresh)
		return err
	})
	if err != nil {
		slog.WarnContext(ctx, "查询集群连接状态失败", "operation", "kubernetes_connection_status", "user_id", scope.UserID, "project_id", scope.ProjectID, "err", err)
		return nil, err
	}
	result := make([]ConnectionStatus, len(clusters))
	// 等待槽位的目标优先于已完成结果的刷新，避免慢集群持续挤占尾部目标。
	for _, i := range s.prioritizeConnectionChecks(clusters, authorizations) {
		cluster := &clusters[i]
		if !cluster.IsActive {
			result[i] = ConnectionStatus{ClusterID: cluster.ID, Status: "unknown", Reason: "disabled", Message: "集群已停用"}
			continue
		}
		auth := authorizations[cluster.ID]
		if auth.err != nil {
			slog.WarnContext(ctx, "集群连接状态授权不可用", "operation", "kubernetes_connection_status_cluster_authorization", "cluster_id", cluster.ID, "user_id", scope.UserID, "err", auth.err)
			result[i] = connectionFailureStatus(cluster.ID, auth.err)
			continue
		}
		result[i] = s.cachedConnectionStatus(ctx, cluster, auth.authorization, refresh)
	}
	return result, nil
}

func connectionStatusKey(cluster *model.KubernetesCluster, authorization *clusterAuthorization) string {
	return fmt.Sprintf("%s/%x", authorization.CacheKey, authorizedQueryVersion(cluster, authorization, false))
}

func (s *Service) prioritizeConnectionChecks(clusters []model.KubernetesCluster, authorizations map[string]connectionAuthorization) []int {
	order := make([]int, len(clusters))
	for i := range order {
		order[i] = i
	}
	if s.connections == nil {
		return order
	}
	s.connections.mu.Lock()
	checked := make([]time.Time, len(clusters))
	for i := range clusters {
		cluster := &clusters[i]
		auth := authorizations[cluster.ID]
		if !cluster.IsActive || auth.err != nil || auth.authorization == nil {
			continue
		}
		entry := s.connections.entries[connectionStatusKey(cluster, auth.authorization)]
		if entry != nil && entry.value.CheckedAt != nil {
			checked[i] = *entry.value.CheckedAt
		}
	}
	s.connections.mu.Unlock()
	// 从未完成的检查优先；随后按真实检查时间调度，刷新排队不能重置其顺序。
	sort.SliceStable(order, func(i, j int) bool { return checked[order[i]].Before(checked[order[j]]) })
	return order
}

func canReadConnectionStatuses(fresh *access.FreshProjectAuthorization) bool {
	for _, permission := range []string{access.PermissionClusterRead, "kubernetes.node.read", "kubernetes.workload.read", "kubernetes.config.read", "kubernetes.storage.read", "kubernetes.network.read", "kubernetes.rbac.read", "kubernetes.rbac.update"} {
		if fresh.Permissions[permission] {
			return true
		}
	}
	return false
}

// 策略、授权及同步结果均按集群批量读取，禁止在集群循环里查询数据库。
func connectionAuthorizations(tx *gorm.DB, clusters []model.KubernetesCluster, fresh *access.FreshProjectAuthorization) (map[string]connectionAuthorization, error) {
	result := make(map[string]connectionAuthorization, len(clusters))
	ids := make([]string, 0, len(clusters))
	for _, cluster := range clusters {
		if cluster.IsActive {
			ids = append(ids, cluster.ID)
		}
	}
	if len(ids) == 0 {
		return result, nil
	}
	policies := []model.KubernetesRBACPolicy{}
	grants := []model.KubernetesRoleGrant{}
	subjects := []model.KubernetesRBACSubject{}
	if !fresh.User.IsSuperuser {
		if err := tx.Where("cluster_id IN ?", ids).Find(&policies).Error; err != nil {
			return nil, err
		}
		if err := tx.Where("cluster_id IN ? AND role_id = ?", ids, fresh.RoleID).Find(&grants).Error; err != nil {
			return nil, err
		}
		if err := tx.Where("cluster_id IN ? AND user_id = ?", ids, fresh.User.ID).Find(&subjects).Error; err != nil {
			return nil, err
		}
	}
	policyByCluster := make(map[string]model.KubernetesRBACPolicy, len(policies))
	grantByCluster := make(map[string][]model.KubernetesRoleGrant, len(grants))
	subjectByCluster := make(map[string]model.KubernetesRBACSubject, len(subjects))
	for _, policy := range policies {
		policyByCluster[policy.ClusterID] = policy
	}
	for _, grant := range grants {
		grantByCluster[grant.ClusterID] = append(grantByCluster[grant.ClusterID], grant)
	}
	for _, subject := range subjects {
		subjectByCluster[subject.ClusterID] = subject
	}
	for i := range clusters {
		cluster := &clusters[i]
		if !cluster.IsActive {
			continue
		}
		policy, exists := policyByCluster[cluster.ID]
		if !exists {
			policy = model.KubernetesRBACPolicy{ClusterID: cluster.ID, ProjectID: cluster.ProjectID, DepartmentID: cluster.DepartmentID, Status: "disabled"}
		}
		plan, err := subjectPlanFromSnapshot(cluster, policy, grantByCluster[cluster.ID], fresh)
		if err != nil {
			result[cluster.ID] = connectionAuthorization{err: err}
			continue
		}
		if plan.authorization.enforced() {
			subject := subjectByCluster[cluster.ID]
			if policy.Status != "synced" || subject.Status != "synced" || subject.Revision != policy.Revision || subject.PolicyHash != plan.authorization.Version {
				result[cluster.ID] = connectionAuthorization{err: ErrRBACSyncPending}
				continue
			}
		}
		result[cluster.ID] = connectionAuthorization{authorization: plan.authorization}
	}
	return result, nil
}

func (s *Service) cachedConnectionStatus(ctx context.Context, cluster *model.KubernetesCluster, authorization *clusterAuthorization, refresh bool) ConnectionStatus {
	cache := s.connections
	if cache == nil || s.queryTransaction {
		return ConnectionStatus{ClusterID: cluster.ID, Status: "unknown", Reason: "not_checked", Message: "连接尚未检查"}
	}
	key := connectionStatusKey(cluster, authorization)
	now := time.Now().UTC()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry := cache.entries[key]
	if entry != nil {
		entry.lastUsed = now
		refreshAllowed := refresh && entry.value.CheckedAt != nil && now.Sub(*entry.value.CheckedAt) >= 2*time.Second
		if entry.checking || !refreshAllowed && entry.value.ExpiresAt != nil && now.Before(*entry.value.ExpiresAt) {
			return entry.value
		}
	}
	if entry == nil {
		cache.trim(now)
		if len(cache.entries) >= maxConnectionStatusEntries {
			return ConnectionStatus{ClusterID: cluster.ID, Status: "checking", Reason: "queued", Message: "等待检查连接"}
		}
		entry = &connectionStatusEntry{lastUsed: now}
		cache.entries[key] = entry
	}
	entry.value = ConnectionStatus{ClusterID: cluster.ID, Status: "checking", Reason: "queued", Message: "等待检查连接", CheckedAt: entry.value.CheckedAt}
	// 已占满时由下一次批量读取重试，不创建等待队列或无限 goroutine。
	select {
	case cache.slots <- struct{}{}:
	default:
		return entry.value
	}
	parent := context.WithoutCancel(ctx)
	if s.queries != nil {
		s.queries.mu.Lock()
		if s.queries.ctx != nil {
			parent = s.queries.ctx
		}
		s.queries.mu.Unlock()
	}
	checkContext, cancel := context.WithTimeout(parent, connectionCheckTimeout)
	entry.cancel, entry.checking = cancel, true
	entry.value.Reason, entry.value.Message = "checking", "正在检查连接"
	clusterSnapshot := *cluster
	authorizationSnapshot := cloneClusterAuthorization(authorization)
	go func() {
		defer cancel()
		defer func() { <-cache.slots }()
		value := s.checkConnection(checkContext, &clusterSnapshot, authorizationSnapshot)
		checked := time.Now().UTC()
		expires := checked.Add(connectionStatusTTL)
		value.CheckedAt, value.ExpiresAt = &checked, &expires
		cache.mu.Lock()
		defer cache.mu.Unlock()
		if cache.entries[key] == entry {
			entry.value, entry.checking, entry.cancel = value, false, nil
		}
	}()
	return entry.value
}

// 调用者已持锁；只清理完成的结果，不取消其他操作者仍在进行的检查。
func (c *connectionStatusCache) trim(now time.Time) {
	oldestKey := ""
	var oldest time.Time
	for key, entry := range c.entries {
		if entry.checking {
			continue
		}
		if now.Sub(entry.lastUsed) > queryIdleTimeout {
			delete(c.entries, key)
			continue
		}
		if oldestKey == "" || entry.lastUsed.Before(oldest) {
			oldestKey, oldest = key, entry.lastUsed
		}
	}
	if len(c.entries) >= maxConnectionStatusEntries && oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

func (s *Service) invalidateConnectionStatuses(id string) {
	if s.connections == nil {
		return
	}
	s.connections.mu.Lock()
	defer s.connections.mu.Unlock()
	for key, entry := range s.connections.entries {
		if entry.value.ClusterID == id {
			if entry.cancel != nil {
				entry.cancel()
			}
			delete(s.connections.entries, key)
		}
	}
}

func (s *Service) checkConnection(ctx context.Context, cluster *model.KubernetesCluster, authorization *clusterAuthorization) ConnectionStatus {
	err := func() error {
		cfg, err := s.restConfigForCluster(cluster)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidCluster, err)
		}
		if authorization.enforced() {
			cfg.Impersonate = rest.ImpersonationConfig{UserName: authorization.Principal}
		}
		client, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return err
		}
		body, err := client.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
		if err != nil {
			return err
		}
		var info version.Info
		if err := json.Unmarshal(body, &info); err != nil {
			return err
		}
		if strings.TrimSpace(info.GitVersion) == "" {
			return errors.New("集群版本响应缺少版本字段")
		}
		return nil
	}()
	if err != nil {
		slog.WarnContext(ctx, "检查 Kubernetes 集群连接失败", "operation", "kubernetes_connection_check", "cluster_id", cluster.ID, "project_id", cluster.ProjectID, "err", err)
		return connectionFailureStatus(cluster.ID, err)
	}
	return ConnectionStatus{ClusterID: cluster.ID, Status: "connected", Reason: "connected", Message: "连接正常"}
}

func connectionFailureStatus(clusterID string, err error) ConnectionStatus {
	value := ConnectionStatus{ClusterID: clusterID, Status: "unreachable", Reason: "server_error", Message: "集群 API 暂不可用，请稍后重试"}
	switch nodeArchitectureReadError(err) {
	case ErrRBACForbidden:
		value.Status, value.Reason, value.Message = "restricted", "permission", "当前账号没有此集群的访问权限"
	case ErrRBACSyncPending:
		value.Status, value.Reason, value.Message = "restricted", "rbac_pending", "集群授权尚未同步，请联系管理员"
	case ErrNodeArchitectureCredentials:
		value.Status, value.Reason, value.Message = "restricted", "credentials", "集群连接凭据已失效，请更新凭据"
	case ErrNodeArchitecturePermission:
		value.Status, value.Reason, value.Message = "restricted", "permission", "集群拒绝连接检查，请检查访问权限"
	case ErrNodeArchitectureConnection:
		value.Reason, value.Message = "network", "无法连接集群，请检查连接地址和网络"
	case ErrNodeArchitectureTimeout:
		value.Reason, value.Message = "timeout", "集群连接超时，请检查网络后重试"
	case ErrNodeArchitectureTLS:
		value.Reason, value.Message = "tls", "集群证书校验失败，请检查连接配置"
	case ErrInvalidCluster:
		value.Reason, value.Message = "invalid_config", "集群连接配置无效，请检查配置"
	case ErrNodeArchitectureCanceled:
		value.Status, value.Reason, value.Message = "unknown", "canceled", "连接检查已取消"
	}
	return value
}
