package deployment

import (
	"context"

	"edo/internal/database"
	"edo/internal/model"
)

// Queue workers carry a project scope; the reviewed record supplies the
// original requester. Never replace an existing operator with the record owner.
func deploymentRequesterContext(ctx context.Context, record model.DeploymentRecord) (context.Context, error) {
	scope, scoped := database.DepartmentScopeFromContext(ctx)
	if scoped && (scope.ProjectID != "" && scope.ProjectID != record.ProjectID || scope.DepartmentID != "" && scope.DepartmentID != record.DepartmentID) {
		return nil, ErrDeploymentNotFound
	}
	if scope.UserID != "" || record.RequestedBy == "" {
		return ctx, nil
	}
	scope.UserID, scope.ProjectID, scope.DepartmentID = record.RequestedBy, record.ProjectID, record.DepartmentID
	return database.WithDepartmentScope(ctx, scope), nil
}
