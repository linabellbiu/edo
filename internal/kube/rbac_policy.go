package kube

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

func (s *Service) rbacActor(ctx context.Context, actorID, clusterID string, write bool) (*model.KubernetesCluster, *access.FreshProjectAuthorization, error) {
	return s.rbacActorWithInactive(ctx, actorID, clusterID, write, false)
}

// Connection recovery may inspect inactive clusters, but still requires the
// current project and RBAC management permission. Business access never opts in.
func (s *Service) rbacActorWithInactive(ctx context.Context, actorID, clusterID string, write, allowInactive bool) (*model.KubernetesCluster, *access.FreshProjectAuthorization, error) {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.UserID == "" || scope.UserID != actorID {
		return nil, nil, ErrRBACForbidden
	}
	cluster, err := s.Find(ctx, clusterID)
	if err != nil {
		return nil, nil, err
	}
	if !cluster.IsActive && !allowInactive {
		return nil, nil, ErrClusterNotFound
	}
	actor, err := access.FreshProjectPermissions(s.db, ctx, actorID, cluster.ProjectID)
	if err != nil {
		return nil, nil, ErrRBACForbidden
	}
	if write && !actor.Permissions["kubernetes.rbac.update"] || !write && !actor.Permissions["kubernetes.rbac.read"] && !actor.Permissions["kubernetes.rbac.update"] {
		return nil, nil, ErrRBACForbidden
	}
	return cluster, actor, nil
}

func (s *Service) readRBACPolicy(ctx context.Context, cluster *model.KubernetesCluster) (model.KubernetesRBACPolicy, []model.KubernetesRoleGrant, error) {
	policy := model.KubernetesRBACPolicy{ClusterID: cluster.ID, ProjectID: cluster.ProjectID, DepartmentID: cluster.DepartmentID, Status: "disabled"}
	err := s.db.WithContext(ctx).Where("cluster_id = ?", cluster.ID).First(&policy).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return policy, nil, err
	}
	grants := []model.KubernetesRoleGrant{}
	if err := s.db.WithContext(ctx).Where("cluster_id = ?", cluster.ID).Order("role_id ASC, cluster_scope ASC, namespace ASC").Find(&grants).Error; err != nil {
		return policy, nil, err
	}
	return policy, grants, nil
}

func grantView(grant model.KubernetesRoleGrant, roleName string) (RBACGrant, error) {
	permissions := []string{}
	if err := json.Unmarshal(grant.Permissions, &permissions); err != nil {
		return RBACGrant{}, ErrRBACInvalid
	}
	return RBACGrant{ID: grant.ID, RoleID: grant.RoleID, RoleName: roleName, Namespace: grant.Namespace, ClusterScope: grant.ClusterScope, Permissions: permissions, UpdatedAt: grant.UpdatedAt}, nil
}

func (s *Service) clusterRBACState(ctx context.Context, cluster *model.KubernetesCluster) (*RBACState, error) {
	policy, grants, err := s.readRBACPolicy(ctx, cluster)
	if err != nil {
		return nil, err
	}
	state := &RBACState{ClusterID: cluster.ID, Enabled: policy.Enabled, DesiredEnabled: policy.DesiredEnabled, Revision: policy.Revision, Status: policy.Status, LastError: policy.LastError, Grants: []RBACGrant{}}
	roleIDs := []string{}
	for _, grant := range grants {
		roleIDs = append(roleIDs, grant.RoleID)
	}
	roles := []model.Role{}
	if len(roleIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("uuid IN ?", roleIDs).Find(&roles).Error; err != nil {
			return nil, err
		}
	}
	names := map[string]string{}
	for _, role := range roles {
		names[role.ID] = role.DisplayName
	}
	for _, grant := range grants {
		view, err := grantView(grant, names[grant.RoleID])
		if err != nil {
			return nil, err
		}
		state.Grants = append(state.Grants, view)
	}
	return state, nil
}

func (s *Service) ListClusterRBAC(ctx context.Context, clusterID string) (*RBACState, error) {
	scope, _ := database.DepartmentScopeFromContext(ctx)
	cluster, _, err := s.rbacActor(ctx, scope.UserID, clusterID, false)
	if err != nil {
		return nil, err
	}
	return s.clusterRBACState(ctx, cluster)
}

