package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"edo/internal/access"
	"edo/internal/cicd"
	"edo/internal/database"
	"edo/internal/model"
)

func TestApplicationEnvironmentEntersDeploymentSnapshotWithoutTaskReference(t *testing.T) {
	f := newInactiveEnvironmentGuardFixture(t)
	actor := model.User{ID: "app-env-authority", Username: "app_env_authority", DepartmentID: f.application.DepartmentID, IsSuperuser: true, IsActive: true}
	if err := f.db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	permissions, err := access.NewService(f.db)
	if err != nil {
		t.Fatal(err)
	}
	f.service.ConfigureDeliveryAccess(permissions)
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: f.application.ProjectID, DepartmentID: f.application.DepartmentID})
	auto, value := true, "TOKEN=snapshot-private-value"
	if _, err = f.service.Delivery().SaveVariable(ctx, actor.ID, "", cicd.VariableInput{Name: "APPLICATION_ENVIRONMENT", ApplicationID: f.application.ID, EnvironmentID: f.environmentID, Kind: "env_text", Usage: "deploy", Value: &value, IsActive: true, IsSecret: true, AutoDeploy: &auto}); err != nil {
		t.Fatal(err)
	}
	// 模拟代码事件从已读取的应用恢复范围，不在任务中手工填写变量引用。
	run, err := f.service.newResolvedWorkflowRun(context.Background(), f.application, f.activeWorkflow, f.activeWorkflow.Source, "manual", "refs/heads/main", strings.Repeat("a", 40), actor.ID, "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil || len(snapshot.VariableBindings["deploy"]) != 1 || len(snapshot.VariableBindings["build"]) != 0 || strings.Contains(run.WorkflowSnapshot, value) {
		t.Fatal("自动配置未按部署任务冻结或泄漏明文", err)
	}
	if err = f.db.Model(&model.User{}).Where("uuid = ?", actor.ID).Update("is_superuser", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.newResolvedWorkflowRun(ctx, f.application, f.activeWorkflow, f.activeWorkflow.Source, "manual", "refs/heads/main", strings.Repeat("b", 40), actor.ID, "", time.Now().UTC()); !errors.Is(err, cicd.ErrExecutionAuthorization) {
		t.Fatal("自动配置绕过变量执行权限", err)
	}
}
