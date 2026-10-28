package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

const deploymentTestApplicationID = "deployment-test-application"

func deploymentExecutionTestContext(projects ...string) context.Context {
	projectID := database.DefaultProjectID(database.DefaultDepartmentID)
	if len(projects) > 0 {
		projectID = projects[0]
	}
	return database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: projectID, DepartmentID: database.DefaultDepartmentID})
}

func configureDeploymentAuthorizationTestActor(t *testing.T, db *gorm.DB, projectID, departmentID, actorID string, permissions ...string) string {
	t.Helper()
	roleID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("deployment-test-role:"+projectID+":"+actorID)).String()
	resources := []any{
		&model.Project{ID: projectID, DepartmentID: departmentID, Name: "执行权限项目-" + projectID, IsActive: true},
		&model.User{ID: actorID, DepartmentID: departmentID, Username: "deployment-" + actorID, IsActive: true},
		&model.Role{ID: roleID, Name: "deployment-" + roleID, DisplayName: "部署执行测试角色"},
		&model.ProjectMember{ProjectID: projectID, UserID: actorID, RoleID: roleID},
	}
	for _, resource := range resources {
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(resource).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, permission := range permissions {
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.RolePermission{RoleID: roleID, Permission: permission}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return roleID
}

func deploymentPipelineExecutionContext(t *testing.T, db *gorm.DB, target *model.DeploymentTarget, runID string) context.Context {
	t.Helper()
	configureDeploymentAuthorizationTestActor(t, db, target.ProjectID, target.DepartmentID, "operator", "pipeline_run.execute")
	snapshot, err := json.Marshal(map[string]any{"authority_id": "operator"})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{
		&model.GitRepository{ID: "deployment-test-repository", ProjectID: target.ProjectID, DepartmentID: target.DepartmentID, Name: "deployment_test_repository", IsActive: true},
		&model.Application{ID: deploymentTestApplicationID, RepositoryID: "deployment-test-repository", ProjectID: target.ProjectID, DepartmentID: target.DepartmentID, Name: "deployment_test_app", IsActive: true},
		&model.PipelineRun{ID: runID, ProjectID: target.ProjectID, DepartmentID: target.DepartmentID, ApplicationID: deploymentTestApplicationID, CreatedBy: "operator", WorkflowSnapshot: string(snapshot)},
	} {
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	return database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: target.ProjectID, DepartmentID: target.DepartmentID})
}

func TestDeploymentExecutionAuthorizationRejectsRevocationWithoutVariables(t *testing.T) {
	for _, mutation := range []string{"当前角色有效", "移除成员", "撤销角色动作", "个人拒绝", "停用账户", "停用项目", "缺少请求人"} {
		t.Run(mutation, func(t *testing.T) {
			f := newKubernetesRollbackFixture(t, model.DeploymentQueued)
			roleID := configureDeploymentAuthorizationTestActor(t, f.db, f.source.ProjectID, f.source.DepartmentID, "independent-requester", access.PermissionDeploymentExecute)
			if err := f.db.Model(&model.DeploymentRecord{}).Where("uuid = ?", f.source.ID).Updates(map[string]any{"requested_by": "independent-requester", "pipeline_run_id": "", "application_id": "", "workflow_node_id": ""}).Error; err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "移除成员":
				if err := f.db.Where("project_id = ? AND user_id = ?", f.source.ProjectID, "independent-requester").Delete(&model.ProjectMember{}).Error; err != nil {
					t.Fatal(err)
				}
			case "撤销角色动作":
				if err := f.db.Where("role_id = ? AND permission = ?", roleID, access.PermissionDeploymentExecute).Delete(&model.RolePermission{}).Error; err != nil {
					t.Fatal(err)
				}
			case "个人拒绝":
				if err := f.db.Create(&model.UserPermission{UserID: "independent-requester", Permission: access.PermissionDeploymentExecute, Effect: model.PermissionDeny}).Error; err != nil {
					t.Fatal(err)
				}
			case "停用账户":
				if err := f.db.Model(&model.User{}).Where("uuid = ?", "independent-requester").Update("is_active", false).Error; err != nil {
					t.Fatal(err)
				}
			case "停用项目":
				if err := f.db.Model(&model.Project{}).Where("uuid = ?", f.source.ProjectID).Update("is_active", false).Error; err != nil {
					t.Fatal(err)
				}
			case "缺少请求人":
				if err := f.db.Model(&model.DeploymentRecord{}).Where("uuid = ?", f.source.ID).Update("requested_by", "").Error; err != nil {
					t.Fatal(err)
				}
			}
			executionErr := f.service.Run(deploymentExecutionTestContext(f.source.ProjectID), f.source.ID)
			if mutation == "当前角色有效" {
				if executionErr != nil || f.writes != 1 {
					t.Fatalf("当前有权限的独立发布无法执行: writes=%d err=%v", f.writes, executionErr)
				}
				return
			}
			if !errors.Is(executionErr, ErrExecutionAuthorization) {
				t.Fatalf("无变量的独立发布沿用了已失效授权: %v", executionErr)
			}
			var record model.DeploymentRecord
			if err := f.db.Where("uuid = ?", f.source.ID).First(&record).Error; err != nil || record.Status != model.DeploymentFailed || record.ErrorCode != "deployment_authorization_revoked" || record.UsesDeliveryVariables || f.writes != 0 {
				t.Fatalf("拒绝授权后仍发布或未记录稳定失败: status=%s code=%s writes=%d err=%v", record.Status, record.ErrorCode, f.writes, err)
			}
		})
	}
}

