package kube

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

const RBACSyncTimeout = 120 * time.Second

var errRBACLeaseBusy = errors.New("cluster RBAC synchronization in progress")

type rbacSyncLease struct {
	owner    string
	revision uint64
}

func (s *Service) claimRBACSync(ctx context.Context, cluster *model.KubernetesCluster, revision uint64) (rbacSyncLease, error) {
	lease := rbacSyncLease{owner: uuid.NewString(), revision: revision}
	now := time.Now().UTC()
	until := now.Add(RBACSyncTimeout + 30*time.Second)
	updated := s.db.WithContext(ctx).Model(&model.KubernetesRBACPolicy{}).Where("cluster_id = ? AND revision = ? AND enabled = ? AND (lease_until IS NULL OR lease_until <= ?)", cluster.ID, revision, true, now).Updates(map[string]any{"lease_owner": lease.owner, "lease_until": until})
	if updated.Error != nil {
		return lease, updated.Error
	}
	if updated.RowsAffected != 1 {
		return lease, errRBACLeaseBusy
	}
	return lease, nil
}

func (s *Service) releaseRBACSync(ctx context.Context, clusterID string, lease rbacSyncLease) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = s.db.WithContext(releaseCtx).Model(&model.KubernetesRBACPolicy{}).Where("cluster_id = ? AND lease_owner = ?", clusterID, lease.owner).Updates(map[string]any{"lease_owner": "", "lease_until": nil}).Error
}

func (s *Service) rbacSyncFence(ctx context.Context, cluster *model.KubernetesCluster, lease rbacSyncLease, plan *rbacSubjectPlan) error {
	var policy model.KubernetesRBACPolicy
	if err := s.db.WithContext(ctx).Where("cluster_id = ? AND revision = ? AND lease_owner = ? AND enabled = ?", cluster.ID, lease.revision, lease.owner, true).First(&policy).Error; err != nil {
		return ErrRBACConflict
	}
	current, err := s.Find(ctx, cluster.ID)
	if err != nil || !current.IsActive || clusterQueryVersion(current) != clusterQueryVersion(cluster) {
		return ErrRBACConflict
	}
	if plan != nil {
		if !policy.DesiredEnabled {
			return ErrRBACConflict
		}
		fresh, err := access.FreshProjectPermissions(s.db, ctx, plan.userID, cluster.ProjectID)
		if err != nil {
			return ErrRBACForbidden
		}
		if fresh.Digest != plan.freshDigest {
			return ErrRBACConflict
		}
	}
	return ctx.Err()
}

func (s *Service) rbacManagementClient(cluster *model.KubernetesCluster) (*kubernetes.Clientset, error) {
	config, err := s.restConfigForCluster(cluster)
	if err != nil {
		return nil, err
	}
	config.Timeout = 5 * time.Second
	return kubernetes.NewForConfig(config)
}

