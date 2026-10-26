package deployment

import (
	"context"
	"errors"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
)

func TestDeploymentRequesterContextPreservesOperatorAndProjectBoundary(t *testing.T) {
	record := model.DeploymentRecord{RequestedBy: "original-requester", ProjectID: "project-a", DepartmentID: "department-a"}
	for _, scenario := range []struct {
		name   string
		scope  database.DepartmentScope
		want   string
		denied bool
	}{
		{name: "worker restores original requester", scope: database.DepartmentScope{ProjectID: "project-a", DepartmentID: "department-a"}, want: "original-requester"},
		{name: "operator cannot borrow original requester's privileges", scope: database.DepartmentScope{ProjectID: "project-a", DepartmentID: "department-a", UserID: "current-operator"}, want: "current-operator"},
		{name: "foreign worker project", scope: database.DepartmentScope{ProjectID: "project-b", DepartmentID: "department-a"}, denied: true},
		{name: "foreign operator department", scope: database.DepartmentScope{ProjectID: "project-a", DepartmentID: "department-b", UserID: "current-operator"}, denied: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := database.WithDepartmentScope(context.Background(), scenario.scope)
			actual, err := deploymentRequesterContext(ctx, record)
			if scenario.denied {
				if !errors.Is(err, ErrDeploymentNotFound) {
					t.Fatalf("foreign record accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			scope, _ := database.DepartmentScopeFromContext(actual)
			if scope.UserID != scenario.want || scope.ProjectID != record.ProjectID || scope.DepartmentID != record.DepartmentID {
				t.Fatalf("incorrect execution identity: %+v", scope)
			}
		})
	}
	ctx, err := deploymentRequesterContext(context.Background(), model.DeploymentRecord{ProjectID: "project-a", DepartmentID: "department-a"})
	if err != nil {
		t.Fatal(err)
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	if scope.UserID != "" {
		t.Fatal("missing requester was replaced with a privileged identity")
	}
}
