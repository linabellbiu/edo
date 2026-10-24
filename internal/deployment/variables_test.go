package deployment

import (
	"context"
	"edo/internal/access"
	"edo/internal/cicd"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/testutil"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestRollbackResolvesFrozenVariablesWithoutSavingPlaintext(t *testing.T) {
	db := testutil.NewDatabase(t)
	manager, err := secret.New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("r", 32))))
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := access.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	actor := model.User{ID: "rollback-variable-actor", Username: "rollback_actor", IsActive: true, IsSuperuser: true}
	if err = db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	project := database.DefaultProjectID(database.DefaultDepartmentID)
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: project, DepartmentID: database.DefaultDepartmentID})
	s := NewService(db, nil, nil, nil, nil, "", nil)
	s.ConfigureReleaseVariables(manager, permissions)
	variables := cicd.New(db, manager, nil)
	value := "private-frozen-value"
	variable, err := variables.SaveVariable(ctx, actor.ID, "", cicd.VariableInput{Name: "APP_TOKEN", Kind: "env", Usage: "deploy", Value: &value, IsSecret: true, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := variables.FreezeVariables(ctx, project, "", "", "deploy", "refs/heads/main", []string{"APP_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	repo := model.GitRepository{ID: "rollback-variable-repository", Name: "rollback_variable_repository"}
	app := model.Application{ID: "rollback-variable-application", Name: "rollback_app", RepositoryID: repo.ID}
	for _, item := range []any{&repo, &app} {
		if err = db.WithContext(ctx).Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	snapshot, _ := json.Marshal(map[string]any{"authority_id": actor.ID, "variable_bindings": map[string]any{"deploy": bindings}})
	run := model.PipelineRun{ID: "rollback-variable-run", ApplicationID: app.ID, WorkflowSnapshot: string(snapshot), Ref: "refs/heads/main"}
	if err = db.WithContext(ctx).Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	record := model.DeploymentRecord{UsesDeliveryVariables: true, ProjectID: project, ApplicationID: app.ID, PipelineRunID: run.ID, WorkflowNodeID: "deploy", RequestedBy: actor.ID}
	values, err := s.resolveRecordVariables(ctx, &record)
	if err != nil || values["APP_TOKEN"] != value {
		t.Fatalf("没有恢复原运行的变量引用: %v", err)
	}
	encoded, _ := json.Marshal(record)
	if strings.Contains(string(encoded), value) {
		t.Fatal("发布记录保存了明文")
	}
	if err = variables.DeleteVariable(ctx, variable.ID, variable.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = s.resolveRecordVariables(ctx, &record); err == nil {
		t.Fatal("已撤销变量仍可用于回滚")
	}
}