func TestDeploymentExecutionAuthorizationRejectsMissingOrForeignScope(t *testing.T) {
	for _, name := range []string{"缺少范围", "缺少项目", "缺少部门", "外项目", "外部门", "不能借用原请求人"} {
		t.Run(name, func(t *testing.T) {
			f := newKubernetesRollbackFixture(t, model.DeploymentQueued)
			scope := database.DepartmentScope{ProjectID: f.source.ProjectID, DepartmentID: f.source.DepartmentID}
			ctx := context.Background()
			switch name {
			case "缺少项目":
				scope.ProjectID = ""
			case "缺少部门":
				scope.DepartmentID = ""
			case "外项目":
				scope.ProjectID = uuid.NewString()
			case "外部门":
				scope.DepartmentID = uuid.NewString()
				scope.AllDepartments = true
			case "不能借用原请求人":
				scope.UserID = "unauthorized-operator"
			}
			if name != "缺少范围" {
				ctx = database.WithDepartmentScope(ctx, scope)
			}
			if err := f.service.Run(ctx, f.source.ID); !errors.Is(err, ErrExecutionAuthorization) && !errors.Is(err, ErrDeploymentNotFound) && !errors.Is(err, database.ErrDepartmentScopeRequired) {
				t.Fatalf("范围或操作者不匹配仍被接受: %v", err)
			}
			var record model.DeploymentRecord
			if err := f.db.Where("uuid = ?", f.source.ID).First(&record).Error; err != nil || f.writes != 0 {
				t.Fatalf("无效范围触发外部操作: writes=%d err=%v", f.writes, err)
			}
			if name != "不能借用原请求人" && record.Status != model.DeploymentQueued {
				t.Fatal("缺失或错误范围改写了原记录")
			}
		})
	}
}