func applyManagedRBAC[T metav1.Object](ctx context.Context, desired T, owner func(map[string]string) bool, fence func() error, get func(context.Context, string, metav1.GetOptions) (T, error), create func(context.Context, T, metav1.CreateOptions) (T, error), update func(context.Context, T, metav1.UpdateOptions) (T, error)) error {
	current, err := get(ctx, desired.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err := fence(); err != nil {
			return err
		}
		_, err = create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if !owner(current.GetLabels()) {
		return ErrRBACConflict
	}
	if sameManagedRBACSpec(desired, current) {
		return nil
	}
	desired.SetResourceVersion(current.GetResourceVersion())
	if err := fence(); err != nil {
		return err
	}
	_, err = update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func sameManagedRBACSpec(left, right metav1.Object) bool {
	switch value := left.(type) {
	case *rbacv1.Role:
		other, ok := right.(*rbacv1.Role)
		return ok && apiequality.Semantic.DeepEqual(value.Rules, other.Rules)
	case *rbacv1.ClusterRole:
		other, ok := right.(*rbacv1.ClusterRole)
		return ok && apiequality.Semantic.DeepEqual(value.Rules, other.Rules)
	case *rbacv1.RoleBinding:
		other, ok := right.(*rbacv1.RoleBinding)
		return ok && apiequality.Semantic.DeepEqual(value.RoleRef, other.RoleRef) && apiequality.Semantic.DeepEqual(value.Subjects, other.Subjects)
	case *rbacv1.ClusterRoleBinding:
		other, ok := right.(*rbacv1.ClusterRoleBinding)
		return ok && apiequality.Semantic.DeepEqual(value.RoleRef, other.RoleRef) && apiequality.Semantic.DeepEqual(value.Subjects, other.Subjects)
	}
	return false
}

func (s *Service) applySubject(ctx context.Context, cluster *model.KubernetesCluster, client *kubernetes.Clientset, lease rbacSyncLease, plan *rbacSubjectPlan) error {
	if err := s.rbacSyncFence(ctx, cluster, lease, plan); err != nil {
		return err
	}
	// Verify the management credential is already authorized to impersonate the
	// exact derived identity. EDO never grants itself impersonation privileges.
	review, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{Verb: "impersonate", Resource: "users", Name: plan.authorization.Principal}}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	if !review.Status.Allowed {
		return ErrRBACForbidden
	}
	labels := managedRBACLabels(cluster, plan.userID, plan.authorization.Version)
	owner := func(value map[string]string) bool {
		return sameRBACOwner(value, cluster, plan.userID, plan.authorization.Version)
	}
	fence := func() error { return s.rbacSyncFence(ctx, cluster, lease, plan) }
	subjects := []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: plan.authorization.Principal}}
	namespaces := append([]string(nil), plan.authorization.Namespaces...)
	sort.Strings(namespaces)
	for _, namespace := range namespaces {
		name := rbacObjectName(cluster.ID, plan.userID, plan.authorization.Version, namespace)
		roles := client.RbacV1().Roles(namespace)
		role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels}, Rules: plan.namespaced[namespace]}
		if err := applyManagedRBAC(ctx, role, owner, fence, roles.Get, roles.Create, roles.Update); err != nil {
			return err
		}
		bindings := client.RbacV1().RoleBindings(namespace)
		binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels}, Subjects: subjects, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}}
		if err := applyManagedRBAC(ctx, binding, owner, fence, bindings.Get, bindings.Create, bindings.Update); err != nil {
			return err
		}
	}
	if len(plan.clusterRules) > 0 {
		name := rbacObjectName(cluster.ID, plan.userID, plan.authorization.Version, "")
		roles := client.RbacV1().ClusterRoles()
		role := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Rules: plan.clusterRules}
		if err := applyManagedRBAC(ctx, role, owner, fence, roles.Get, roles.Create, roles.Update); err != nil {
			return err
		}
		bindings := client.RbacV1().ClusterRoleBindings()
		binding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Subjects: subjects, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: name}}
		if err := applyManagedRBAC(ctx, binding, owner, fence, bindings.Get, bindings.Create, bindings.Update); err != nil {
			return err
		}
	}
	if err := fence(); err != nil {
		return err
	}
	record := model.KubernetesRBACSubject{ID: uuid.NewString(), ClusterID: cluster.ID, ProjectID: cluster.ProjectID, DepartmentID: cluster.DepartmentID, UserID: plan.userID, Revision: lease.revision, PolicyHash: plan.authorization.Version, Status: "synced", UpdatedAt: time.Now().UTC()}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "cluster_id"}, {Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"revision", "policy_hash", "status", "updated_at"})}).Create(&record).Error
}

