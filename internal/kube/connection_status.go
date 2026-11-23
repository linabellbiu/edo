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
	"sync/atomic"
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
	connectionStatusTTL    = 30 * time.Second
	connectionCheckTimeout = 5 * time.Second
	// 探测超时仍未退出时回收名额，避免后续目标无法得到检测。
	connectionCheckStaleAfter  = connectionCheckTimeout + 2*time.Second
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
	Checking  bool       `json:"checking,omitempty"`
}

type connectionStatusEntry struct {
	value     ConnectionStatus
	checking  bool
	requested bool
	stalled   bool
	startedAt time.Time
	sequence  uint64
	lease     *connectionCheckLease
	lastUsed  time.Time
	cancel    context.CancelFunc
}

// connectionCheckLease 是一次后台检查占用的名额；释放可重复调用且只生效一次，
// 卡住的检查被回收后，其迟到返回不会再次扣减名额。
type connectionCheckLease struct{ released atomic.Bool }

type connectionCheckGate struct {
	mu     sync.Mutex
	active int
	limit  int
}

func newConnectionCheckGate(limit int) *connectionCheckGate {
	return &connectionCheckGate{limit: limit}
}

func (g *connectionCheckGate) acquire() *connectionCheckLease {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active >= g.limit {
		return nil
	}
	g.active++
	return &connectionCheckLease{}
}

func (g *connectionCheckGate) release(lease *connectionCheckLease) {
	if lease == nil || !lease.released.CompareAndSwap(false, true) {
		return
	}
	g.mu.Lock()
	if g.active > 0 {
		g.active--
	}
	g.mu.Unlock()
}

func (g *connectionCheckGate) activeCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active
}

type connectionStatusCache struct {
	mu             sync.Mutex
	entries        map[string]*connectionStatusEntry
	monitorEntries map[string]*connectionStatusEntry
	subjectRetries map[string]time.Time
	checks         *connectionCheckGate
}

func newConnectionStatusCache() *connectionStatusCache {
	return &connectionStatusCache{
		entries:        map[string]*connectionStatusEntry{},
		monitorEntries: map[string]*connectionStatusEntry{},
		subjectRetries: map[string]time.Time{},
		checks:         newConnectionCheckGate(maxConnectionChecks),
	}
}

type connectionAuthorization struct {
	authorization *clusterAuthorization
	err           error
}

// ConnectionStatuses 批量读取当前权限和快照。只有显式刷新才请求重新检测。
func (s *Service) ConnectionStatuses(ctx context.Context, ids []string, refresh bool) ([]ConnectionStatus, error) {
	return s.connectionStatuses(ctx, ids, refresh, refresh)
}

func (s *Service) connectionStatuses(ctx context.Context, ids []string, refresh, probe bool) ([]ConnectionStatus, error) {
	clusters, authorizations, err := s.loadConnectionTargets(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]ConnectionStatus, len(clusters))
	order := make([]int, len(clusters))
	for i := range order {
		order[i] = i
	}
	if probe {
		order = s.prioritizeConnectionChecks(clusters, authorizations)
	}
	for _, i := range order {
		cluster := &clusters[i]
		if !cluster.IsActive {
			result[i] = ConnectionStatus{ClusterID: cluster.ID, Status: "unknown", Reason: "disabled", Message: "集群已停用"}
			continue
		}
		auth := authorizations[cluster.ID]
		if auth.err != nil {
			if !errors.Is(auth.err, ErrRBACForbidden) && !errors.Is(auth.err, ErrRBACSyncPending) {
				slog.WarnContext(ctx, "读取集群连接状态授权失败", "operation", "kubernetes_connection_status_cluster_authorization", "cluster_id", cluster.ID, "err", auth.err)
			}
			result[i] = connectionFailureStatus(cluster.ID, auth.err)
			continue
		}
		result[i] = s.readCachedConnectionStatus(cluster, auth.authorization)
		if probe {
			target := auth.authorization
			if refresh && !target.enforced() {
				target = nil
			}
			value := s.cachedConnectionStatus(ctx, cluster, target, refresh)
			result[i] = newerConnectionStatus(result[i], value)
		}
	}
	return result, nil
}