func TestPipelineDeploymentExecutionUsesFrozenAuthorityWithoutManualGrant(t *testing.T) {
	for _, mutation := range []string{"仅流水线执行权限", "撤销快照授权人", "快照缺少授权人", "运行属于其他项目", "请求人与运行不一致"} {
		t.Run(mutation, func(t *testing.T) {
			f := newKubernetesRollbackFixture(t, model.DeploymentQueued)
			configureDeploymentAuthorizationTestActor(t, f.db, f.source.ProjectID, f.source.DepartmentID, "pipeline-authority", "pipeline_run.execute")
			if err := f.db.Model(&model.DeploymentRecord{}).Where("uuid = ?", f.source.ID).Update("requested_by", "system").Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Model(&model.PipelineRun{}).Where("uuid = ?", f.source.PipelineRunID).Updates(map[string]any{"created_by": "system", "workflow_snapshot": `{"authority_id":"pipeline-authority"}`}).Error; err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "撤销快照授权人":
				if err := f.db.Create(&model.UserPermission{UserID: "pipeline-authority", Permission: "pipeline_run.execute", Effect: model.PermissionDeny}).Error; err != nil {
					t.Fatal(err)
				}
			case "快照缺少授权人":
				if err := f.db.Model(&model.PipelineRun{}).Where("uuid = ?", f.source.PipelineRunID).Update("workflow_snapshot", `{}`).Error; err != nil {
					t.Fatal(err)
				}
			case "运行属于其他项目":
				if err := f.db.Model(&model.PipelineRun{}).Where("uuid = ?", f.source.PipelineRunID).Update("project_id", uuid.NewString()).Error; err != nil {
					t.Fatal(err)
				}
			case "请求人与运行不一致":
				if err := f.db.Model(&model.DeploymentRecord{}).Where("uuid = ?", f.source.ID).Update("requested_by", "operator").Error; err != nil {
					t.Fatal(err)
				}
			}
			err := f.service.Run(deploymentExecutionTestContext(f.source.ProjectID), f.source.ID)
			if mutation == "仅流水线执行权限" {
				if err != nil || f.writes != 1 {
					t.Fatalf("自动流水线被额外要求手动部署权限: writes=%d err=%v", f.writes, err)
				}
			} else if !errors.Is(err, ErrExecutionAuthorization) || f.writes != 0 {
				t.Fatalf("无效流水线授权产生了发布: writes=%d err=%v", f.writes, err)
			}
		})
	}
}