// cleanupManagedRBAC touches only fully-owned objects and uses UID/version
// preconditions so an object replaced after listing cannot be deleted.
func (s *Service) cleanupManagedRBAC(ctx context.Context, cluster *model.KubernetesCluster, client *kubernetes.Clientset, lease rbacSyncLease, keep map[string]bool) error {
	shouldDelete := func(meta metav1.Object) bool {
		if !sameRBACOwner(meta.GetLabels(), cluster, "", "") {
			return false
		}
		return !keep[meta.GetLabels()["edo.dev/subject"]+"/"+meta.GetLabels()["edo.dev/policy"]]
	}
	fence := func() error { return s.rbacSyncFence(ctx, cluster, lease, nil) }
	options := metav1.ListOptions{LabelSelector: managedRBACSelector(cluster), Limit: 500}
	deleteOptions := func(meta metav1.Object) metav1.DeleteOptions {
		uid, version := meta.GetUID(), meta.GetResourceVersion()
		return metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}}
	}
	continuation := ""
	seen := map[string]bool{}
	for {
		options.Continue = continuation
		page, err := client.RbacV1().RoleBindings("").List(ctx, options)
		if err != nil {
			return err
		}
		for index := range page.Items {
			item := &page.Items[index]
			if shouldDelete(item) {
				if err := fence(); err != nil {
					return err
				}
				if err := client.RbacV1().RoleBindings(item.Namespace).Delete(ctx, item.Name, deleteOptions(item)); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
			}
		}
		continuation = page.Continue
		if continuation == "" {
			break
		}
		if seen[continuation] || len(seen) > 100 {
			return ErrRBACConflict
		}
		seen[continuation] = true
	}
	continuation = ""
	seen = map[string]bool{}
	for {
		options.Continue = continuation
		page, err := client.RbacV1().ClusterRoleBindings().List(ctx, options)
		if err != nil {
			return err
		}
		for index := range page.Items {
			item := &page.Items[index]
			if shouldDelete(item) {
				if err := fence(); err != nil {
					return err
				}
				if err := client.RbacV1().ClusterRoleBindings().Delete(ctx, item.Name, deleteOptions(item)); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
			}
		}
		continuation = page.Continue
		if continuation == "" {
			break
		}
		if seen[continuation] || len(seen) > 100 {
			return ErrRBACConflict
		}
		seen[continuation] = true
	}
	continuation = ""
	seen = map[string]bool{}
	for {
		options.Continue = continuation
		page, err := client.RbacV1().Roles("").List(ctx, options)
		if err != nil {
			return err
		}
		for index := range page.Items {
			item := &page.Items[index]
			if shouldDelete(item) {
				if err := fence(); err != nil {
					return err
				}
				if err := client.RbacV1().Roles(item.Namespace).Delete(ctx, item.Name, deleteOptions(item)); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
			}
		}
		continuation = page.Continue
		if continuation == "" {
			break
		}
		if seen[continuation] || len(seen) > 100 {
			return ErrRBACConflict
		}
		seen[continuation] = true
	}
	continuation = ""
	seen = map[string]bool{}
	for {
		options.Continue = continuation
		page, err := client.RbacV1().ClusterRoles().List(ctx, options)
		if err != nil {
			return err
		}
		for index := range page.Items {
			item := &page.Items[index]
			if shouldDelete(item) {
				if err := fence(); err != nil {
					return err
				}
				if err := client.RbacV1().ClusterRoles().Delete(ctx, item.Name, deleteOptions(item)); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
			}
		}
		continuation = page.Continue
		if continuation == "" {
			break
		}
		if seen[continuation] || len(seen) > 100 {
			return ErrRBACConflict
		}
		seen[continuation] = true
	}
	return nil
}

func (s *Service) synchronizeSubject(ctx context.Context, cluster *model.KubernetesCluster, plan *rbacSubjectPlan) error {
	ctx, cancel := context.WithTimeout(ctx, RBACSyncTimeout)
	defer cancel()
	lease, err := s.claimRBACSync(ctx, cluster, plan.policy.Revision)
	if err != nil {
		return ErrRBACSyncPending
	}
	defer s.releaseRBACSync(ctx, cluster.ID, lease)
	client, err := s.rbacManagementClient(cluster)
	if err != nil {
		return ErrRBACSyncPending
	}
	if err := s.applySubject(ctx, cluster, client, lease, plan); err != nil {
		return ErrRBACSyncPending
	}
	return nil
}

