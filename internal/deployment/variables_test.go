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

func TestRollbackVariableAuthorizationUsesCurrentProjectMembershipAndRole(t *testing.T) {
	for _, candidate := range []string{"撤销原授权角色", "撤销回滚发起人成员", "明确拒绝回滚发起人", "停用项目"} {
		t.Run(candidate, func(t *testing.T) {
			db := testutil.NewDatabase(t)
			manager, err := secret.New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("r", 32))))
			if err != nil {
				t.Fatal(err)
			}
			project := database.DefaultProjectID(database.DefaultDepartmentID)
			authority := model.User{ID: "variable-authority", Username: "variable_authority", DepartmentID: database.DefaultDepartmentID, IsActive: true}
			requester := model.User{ID: "variable-requester", Username: "variable_requester", DepartmentID: database.DefaultDepartmentID, IsActive: true}
			for _, actor := range []*model.User{&authority, &requester} {
				role := model.Role{ID: actor.ID + "-role", Name: actor.Username + "_role", DisplayName: actor.Username}
				for _, item := range []any{actor, &role, &model.RolePermission{RoleID: role.ID, Permission: "delivery_variable.execute"}, &model.ProjectMember{ProjectID: project, UserID: actor.ID, RoleID: role.ID}} {
					if err = db.Create(item).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			permissions, err := access.NewService(db)
			if err != nil {
				t.Fatal(err)
			}
			ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: project, DepartmentID: database.DefaultDepartmentID, UserID: requester.ID})
			service := NewService(db, nil, nil, nil, nil, "", nil)
			service.ConfigureReleaseVariables(manager, permissions)
			variables := cicd.New(db, manager, nil)
			value := "private-project-rollback-value"
			if _, err = variables.SaveVariable(ctx, authority.ID, "", cicd.VariableInput{Name: "APP_TOKEN", Kind: "env", Usage: "deploy", Value: &value, IsSecret: true, IsActive: true}); err != nil {
				t.Fatal(err)
			}
			bindings, err := variables.FreezeVariables(ctx, project, "", "", "deploy", "refs/heads/main", []string{"APP_TOKEN"})
			if err != nil {
				t.Fatal(err)
			}
			repository := model.GitRepository{ID: "role-variable-repository", Name: "role_variable_repository"}
			application := model.Application{ID: "role-variable-app", Name: "role_variable_app", RepositoryID: repository.ID}
			for _, item := range []any{&repository, &application} {
				if err = db.WithContext(ctx).Create(item).Error; err != nil {
					t.Fatal(err)
				}
			}
			snapshot, _ := json.Marshal(map[string]any{"authority_id": authority.ID, "variable_bindings": map[string]any{"deploy": bindings}})
			run := model.PipelineRun{ID: "role-variable-run", ApplicationID: application.ID, WorkflowSnapshot: string(snapshot), Ref: "refs/heads/main"}
			if err = db.WithContext(ctx).Create(&run).Error; err != nil {
				t.Fatal(err)
			}
			record := model.DeploymentRecord{ID: "role-variable-deployment", UsesDeliveryVariables: true, ProjectID: project, ApplicationID: application.ID, PipelineRunID: run.ID, WorkflowNodeID: "deploy", RequestedBy: requester.ID}
			if values, err := service.resolveRecordVariables(ctx, &record); err != nil || values["APP_TOKEN"] != value {
				t.Fatalf("有效项目权限不能恢复固定变量: %v", err)
			}
			switch candidate {
			case "撤销原授权角色":
				err = db.Where("role_id = ? AND permission = ?", authority.ID+"-role", "delivery_variable.execute").Delete(&model.RolePermission{}).Error
			case "撤销回滚发起人成员":
				err = db.Where("project_id = ? AND user_id = ?", project, requester.ID).Delete(&model.ProjectMember{}).Error
			case "明确拒绝回滚发起人":
				err = db.Create(&model.UserPermission{UserID: requester.ID, Permission: "delivery_variable.execute", Effect: model.PermissionDeny}).Error
			case "停用项目":
				err = db.Model(&model.Project{}).Where("uuid = ?", project).Update("is_active", false).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, actor := range []*model.User{&authority, &requester} {
				if stale, err := permissions.HasPermission(ctx, actor, "delivery_variable.execute"); err != nil || !stale {
					t.Fatalf("夹具没有保留过期缓存授权: %v", err)
				}
			}
			if values, err := service.resolveRecordVariables(ctx, &record); err == nil || values != nil || strings.Contains(err.Error(), value) {
				t.Fatalf("回滚继续使用已撤销的项目变量授权: %v", err)
			}
		})
	}
}