func (s *Service) loadConnectionTargets(ctx context.Context, ids []string) ([]model.KubernetesCluster, map[string]connectionAuthorization, error) {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.UserID == "" || scope.ProjectID == "" || scope.DepartmentID == "" {
		slog.WarnContext(ctx, "查询集群连接状态缺少操作者范围", "operation", "kubernetes_connection_status", "err", ErrRBACForbidden)
		return nil, nil, ErrRBACForbidden
	}
	var clusters []model.KubernetesCluster
	var authorizations map[string]connectionAuthorization
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fresh, err := access.FreshProjectPermissions(tx, ctx, scope.UserID, scope.ProjectID)
		if errors.Is(err, access.ErrAccessDelegationDenied) {
			return ErrRBACForbidden
		}
		if err != nil {
			slog.WarnContext(ctx, "核验集群连接状态读取权限失败", "operation", "kubernetes_connection_status_authorization", "user_id", scope.UserID, "project_id", scope.ProjectID, "err", err)
			return err
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
		return nil, nil, err
	}
	return clusters, authorizations, nil
}

func (s *Service) readCachedConnectionStatus(cluster *model.KubernetesCluster, authorization *clusterAuthorization) ConnectionStatus {
	value := ConnectionStatus{ClusterID: cluster.ID, Status: "unknown", Reason: "not_checked", Message: "尚无连接检测结果，等待后台检测"}
	if s.connections != nil {
		s.connections.mu.Lock()
		defer s.connections.mu.Unlock()
		// 未启用原生身份映射时，使用独立后台任务以集群连接凭据取得的基础结果。
		// 该快照与人工检测及模拟用户的结果分开，不能替代当前操作者的权限判断。
		if !authorization.enforced() {
			if entry := s.connections.monitorEntries[connectionStatusKey(cluster, nil)]; entry != nil {
				value = entry.status()
			}
		}
		if entry := s.connections.entries[connectionStatusKey(cluster, authorization)]; entry != nil {
			value = newerConnectionStatus(value, entry.status())
		}
	}
	return value
}

func (e *connectionStatusEntry) status() ConnectionStatus {
	value := e.value
	value.Checking = e.checking || e.requested
	return value
}

func newerConnectionStatus(previous, next ConnectionStatus) ConnectionStatus {
	checking := previous.Checking || next.Checking
	if previous.CheckedAt == nil || next.CheckedAt != nil && !next.CheckedAt.Before(*previous.CheckedAt) {
		previous = next
	}
	previous.Checking = checking
	return previous
}

func connectionStatusKey(cluster *model.KubernetesCluster, authorization *clusterAuthorization) string {
	return connectionStatusVersionKey(clusterQueryVersion(cluster), authorization)
}