func (s *Service) validateGrantRole(ctx context.Context, input RBACGrantInput) (model.Role, error) {
	var role model.Role
	if err := s.db.WithContext(ctx).First(&role, "uuid = ?", input.RoleID).Error; err != nil {
		return role, ErrRBACInvalid
	}
	var codes []string
	if err := s.db.WithContext(ctx).Model(&model.RolePermission{}).Where("role_id = ?", input.RoleID).Pluck("permission", &codes).Error; err != nil {
		return role, err
	}
	set := permissionSet(codes)
	for _, code := range codes {
		if !access.ProjectPermission(code) {
			return role, ErrRBACInvalid
		}
	}
	for _, code := range input.Permissions {
		if !set[code] {
			return role, ErrRBACInvalid
		}
	}
	return role, nil
}

func (s *Service) canDelegateRBAC(ctx context.Context, cluster *model.KubernetesCluster, actor *access.FreshProjectAuthorization, input RBACGrantInput) error {
	if actor.User.IsSuperuser {
		return nil
	}
	for _, code := range input.Permissions {
		if !actor.Permissions[code] {
			return ErrRBACForbidden
		}
	}
	_, grants, err := s.readRBACPolicy(ctx, cluster)
	if err != nil {
		return err
	}
	covered := map[string]bool{}
	for _, grant := range grants {
		if grant.RoleID != actor.RoleID || grant.Namespace != input.Namespace || grant.ClusterScope != input.ClusterScope {
			continue
		}
		var codes []string
		if json.Unmarshal(grant.Permissions, &codes) != nil {
			return ErrRBACInvalid
		}
		for _, code := range codes {
			covered[code] = true
		}
	}
	for _, code := range input.Permissions {
		if !covered[code] {
			return ErrRBACForbidden
		}
	}
	return nil
}

func (s *Service) PreviewRoleGrant(ctx context.Context, actorID, clusterID, grantID string, input RBACGrantInput) (*RBACPreview, error) {
	cluster, actor, err := s.rbacActor(ctx, actorID, clusterID, true)
	if err != nil {
		return nil, err
	}
	input, err = normalizeRBACGrant(input)
	if err != nil {
		return nil, err
	}
	role, err := s.validateGrantRole(ctx, input)
	if err != nil {
		return nil, err
	}
	if err := s.canDelegateRBAC(ctx, cluster, actor, input); err != nil {
		return nil, err
	}
	policy, grants, err := s.readRBACPolicy(ctx, cluster)
	if err != nil {
		return nil, err
	}
	if policy.Revision != input.ExpectedRevision {
		return nil, ErrRBACConflict
	}
	if grantID != "" {
		found := false
		for _, grant := range grants {
			if grant.ID == grantID {
				found = true
			}
		}
		if !found {
			return nil, ErrRBACNotFound
		}
	}
	grant := RBACGrant{ID: grantID, RoleID: input.RoleID, RoleName: role.DisplayName, Namespace: input.Namespace, ClusterScope: input.ClusterScope, Permissions: input.Permissions}
	result := &RBACPreview{Grant: grant, Rules: []RBACRulePreview{}, Subjects: []string{}, Warnings: []string{}, Revision: policy.Revision}
	var members []model.ProjectMember
	if err := s.db.WithContext(ctx).Where("project_id = ? AND role_id = ?", cluster.ProjectID, input.RoleID).Find(&members).Error; err != nil {
		return nil, err
	}
	proposedPolicy := policy
	proposedPolicy.Revision++
	proposedPolicy.Enabled = true
	proposedPolicy.DesiredEnabled = true
	proposedPolicy.Status = "synced"
	proposed := []model.KubernetesRoleGrant{}
	for _, existing := range grants {
		if existing.ID != grantID {
			proposed = append(proposed, existing)
		}
	}
	encoded, _ := json.Marshal(input.Permissions)
	proposed = append(proposed, model.KubernetesRoleGrant{RoleID: input.RoleID, Namespace: input.Namespace, ClusterScope: input.ClusterScope, Permissions: encoded})
	for _, member := range members {
		fresh, err := access.FreshProjectPermissions(s.db, ctx, member.UserID, cluster.ProjectID)
		if err != nil {
			continue
		}
		plan, err := compileRBACSubject(cluster, proposedPolicy, proposed, fresh)
		if errors.Is(err, ErrRBACForbidden) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result.Subjects = append(result.Subjects, plan.authorization.Principal)
		for _, namespace := range plan.authorization.Namespaces {
			result.Rules = append(result.Rules, RBACRulePreview{Subject: plan.authorization.Principal, Namespace: namespace, Rules: plan.namespaced[namespace]})
		}
		if len(plan.clusterRules) > 0 {
			result.Rules = append(result.Rules, RBACRulePreview{Subject: plan.authorization.Principal, ClusterScope: true, Rules: plan.clusterRules})
		}
	}
	if len(result.Rules) == 0 {
		result.Rules = append(result.Rules, RBACRulePreview{Namespace: input.Namespace, ClusterScope: input.ClusterScope, Rules: rbacRules(permissionSet(input.Permissions), input.Namespace, input.ClusterScope)})
	}

	sort.Strings(result.Subjects)
	result.AffectedUsers = len(result.Subjects)
	if result.AffectedUsers == 0 {
		result.Warnings = append(result.Warnings, "此角色目前没有具备对应有效权限的项目成员；不会自动给其他用户分配角色")
	}
	if !policy.Enabled {
		result.Warnings = append(result.Warnings, "当前集群尚未启用原生 RBAC 映射，保存仅记录授权草稿；启用并同步成功后才按本范围访问")
	}
	result.Warnings = append(result.Warnings, "用户显式拒绝优先；Secret 内容仅在单独授予 kubernetes.secret.read 时可读。控制平面/工作节点标签不是访问授权角色。")
	result.Warnings = append(result.Warnings, "同步会附加仅当前身份的权限自查，以及已授权命名空间的精确元数据读取；不包含其他命名空间或其他用户的权限。")
	return result, nil
}

