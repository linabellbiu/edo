package kube

import (
	"context"
	"errors"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

// Association validation is a control-plane read. Its saved credential must
// not become a way for an ordinary host editor to bypass cluster authorization.
func (s *Service) requireHostValidationAuthorization(ctx context.Context, cluster *model.KubernetesCluster) error {
	policy, _, err := s.readRBACPolicy(ctx, cluster)
	if err != nil {
		return err
	}
	if !policy.Enabled {
		if policy.DesiredEnabled || policy.Status != "disabled" {
			return ErrRBACSyncPending
		}
		return nil
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	actor, err := access.FreshProjectPermissions(s.db, ctx, scope.UserID, cluster.ProjectID)
	if err != nil {
		return ErrRBACForbidden
	}
	if actor.Permissions["kubernetes.rbac.update"] {
		// This is an explicitly authorized management validation, not a data
		// response. The caller receives only the existing identity match result.
		return nil
	}
	err = s.RequireClusterPermission(ctx, cluster.ID, "", "kubernetes.node.read")
	if err == nil || !errors.Is(err, ErrRBACForbidden) {
		return err
	}
	// Node creation also authorizes identity checks needed to associate the
	// newly joined node, with a native authorization fence before this read.
	return s.RequireClusterPermission(ctx, cluster.ID, "", "kubernetes.node.create")
}
