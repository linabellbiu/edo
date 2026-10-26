package kube

import (
	"errors"
	"testing"

	"edo/internal/model"
)

func TestHostAssociationValidationCannotBorrowSavedClusterPrivilege(t *testing.T) {
	f := newRBACFixture(t)
	if err := f.service.requireHostValidationAuthorization(f.userCtx, f.cluster); err != nil {
		t.Fatalf("legacy mode changed: %v", err)
	}
	f.enable(t)
	if err := f.service.requireHostValidationAuthorization(f.adminCtx, f.cluster); err != nil {
		t.Fatalf("explicit bootstrap management unavailable: %v", err)
	}
	if err := f.service.requireHostValidationAuthorization(f.userCtx, f.cluster); err != nil {
		t.Fatalf("fresh RBAC manager cannot validate host association: %v", err)
	}
	if err := f.service.db.Create(&model.UserPermission{UserID: f.userID, Permission: "kubernetes.rbac.update", Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	state, err := f.service.ListClusterRBAC(f.adminCtx, f.cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision); err != nil {
		t.Fatal(err)
	}
	err = f.service.validateHosts(f.userCtx, f.cluster, []model.Host{{ID: "candidate-host"}}, nil)
	if !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("host editor borrowed management credentials: %v", err)
	}
}