func (s *Service) mutateRBAC(ctx context.Context, cluster *model.KubernetesCluster, expected uint64, mutate func(*gorm.DB, *model.KubernetesRBACPolicy) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var policy model.KubernetesRBACPolicy
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("cluster_id = ?", cluster.ID).First(&policy).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if expected != 0 {
				return ErrRBACConflict
			}
			policy = model.KubernetesRBACPolicy{ID: uuid.NewString(), ClusterID: cluster.ID, ProjectID: cluster.ProjectID, DepartmentID: cluster.DepartmentID, Status: "disabled", UpdatedAt: time.Now().UTC()}
			if err := tx.Create(&policy).Error; err != nil {
				if errors.Is(err, gorm.ErrDuplicatedKey) {
					return ErrRBACConflict
				}
				return err
			}
		} else if err != nil {
			return err
		}
		if policy.Revision != expected {
			return ErrRBACConflict
		}
		if err := mutate(tx, &policy); err != nil {
			return err
		}
		status := "disabled"
		if policy.Enabled {
			status = "pending"
		}
		updated := tx.Model(&model.KubernetesRBACPolicy{}).Where("uuid = ? AND revision = ?", policy.ID, expected).Updates(map[string]any{"revision": expected + 1, "enabled": policy.Enabled, "desired_enabled": policy.DesiredEnabled, "status": status, "last_error": "", "updated_at": time.Now().UTC()})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRBACConflict
		}
		return nil
	})
}

func (s *Service) SaveRoleGrant(ctx context.Context, actorID, clusterID, grantID string, input RBACGrantInput) (*RBACState, error) {
	cluster, _, err := s.rbacActor(ctx, actorID, clusterID, true)
	if err != nil {
		return nil, err
	}
	input, err = normalizeRBACGrant(input)
	if err != nil {
		return nil, err
	}
	err = s.mutateRBAC(ctx, cluster, input.ExpectedRevision, func(tx *gorm.DB, policy *model.KubernetesRBACPolicy) error {
		validation := s.WithTransaction(tx)
		_, actor, err := validation.rbacActor(ctx, actorID, clusterID, true)
		if err != nil {
			return err
		}
		// Serialize against global role deletion. Otherwise an empty role could
		// disappear after validation while this transaction adds its first grant.
		var lockedRole model.Role
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedRole, "uuid = ?", input.RoleID).Error; err != nil {
			return ErrRBACInvalid
		}
		if _, err := validation.validateGrantRole(ctx, input); err != nil {
			return err
		}
		if err := validation.canDelegateRBAC(ctx, cluster, actor, input); err != nil {
			return err
		}
		encoded, _ := json.Marshal(input.Permissions)
		now := time.Now().UTC()
		if grantID == "" {
			grant := model.KubernetesRoleGrant{ID: uuid.NewString(), ClusterID: cluster.ID, ProjectID: cluster.ProjectID, DepartmentID: cluster.DepartmentID, RoleID: input.RoleID, Namespace: input.Namespace, ClusterScope: input.ClusterScope, Permissions: encoded, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&grant).Error; err != nil {
				if errors.Is(err, gorm.ErrDuplicatedKey) {
					return ErrRBACConflict
				}
				return err
			}
			return nil
		}
		var old model.KubernetesRoleGrant
		if err := tx.Where("cluster_id = ? AND uuid = ?", cluster.ID, grantID).First(&old).Error; err != nil {
			return ErrRBACNotFound
		}
		if err := validation.canDelegateRBAC(ctx, cluster, actor, RBACGrantInput{RoleID: old.RoleID, Namespace: old.Namespace, ClusterScope: old.ClusterScope, Permissions: grantCodes(old)}); err != nil {
			return err
		}
		return tx.Model(&old).Updates(map[string]any{"role_id": input.RoleID, "namespace": input.Namespace, "cluster_scope": input.ClusterScope, "permissions": encoded, "updated_at": now}).Error
	})
	if err != nil {
		return nil, err
	}
	s.invalidateClusterQueries(cluster.ID)
	return s.clusterRBACState(ctx, cluster)
}

