package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"edo/internal/access"
	"edo/internal/cicd"
	"edo/internal/database"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/testutil"
)

func TestPipelineInjectsFrozenVariablesAndRedactsActualValue(t *testing.T) {
	node := parallelShell("test")
	snapshot := workflowSnapshot{SchemaVersion: 1, ExecutionVersion: 2, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "ci", Tasks: []model.WorkflowNode{node}}}}
	s, db, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"code.txt": "source"})
	actor := model.User{ID: "delivery-authority", Username: "delivery_authority", IsSuperuser: true, IsActive: true}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	permissions, err := access.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigureDeliveryAccess(permissions)
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, UserID: actor.ID})
	value := "private-actual-variable"
	input := cicd.VariableInput{Name: "TOKEN_WITHOUT_HINT", Kind: "env", Usage: "build", Value: &value, IsSecret: true, IsActive: true}
	variable, err := s.Delivery().SaveVariable(ctx, actor.ID, "", input)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := s.Delivery().FreezeVariables(ctx, run.ProjectID, run.ApplicationID, "", "build", run.Ref, []string{input.Name})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.AuthorityID = actor.ID
	snapshot.VariableBindings = map[string][]model.DeliveryVariableBinding{node.ID: bindings}
	raw, _ := json.Marshal(snapshot)
	if strings.Contains(string(raw), value) {
		t.Fatal("运行快照保存了密钥明文")
	}
	if err = db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("workflow_snapshot", string(raw)).Error; err != nil {
		t.Fatal(err)
	}
	replacement := "replacement-private-value"
	input.Value = &replacement
	input.ExpectedVersion = variable.Version
	if _, err = s.Delivery().SaveVariable(ctx, actor.ID, variable.ID, input); err != nil {
		t.Fatal(err)
	}
	s.scriptRunner = parallelScriptRunner(func(_ context.Context, in dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error) {
		if in.Environment["TOKEN_WITHOUT_HINT"] != value {
			t.Fatal("没有注入运行创建时的变量版本")
		}
		_, _ = in.Stdout.Write([]byte("value=" + value + "\n"))
		return dockerengine.ScriptContainerResult{}, nil
	})
	if _, err = s.AdvanceRun(ctx, run.ID, actor.ID, ""); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodeRuns(ctx, run.ID)
	if err != nil || len(nodes) != 1 {
		t.Fatal(err)
	}
	if err = s.ExecuteBuildTask(ctx, BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}, nodes[0].JobID); err != nil {
		t.Fatal(err)
	}
	var logs []model.PipelineRunLog
	if err = db.Where("pipeline_run_id = ?", run.ID).Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(logs)
	if strings.Contains(string(encoded), value) || !strings.Contains(string(encoded), "已脱敏") {
		t.Fatal("日志没有按密钥实际值脱敏")
	}
}

func TestDeliveryPermissionUsesCurrentProjectRoleAfterRevocation(t *testing.T) {
	for _, candidate := range []string{"移除角色权限", "移除项目成员", "个人明确拒绝", "停用项目", "停用用户"} {
		t.Run(candidate, func(t *testing.T) {
			db := testutil.NewDatabase(t)
			projectID := database.DefaultProjectID(database.DefaultDepartmentID)
			actor := model.User{ID: "delivery-role-actor", Username: "delivery_role_actor", DepartmentID: database.DefaultDepartmentID, IsActive: true}
			role := model.Role{ID: "delivery-variable-role", Name: "delivery_variable_role", DisplayName: "交付变量角色"}
			for _, item := range []any{
				&actor, &role,
				&model.RolePermission{RoleID: role.ID, Permission: "delivery_variable.execute"},
				&model.ProjectMember{ProjectID: projectID, UserID: actor.ID, RoleID: role.ID},
			} {
				if err := db.Create(item).Error; err != nil {
					t.Fatal(err)
				}
			}
			permissions, err := access.NewService(db)
			if err != nil {
				t.Fatal(err)
			}
			service := NewService(db, nil, nil)
			service.ConfigureDeliveryAccess(permissions)
			ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: projectID, DepartmentID: actor.DepartmentID})
			if err = service.CheckDeliveryPermission(ctx, actor.ID, "delivery_variable.execute"); err != nil {
				t.Fatalf("有效项目角色不能使用变量: %v", err)
			}
			switch candidate {
			case "移除角色权限":
				err = db.Where("role_id = ? AND permission = ?", role.ID, "delivery_variable.execute").Delete(&model.RolePermission{}).Error
			case "移除项目成员":
				err = db.Where("project_id = ? AND user_id = ?", projectID, actor.ID).Delete(&model.ProjectMember{}).Error
			case "个人明确拒绝":
				err = db.Create(&model.UserPermission{UserID: actor.ID, Permission: "delivery_variable.execute", Effect: model.PermissionDeny}).Error
			case "停用项目":
				err = db.Model(&model.Project{}).Where("uuid = ?", projectID).Update("is_active", false).Error
			case "停用用户":
				err = db.Model(&model.User{}).Where("uuid = ?", actor.ID).Update("is_active", false).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			// 故意不重载 Casbin，模拟权限刚撤销、后台消息随后到达。
			if stale, err := permissions.HasPermission(ctx, &actor, "delivery_variable.execute"); err != nil || !stale {
				t.Fatalf("夹具没有保留过期缓存授权: %v", err)
			}
			if err = service.CheckDeliveryPermission(ctx, actor.ID, "delivery_variable.execute"); !errors.Is(err, cicd.ErrExecutionAuthorization) {
				t.Fatalf("后台执行沿用了过期项目角色: %v", err)
			}
		})
	}
}

func TestDeliveryPermissionRequiresRoleInTheSelectedProject(t *testing.T) {
	db := testutil.NewDatabase(t)
	projectID := database.DefaultProjectID(database.DefaultDepartmentID)
	actor := model.User{ID: "delivery-project-actor", Username: "delivery_project_actor", DepartmentID: database.DefaultDepartmentID, IsActive: true}
	role := model.Role{ID: "delivery-project-writer", Name: "delivery_project_writer", DisplayName: "变量编辑角色"}
	other := model.Project{ID: "delivery-project-other", DepartmentID: actor.DepartmentID, Name: "其他交付项目", IsActive: true}
	for _, item := range []any{&actor, &role, &other, &model.RolePermission{RoleID: role.ID, Permission: "delivery_variable.update"}, &model.ProjectMember{ProjectID: projectID, UserID: actor.ID, RoleID: role.ID}} {
		if err := db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	permissions, err := access.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.ConfigureDeliveryAccess(permissions)
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: projectID, DepartmentID: actor.DepartmentID})
	if err = service.CheckDeliveryPermission(ctx, actor.ID, "delivery_variable.update"); err != nil {
		t.Fatal(err)
	}
	foreign := database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: other.ID, DepartmentID: actor.DepartmentID})
	if err = service.CheckDeliveryPermission(foreign, actor.ID, "delivery_variable.update"); !errors.Is(err, cicd.ErrExecutionAuthorization) {
		t.Fatalf("一个项目的角色权限用于修改其他项目: %v", err)
	}
	if err = service.CheckDeliveryPermission(context.Background(), actor.ID, "delivery_variable.update"); !errors.Is(err, cicd.ErrExecutionAuthorization) {
		t.Fatalf("未选项目仍可修改交付配置: %v", err)
	}
}