func (s *Service) syncRBACCluster(ctx context.Context, cluster *model.KubernetesCluster, expected uint64) error {
	if !cluster.IsActive {
		return ErrClusterNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, RBACSyncTimeout)
	defer cancel()
	policy, _, err := s.readRBACPolicy(ctx, cluster)
	if err != nil {
		return err
	}
	if !policy.Enabled {
		return nil
	}
	if policy.Revision != expected {
		return ErrRBACConflict
	}
	lease, err := s.claimRBACSync(ctx, cluster, expected)
	if err != nil {
		return err
	}
	defer s.releaseRBACSync(ctx, cluster.ID, lease)
	client, err := s.rbacManagementClient(cluster)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	changed := policy.Status != "synced"
	if policy.DesiredEnabled {
		members := []model.ProjectMember{}
		if err := s.db.WithContext(ctx).Where("project_id = ?", cluster.ProjectID).Find(&members).Error; err != nil {
			return err
		}
		for _, member := range members {
			plan, err := s.subjectPlan(ctx, cluster, member.UserID)
			if errors.Is(err, ErrRBACForbidden) {
				continue
			}
			if err != nil {
				return err
			}
			if !plan.authorization.enforced() {
				return ErrRBACConflict
			}
			var synced model.KubernetesRBACSubject
			err = s.db.WithContext(ctx).Where("cluster_id = ? AND user_id = ? AND revision = ? AND policy_hash = ? AND status = ?", cluster.ID, member.UserID, expected, plan.authorization.Version, "synced").First(&synced).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err != nil || time.Since(synced.UpdatedAt) > time.Minute {
				if err := s.applySubject(ctx, cluster, client, lease, plan); err != nil {
					return err
				}
				if err != nil {
					changed = true
				}
			}
			keep[rbacHash(member.UserID)[:32]+"/"+plan.authorization.Version[:32]] = true
		}
	}
	if err := s.cleanupManagedRBAC(ctx, cluster, client, lease, keep); err != nil {
		return err
	}
	if err := s.rbacSyncFence(ctx, cluster, lease, nil); err != nil {
		return err
	}
	status := "synced"
	if !policy.DesiredEnabled {
		status = "disabled"
	}
	updated := s.db.WithContext(ctx).Model(&model.KubernetesRBACPolicy{}).Where("cluster_id = ? AND revision = ? AND lease_owner = ?", cluster.ID, expected, lease.owner).Updates(map[string]any{"enabled": policy.DesiredEnabled, "status": status, "last_error": "", "updated_at": time.Now().UTC()})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrRBACConflict
	}
	if !policy.DesiredEnabled {
		_ = s.db.WithContext(ctx).Where("cluster_id = ?", cluster.ID).Delete(&model.KubernetesRBACSubject{}).Error
	} else {
		subjects := []model.KubernetesRBACSubject{}
		if err := s.db.WithContext(ctx).Where("cluster_id = ?", cluster.ID).Find(&subjects).Error; err != nil {
			return err
		}
		for _, subject := range subjects {
			if !keep[rbacHash(subject.UserID)[:32]+"/"+subject.PolicyHash[:32]] {
				if err := s.db.WithContext(ctx).Delete(&subject).Error; err != nil {
					return err
				}
			}
		}
	}
	if changed || !policy.DesiredEnabled {
		s.invalidateClusterQueries(cluster.ID)
	}
	return nil
}

func (s *Service) recordRBACSyncFailure(ctx context.Context, clusterID string, revision uint64, err error) {
	if errors.Is(err, errRBACLeaseBusy) {
		return
	}
	failedCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = s.db.WithContext(failedCtx).Model(&model.KubernetesRBACPolicy{}).Where("cluster_id = ? AND revision = ? AND enabled = ?", clusterID, revision, true).Updates(map[string]any{"status": "error", "last_error": safeRBACSyncError(err), "updated_at": time.Now().UTC()}).Error
	s.invalidateClusterQueries(clusterID)
}

func (s *Service) SyncClusterRBAC(ctx context.Context, actorID, clusterID string, expected uint64) (*RBACState, error) {
	cluster, _, err := s.rbacActor(ctx, actorID, clusterID, true)
	if err != nil {
		return nil, err
	}
	policy, _, err := s.readRBACPolicy(ctx, cluster)
	if err != nil {
		return nil, err
	}
	if policy.Revision != expected {
		return nil, ErrRBACConflict
	}
	if err := s.syncRBACCluster(ctx, cluster, expected); err != nil {
		s.recordRBACSyncFailure(ctx, clusterID, expected, err)
	}
	return s.clusterRBACState(ctx, cluster)
}

func (s *Service) RunRBACSynchronization(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var workers sync.WaitGroup
	defer workers.Wait()
	var mutex sync.Mutex
	active := map[string]bool{}
	slots := make(chan struct{}, 4)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		policies := []model.KubernetesRBACPolicy{}
		if err := s.db.WithContext(ctx).Where("enabled = ?", true).Order("CASE WHEN status = 'synced' THEN 1 ELSE 0 END ASC, updated_at ASC").Find(&policies).Error; err != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		for _, policy := range policies {
			mutex.Lock()
			if active[policy.ClusterID] {
				mutex.Unlock()
				continue
			}
			select {
			case slots <- struct{}{}:
			default:
				mutex.Unlock()
				continue
			}
			active[policy.ClusterID] = true
			mutex.Unlock()
			workers.Add(1)
			go func(policy model.KubernetesRBACPolicy) {
				defer workers.Done()
				defer func() { <-slots; mutex.Lock(); delete(active, policy.ClusterID); mutex.Unlock() }()
				clusterCtx := database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: policy.ProjectID, DepartmentID: policy.DepartmentID, AllDepartments: true})
				cluster, err := s.Find(clusterCtx, policy.ClusterID)
				if err != nil || !cluster.IsActive {
					return
				}
				if err := s.syncRBACCluster(clusterCtx, cluster, policy.Revision); err != nil {
					s.recordRBACSyncFailure(clusterCtx, policy.ClusterID, policy.Revision, err)
				}
			}(policy)
		}
	}
}