func connectionStatusVersionKey(version [32]byte, authorization *clusterAuthorization) string {
	if authorization == nil {
		return fmt.Sprintf("monitor/%x", version)
	}
	return fmt.Sprintf("actor/%s/%s/%x", authorization.CacheKey, authorization.Version, version)
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
	if fresh.Permissions[access.PermissionClusterRead] || fresh.Permissions["kubernetes.rbac.read"] || fresh.Permissions["kubernetes.rbac.update"] {
		return true
	}
	for _, def := range resourceDefinitions {
		if fresh.Permissions[def.Permission+".read"] {
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
	entries := cache.entries
	if authorization == nil {
		entries = cache.monitorEntries
	}
	entry := entries[key]
	if entry != nil {
		entry.lastUsed = now
		if entry.checking && !entry.startedAt.IsZero() && now.Sub(entry.startedAt) > connectionCheckStaleAfter {
			// 检查卡住：取消、回收名额并作废旧结果，下面按最新配置重新检查。
			slog.WarnContext(ctx, "集群连接检查超时未完成，已回收并重试", "operation", "kubernetes_connection_check_stalled", "cluster_id", cluster.ID, "elapsed_ms", now.Sub(entry.startedAt).Milliseconds(), "active_checks", cache.checks.activeCount())
			cache.abandonCheck(entry, now)
		}
		refreshAllowed := refresh && (entry.value.CheckedAt == nil || now.Sub(*entry.value.CheckedAt) >= 2*time.Second)
		if entry.checking || !entry.requested && !refreshAllowed && entry.value.ExpiresAt != nil && now.Before(*entry.value.ExpiresAt) {
			return entry.status()
		}
	}
	if entry == nil {
		if authorization != nil {
			cache.trim(now)
			if len(cache.entries) >= maxConnectionStatusEntries {
				return ConnectionStatus{ClusterID: cluster.ID, Status: "unknown", Reason: "not_checked", Message: "等待后台检测"}
			}
		}
		entry = &connectionStatusEntry{value: ConnectionStatus{ClusterID: cluster.ID}, lastUsed: now}
		entries[key] = entry
	}
	// 已占满时由下一轮后台扫描重试。
	lease := cache.checks.acquire()
	if lease == nil {
		entry.requested = entry.requested || refresh
		if !entry.stalled && entry.value.CheckedAt == nil {
			entry.value = ConnectionStatus{ClusterID: cluster.ID, Status: "checking", Reason: "queued", Message: "等待检查连接", CheckedAt: entry.value.CheckedAt}
		}
		return entry.status()
	}
	parent := context.WithoutCancel(ctx)
	if monitor, ok := ctx.Value(connectionMonitorContextKey{}).(context.Context); ok {
		parent = monitor
	}
	if s.queries != nil {
		s.queries.mu.Lock()
		if s.queries.ctx != nil {
			parent = s.queries.ctx
		}
		s.queries.mu.Unlock()
	}
	checkContext, cancel := context.WithTimeout(parent, connectionCheckTimeout)
	entry.sequence++
	sequence := entry.sequence
	// 卡住后的重试保留“检测超时”结论，避免页面在重试期间继续转圈。
	if !entry.stalled && entry.value.CheckedAt == nil {
		entry.value = ConnectionStatus{ClusterID: cluster.ID, Status: "checking", Reason: "checking", Message: "正在检查连接", CheckedAt: entry.value.CheckedAt}
	}
	entry.cancel, entry.checking, entry.requested, entry.startedAt, entry.lease = cancel, true, false, now, lease
	clusterSnapshot := *cluster
	authorizationSnapshot := cloneClusterAuthorization(authorization)
	go func() {
		defer cancel()
		defer cache.checks.release(lease)
		value := s.checkConnection(checkContext, &clusterSnapshot, authorizationSnapshot)
		checked := time.Now().UTC()
		expires := checked.Add(connectionStatusTTL)
		value.CheckedAt, value.ExpiresAt = &checked, &expires
		cache.mu.Lock()
		defer cache.mu.Unlock()
		// 被回收或被替换的旧检查不能覆盖新结果。
		if entries[key] == entry && entry.sequence == sequence {
			entry.value, entry.checking, entry.stalled, entry.cancel, entry.lease, entry.startedAt = value, false, false, nil, nil, time.Time{}
		}
	}()
	return entry.status()
}

// recordConnectionSuccess 用一次真实成功的探测回填连接状态缓存。
// 后台主机运行状态探测会频繁调用 Ping，不能借此清空缓存。
func (s *Service) recordConnectionSuccess(connection *clusterQueries) {
	cache := s.connections
	if cache == nil || connection == nil || connection.internal || connection.authorization == nil || !connection.authorization.identityBound() {
		return
	}
	now := time.Now().UTC()
	expires := now.Add(connectionStatusTTL)
	key := connectionStatusVersionKey(connection.connectionVersion, connection.authorization)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry := cache.entries[key]
	if entry == nil {
		cache.trim(now)
		if len(cache.entries) >= maxConnectionStatusEntries {
			return
		}
		entry = &connectionStatusEntry{lastUsed: now}
		cache.entries[key] = entry
	} else if entry.checking {
		if entry.cancel != nil {
			entry.cancel()
		}
		cache.checks.release(entry.lease)
	}
	entry.sequence++
	entry.checking, entry.requested, entry.stalled, entry.cancel, entry.lease, entry.startedAt = false, false, false, nil, nil, time.Time{}
	entry.lastUsed = now
	entry.value = ConnectionStatus{ClusterID: connection.clusterID, Status: "connected", Reason: "connected", Message: "连接正常", CheckedAt: &now, ExpiresAt: &expires}
}

// 调用者已持锁；只终止本次检查并回收其名额，迟到结果因 sequence 变化被丢弃。
func (c *connectionStatusCache) abandonCheck(entry *connectionStatusEntry, now time.Time) {
	if entry.cancel != nil {
		entry.cancel()
	}
	c.checks.release(entry.lease)
	entry.sequence++
	entry.checking, entry.stalled, entry.cancel, entry.lease, entry.startedAt = false, true, nil, nil, time.Time{}
	entry.value = ConnectionStatus{ClusterID: entry.value.ClusterID, Status: "unknown", Reason: "stalled", Message: "连接检测超时，请重试", CheckedAt: entry.value.CheckedAt}
	entry.lastUsed = now
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
	for _, entries := range []map[string]*connectionStatusEntry{s.connections.entries, s.connections.monitorEntries} {
		for key, entry := range entries {
			if entry.value.ClusterID == id {
				if entry.cancel != nil {
					entry.cancel()
				}
				s.connections.checks.release(entry.lease)
				delete(entries, key)
			}
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