func TestRollbackExecutionChecksCurrentRequesterAndAllowsAuthorizedRecovery(t *testing.T) {
	for _, phase := range []string{"旧授权人失效仍可恢复", "创建前撤权", "排队后撤权"} {
		t.Run(phase, func(t *testing.T) {
			f := newKubernetesRollbackFixture(t, model.DeploymentSucceeded)
			roleID := configureDeploymentAuthorizationTestActor(t, f.db, f.source.ProjectID, f.source.DepartmentID, "recovery-operator", access.PermissionDeploymentExecute)
			// 回退依据当前操作者的恢复权限，不依赖来源流水线过去的授权人仍然有效。
			if err := f.db.Model(&model.PipelineRun{}).Where("uuid = ?", f.source.PipelineRunID).Update("workflow_snapshot", `{"authority_id":"deleted-original-authority"}`).Error; err != nil {
				t.Fatal(err)
			}
			revoke := func() {
				t.Helper()
				if err := f.db.Where("role_id = ?", roleID).Delete(&model.RolePermission{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if phase == "创建前撤权" {
				revoke()
			}
			record, err := f.service.RollbackChecked(deploymentExecutionTestContext(f.source.ProjectID), f.source.ID, "recovery-operator", f.confirmation())
			if phase == "创建前撤权" {
				if !errors.Is(err, ErrExecutionAuthorization) || record != nil {
					t.Fatalf("撤权后仍创建回退: record=%v err=%v", record, err)
				}
				var jobs int64
				if err := f.db.Model(&model.Job{}).Count(&jobs).Error; err != nil || jobs != 0 {
					t.Fatalf("拒绝后仍投递回退任务: count=%d err=%v", jobs, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var job model.Job
			if err := f.db.Where("uuid = ?", record.JobID).First(&job).Error; err != nil || job.MaxAttempts != 1 {
				t.Fatalf("回退任务不应自动重试: attempts=%d err=%v", job.MaxAttempts, err)
			}
			if phase == "排队后撤权" {
				revoke()
			}
			err = f.service.Run(deploymentExecutionTestContext(f.source.ProjectID), record.ID)
			if phase == "排队后撤权" {
				if !errors.Is(err, ErrExecutionAuthorization) || f.writes != 0 {
					t.Fatalf("排队期间撤权后仍回退: writes=%d err=%v", f.writes, err)
				}
			} else if err != nil || f.writes != 1 {
				t.Fatalf("当前有权操作者无法恢复历史发布: writes=%d err=%v", f.writes, err)
			}
		})
	}
}

func TestCommandDeploymentRechecksAuthorizationBeforeSideEffect(t *testing.T) {
	service, db, _ := newDeploymentTestService(t)
	environment, host := createSSHDeploymentResources(t, db)
	target, err := service.CreateTarget(context.Background(), "operator", TargetInput{Name: "执行前复核", Platform: model.DeploymentSSH, EnvironmentID: environment.ID, HostID: host.ID, RolloutTimeout: 30})
	if err != nil {
		t.Fatal(err)
	}
	ctx := deploymentPipelineExecutionContext(t, db, target, "command-authorization-run")
	runner := &idempotentHostScriptRunnerStub{}
	service.ssh = runner
	const callback = "test:revoke_command_authority_after_claim"
	if err := db.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "deployment_records" {
			return
		}
		updates, ok := tx.Statement.Dest.(map[string]any)
		if !ok || updates["status"] != model.DeploymentRunning {
			return
		}
		if err := tx.Session(&gorm.Session{NewDB: true}).Where("user_id = ? AND project_id = ?", "operator", target.ProjectID).Delete(&model.ProjectMember{}).Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	script := "printf ok"
	record, err := service.RequestCommandSnapshotAndRun(ctx, "operator", *target, CommandRequestInput{ApplicationID: deploymentTestApplicationID, TargetID: target.ID, PipelineRunID: "command-authorization-run", WorkflowNodeID: "deploy", DeploymentPlanID: "command-auth-plan", PlanKind: model.DeploymentPlanScript, Script: script, ScriptDigest: model.DeploymentPlanExecutionDigest(model.DeploymentPlanScript, script, 30), TimeoutSeconds: 30})
	if !errors.Is(err, ErrExecutionAuthorization) || record == nil || record.Status != model.DeploymentFailed || record.ErrorCode != "deployment_authorization_revoked" || runner.calls.Load() != 0 {
		t.Fatalf("命令外部执行前未复核授权: calls=%d err=%v", runner.calls.Load(), err)
	}
	if _, err := service.RequestCommandSnapshotAndRun(ctx, "operator", *target, CommandRequestInput{ApplicationID: deploymentTestApplicationID, TargetID: target.ID, PipelineRunID: "command-authorization-run", WorkflowNodeID: "deploy", DeploymentPlanID: "command-auth-plan", PlanKind: model.DeploymentPlanScript, Script: script, ScriptDigest: model.DeploymentPlanExecutionDigest(model.DeploymentPlanScript, script, 30), TimeoutSeconds: 30}); !errors.Is(err, ErrPipelineReleaseFailed) {
		t.Fatalf("授权失败被隐式重试: %v", err)
	}
	if runner.calls.Load() != 0 {
		t.Fatal("重投产生命令副作用")
	}
}

func TestSucceededDeploymentReplayDoesNotRequireOldGrantOrRepeatSideEffect(t *testing.T) {
	f := newKubernetesRollbackFixture(t, model.DeploymentSucceeded)
	if err := f.db.Where("project_id = ? AND user_id = ?", f.source.ProjectID, "operator").Delete(&model.ProjectMember{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.service.Run(deploymentExecutionTestContext(f.source.ProjectID), f.source.ID); err != nil {
		t.Fatalf("已成功的发布重投不应在撤权后改判失败: %v", err)
	}
	var stored model.DeploymentRecord
	if err := f.db.Where("uuid = ?", f.source.ID).First(&stored).Error; err != nil || stored.Status != model.DeploymentSucceeded || stored.ErrorCode != "" || f.writes != 0 {
		t.Fatalf("成功记录重投改变历史或执行副作用: status=%s writes=%d err=%v", stored.Status, f.writes, err)
	}
}
