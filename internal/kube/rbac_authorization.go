package kube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"

	"gorm.io/gorm"
	rbacv1 "k8s.io/api/rbac/v1"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

func rbacHash(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
func rbacPrincipal(projectID, userID, version string) string {
	return "edo:" + projectID + ":" + userID + ":" + version
}

type rbacSubjectPlan struct {
	policy        model.KubernetesRBACPolicy
	userID        string
	freshDigest   string
	authorization *clusterAuthorization
	namespaced    map[string][]rbacv1.PolicyRule
	clusterRules  []rbacv1.PolicyRule
}

func (s *Service) subjectPlan(ctx context.Context, cluster *model.KubernetesCluster, userID string) (*rbacSubjectPlan, error) {
	plan := &rbacSubjectPlan{userID: userID, namespaced: map[string][]rbacv1.PolicyRule{}, clusterRules: []rbacv1.PolicyRule{}}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		reader := s.WithTransaction(tx)
		var fresh *access.FreshProjectAuthorization
		var err error
		if userID != "" {
			fresh, err = access.FreshProjectPermissions(tx, ctx, userID, cluster.ProjectID)
			if err != nil {
				slog.WarnContext(ctx, "核验集群操作者权限失败", "operation", "kubernetes_cluster_authorization", "cluster_id", cluster.ID, "user_id", userID, "err", err)
				return ErrRBACForbidden
			}
			if fresh.User.IsSuperuser {
				// 使用现有集群连接，不写入额外角色绑定；身份变化后立即作废此授权。
				plan.freshDigest = fresh.Digest
				plan.authorization = &clusterAuthorization{Administrator: true, CacheKey: cluster.ProjectID + "/" + userID, Version: rbacHash([]any{"administrator", clusterQueryVersion(cluster), fresh.Digest})}
				return nil
			}
		}
		policy, grants, err := reader.readRBACPolicy(ctx, cluster)
		if err != nil {
			return err
		}
		plan.policy = policy
		if !policy.Enabled {
			if policy.DesiredEnabled || policy.Status != "disabled" {
				return ErrRBACSyncPending
			}
			plan.authorization = &clusterAuthorization{Enforced: false}
			if fresh != nil {
				codes := []string{}
				for code, permitted := range fresh.Permissions {
					if permitted {
						codes = append(codes, code)
					}
				}
				sort.Strings(codes)
				plan.authorization.CacheKey = cluster.ProjectID + "/" + userID
				plan.authorization.Version = rbacHash([]any{"application", clusterQueryVersion(cluster), policy.Revision, fresh.Digest})
				plan.authorization.Permissions = map[string][]string{"": codes}
			}
			return nil
		}
		if !policy.DesiredEnabled || policy.Status != "synced" && policy.Status != "pending" && policy.Status != "error" {
			return ErrRBACSyncPending
		}
		if fresh == nil {
			fresh, err = access.FreshProjectPermissions(tx, ctx, userID, cluster.ProjectID)
			if err != nil {
				return ErrRBACForbidden
			}
		}
		compiled, err := compileRBACSubject(cluster, policy, grants, fresh)
		if err != nil {
			return err
		}
		*plan = *compiled
		return nil
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func compileRBACSubject(cluster *model.KubernetesCluster, policy model.KubernetesRBACPolicy, grants []model.KubernetesRoleGrant, fresh *access.FreshProjectAuthorization) (*rbacSubjectPlan, error) {
	userID := fresh.User.ID
	plan := &rbacSubjectPlan{policy: policy, userID: userID, namespaced: map[string][]rbacv1.PolicyRule{}, clusterRules: []rbacv1.PolicyRule{}}
	plan.freshDigest = fresh.Digest
	if fresh.RoleID == "" {
		return nil, ErrRBACForbidden
	}
	scopes := map[string]map[string]bool{}
	for _, grant := range grants {
		if grant.RoleID != fresh.RoleID {
			continue
		}
		codes := []string{}
		if json.Unmarshal(grant.Permissions, &codes) != nil {
			return nil, ErrRBACInvalid
		}
		key := grant.Namespace
		if grant.ClusterScope {
			key = ""
		}
		if scopes[key] == nil {
			scopes[key] = map[string]bool{}
		}
		for _, code := range codes {
			if fresh.Permissions[code] {
				scopes[key][code] = true
			}
		}
	}
	permissions := map[string][]string{}
	namespaces := []string{}
	for namespace, codes := range scopes {
		rules := rbacRules(codes, namespace, namespace == "")
		if len(rules) == 0 {
			continue
		}
		for code, allowed := range codes {
			if allowed {
				permissions[namespace] = append(permissions[namespace], code)
			}
		}
		sort.Strings(permissions[namespace])
		if namespace == "" {
			plan.clusterRules = rules
		} else {
			plan.namespaced[namespace] = rules
			namespaces = append(namespaces, namespace)
		}
	}
	if len(plan.namespaced) == 0 && len(plan.clusterRules) == 0 {
		return nil, ErrRBACForbidden
	}
	sort.Strings(namespaces)
	if len(namespaces) > 0 {
		plan.clusterRules = append(plan.clusterRules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"namespaces"}, ResourceNames: append([]string(nil), namespaces...), Verbs: []string{"get"}})
	}
	// Active watch/log/exec leases ask the API server about their own current
	// permissions; this never authorizes reviews of a different identity.
	plan.clusterRules = append(plan.clusterRules, rbacv1.PolicyRule{APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"selfsubjectaccessreviews"}, Verbs: []string{"create"}})
	// Revision and current connection are part of the identity, not merely a
	// cache tag: old immutable native bindings cannot authorize a new request.
	version := rbacHash([]any{clusterQueryVersion(cluster), policy.Revision, fresh.Digest, permissions, plan.namespaced, plan.clusterRules})
	plan.authorization = &clusterAuthorization{Enforced: true, CacheKey: cluster.ProjectID + "/" + userID, Version: version, Principal: rbacPrincipal(cluster.ProjectID, userID, version), Namespaces: namespaces, ClusterScope: len(permissions[""]) > 0, Permissions: permissions}
	return plan, nil
}