func grantCodes(grant model.KubernetesRoleGrant) []string {
	var codes []string
	_ = json.Unmarshal(grant.Permissions, &codes)
	return codes
}

func (s *Service) DeleteRoleGrant(ctx context.Context, actorID, clusterID, grantID string, expected uint64) (*RBACState, error) {
	cluster, _, err := s.rbacActor(ctx, actorID, clusterID, true)
	if err != nil {
		return nil, err
	}
	err = s.mutateRBAC(ctx, cluster, expected, func(tx *gorm.DB, _ *model.KubernetesRBACPolicy) error {
		validation := s.WithTransaction(tx)
		_, actor, err := validation.rbacActor(ctx, actorID, clusterID, true)
		if err != nil {
			return err
		}
		var grant model.KubernetesRoleGrant
		if err := tx.Where("uuid = ? AND cluster_id = ?", grantID, cluster.ID).First(&grant).Error; err != nil {
			return ErrRBACNotFound
		}
		if err := validation.canDelegateRBAC(ctx, cluster, actor, RBACGrantInput{RoleID: grant.RoleID, Namespace: grant.Namespace, ClusterScope: grant.ClusterScope, Permissions: grantCodes(grant)}); err != nil {
			return err
		}
		return tx.Delete(&grant).Error
	})
	if err != nil {
		return nil, err
	}
	s.invalidateClusterQueries(cluster.ID)
	return s.clusterRBACState(ctx, cluster)
}

func (s *Service) SetClusterRBACEnabled(ctx context.Context, actorID, clusterID string, enabled bool, expected uint64) (*RBACState, error) {
	cluster, _, err := s.rbacActor(ctx, actorID, clusterID, true)
	if err != nil {
		return nil, err
	}
	err = s.mutateRBAC(ctx, cluster, expected, func(tx *gorm.DB, policy *model.KubernetesRBACPolicy) error {
		validation := s.WithTransaction(tx)
		_, actor, err := validation.rbacActor(ctx, actorID, clusterID, true)
		if err != nil {
			return err
		}
		// Disabling restores the legacy trust boundary and is reserved to the
		// explicit superuser recovery path, never a delegate's implicit fallback.
		if !enabled && !actor.User.IsSuperuser {
			return ErrRBACForbidden
		}
		if enabled && !actor.User.IsSuperuser {
			_, grants, err := validation.readRBACPolicy(ctx, cluster)
			if err != nil {
				return err
			}
			if len(grants) == 0 {
				return ErrRBACForbidden
			}
			for _, grant := range grants {
				if err := validation.canDelegateRBAC(ctx, cluster, actor, RBACGrantInput{Namespace: grant.Namespace, ClusterScope: grant.ClusterScope, Permissions: grantCodes(grant)}); err != nil {
					return err
				}
			}
		}
		policy.DesiredEnabled = enabled
		if enabled {
			policy.Enabled = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.invalidateClusterQueries(cluster.ID)
	return s.clusterRBACState(ctx, cluster)
}

// RequireRBACManagementForConnectionChange protects enabled mappings when a
// kubeconfig is rotated or a connection would point at a different cluster.
func (s *Service) RequireRBACManagementForConnectionChange(ctx context.Context, cluster *model.KubernetesCluster) error {
	policy, _, err := s.readRBACPolicy(ctx, cluster)
	if err != nil {
		return err
	}
	if !policy.Enabled {
		return nil
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	_, _, err = s.rbacActorWithInactive(ctx, scope.UserID, cluster.ID, true, true)
	return err
}

func (s *Service) MarkRBACConnectionChanged(ctx context.Context, clusterID string) error {
	updated := s.db.WithContext(ctx).Model(&model.KubernetesRBACPolicy{}).Where("cluster_id = ? AND enabled = ?", clusterID, true).Updates(map[string]any{"revision": gorm.Expr("revision + 1"), "status": "pending", "last_error": "", "updated_at": time.Now().UTC()})
	s.invalidateClusterQueries(clusterID)
	return updated.Error
}