func (s *Service) authorizationForCluster(ctx context.Context, cluster *model.KubernetesCluster) (*clusterAuthorization, error) {
	scope, _ := database.DepartmentScopeFromContext(ctx)
	plan, err := s.subjectPlan(ctx, cluster, scope.UserID)
	if err != nil {
		return nil, err
	}
	if !plan.authorization.Enforced {
		return plan.authorization, nil
	}
	if plan.policy.Status != "synced" {
		return nil, ErrRBACSyncPending
	}
	if !s.subjectSynced(ctx, cluster, plan) {
		if err := s.synchronizeSubject(ctx, cluster, plan); err != nil {
			return nil, err
		}
	}
	// Re-read after native API work; a permission revocation during sync must
	// never make the earlier decision available to the caller.
	return s.authorizationCurrent(ctx, cluster)
}

func (s *Service) subjectSynced(ctx context.Context, cluster *model.KubernetesCluster, plan *rbacSubjectPlan) bool {
	var subject model.KubernetesRBACSubject
	err := s.db.WithContext(ctx).Where("cluster_id = ? AND user_id = ? AND revision = ? AND policy_hash = ? AND status = ?", cluster.ID, plan.userID, plan.policy.Revision, plan.authorization.Version, "synced").First(&subject).Error
	return err == nil
}

func (s *Service) authorizationCurrent(ctx context.Context, cluster *model.KubernetesCluster) (*clusterAuthorization, error) {
	scope, _ := database.DepartmentScopeFromContext(ctx)
	plan, err := s.subjectPlan(ctx, cluster, scope.UserID)
	if err != nil {
		return nil, err
	}
	if !plan.authorization.Enforced {
		return plan.authorization, nil
	}
	if plan.policy.Status != "synced" || !s.subjectSynced(ctx, cluster, plan) {
		return nil, ErrRBACSyncPending
	}
	return plan.authorization, nil
}

// Native ownership labels are exact values and never derived from a caller's
// arbitrary username or group. They also bound cleanup to this connection.
func managedRBACLabels(cluster *model.KubernetesCluster, userID, version string) map[string]string {
	result := map[string]string{"edo.dev/managed-by": "edo-rbac", "edo.dev/cluster": cluster.ID, "edo.dev/project": cluster.ProjectID}
	if userID != "" {
		result["edo.dev/subject"] = rbacHash(userID)[:32]
	}
	if version != "" {
		result["edo.dev/policy"] = version[:32]
	}
	return result
}
func managedRBACSelector(cluster *model.KubernetesCluster) string {
	return "edo.dev/managed-by=edo-rbac,edo.dev/cluster=" + cluster.ID + ",edo.dev/project=" + cluster.ProjectID
}
func rbacObjectName(clusterID, userID, version, namespace string) string {
	return "edo-rbac-" + rbacHash([]string{clusterID, userID, version, namespace})[:40]
}

func sameRBACOwner(labels map[string]string, cluster *model.KubernetesCluster, userID, version string) bool {
	for key, value := range managedRBACLabels(cluster, userID, version) {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func isRBACPublicError(err error) bool {
	return errors.Is(err, ErrRBACForbidden) || errors.Is(err, ErrRBACSyncPending) || errors.Is(err, ErrRBACConflict) || errors.Is(err, ErrRBACInvalid) || errors.Is(err, ErrRBACNotFound)
}
func safeRBACSyncError(err error) string {
	if isRBACPublicError(err) {
		return err.Error()
	}
	return "无法同步集群 RBAC，请检查集群连接、身份模拟及角色绑定管理权限后重试"
}
