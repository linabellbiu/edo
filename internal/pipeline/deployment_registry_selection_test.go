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
	"github.com/google/uuid"
)

// 正向流程显式保存仓库，权限夹具与仓库选择分别建立，不从上游自动补配置。
func savedRegistryExecutionForTest(t *testing.T, s *Service, buildID string) *model.DeploymentExecutionConfig {
	t.Helper()
	registry := createTestImageRegistry(t, s, "已保存部署仓库"+uuid.NewString())
	return &model.DeploymentExecutionConfig{DeploymentRegistryID: registry.ID, ArtifactBuildPlanID: buildID}
}

func authorizeRegistryTestActors(t *testing.T, s *Service, ids ...string) {
	t.Helper()
	for _, id := range ids {
		actor := model.User{ID: id, Username: "registry_test_" + id, DepartmentID: database.DefaultDepartmentID, IsActive: true, IsSuperuser: true}
		if err := s.db.Where("uuid = ?", id).FirstOrCreate(&actor).Error; err != nil {
			t.Fatal(err)
		}
	}
	permissions, err := access.NewService(s.db)
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigureDeliveryAccess(permissions)
}

func TestNewDeploymentRequiresSavedRegistryAndNeverInfersUpstream(t *testing.T) {
	f := newInactiveEnvironmentGuardFixture(t)
	ctx := deliveryTestContext()
	var plan model.DeploymentPlan
	tasks := workflowTasks(f.activeWorkflow.Stages)
	var deploy model.WorkflowNode
	for _, node := range tasks {
		if node.Type == model.WorkflowNodeDeploy {
			deploy = node
		}
	}
	if err := f.db.First(&plan, "uuid = ?", deploy.Config.DeploymentPlanID).Error; err != nil {
		t.Fatal(err)
	}
	savedID := plan.ExecutionConfig.DeploymentRegistryID
	upstream := createTestImageRegistry(t, f.service, "上游另一个仓库")
	for _, node := range tasks {
		if node.Type == model.WorkflowNodeBuild {
			if err := f.db.Model(&model.BuildPlan{}).Where("uuid = ?", node.Config.BuildPlanID).Update("image_registry_id", upstream.ID).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	plan.ExecutionConfig.DeploymentRegistryID = ""
	if err := f.db.Save(&plan).Error; err != nil {
		t.Fatal(err)
	}
	// 即便任务带有客户端提交的建议值，也必须读取方案中已保存的选择。
	for i := range f.activeWorkflow.Stages {
		for j := range f.activeWorkflow.Stages[i].Tasks {
			node := &f.activeWorkflow.Stages[i].Tasks[j]
			if node.Type == model.WorkflowNodeDeploy {
				node.Config.DeploymentRegistryID = upstream.ID
			}
		}
	}
	issues := f.service.validateWorkflow(ctx, f.application, 1, f.activeWorkflow.Source, cloneWorkflowStages(f.activeWorkflow.Stages))
	if !hasWorkflowIssue(issues, "missing_deployment_registry") {
		t.Fatalf("有上游仓库却未保存部署选择时未阻断: %+v", issues)
	}
	run, err := f.service.newResolvedWorkflowRun(ctx, f.application, f.activeWorkflow, f.activeWorkflow.Source, "manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", time.Now())
	if run != nil || !errors.Is(err, ErrDeploymentRegistryRequired) {
		t.Fatalf("未保存仓库仍创建运行: %+v %v", run, err)
	}
	plan.ExecutionConfig.DeploymentRegistryID = savedID
	if err := f.db.Save(&plan).Error; err != nil {
		t.Fatal(err)
	}
	run, err = f.service.newResolvedWorkflowRun(ctx, f.application, f.activeWorkflow, f.activeWorkflow.Source, "manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil || !snapshot.ExplicitDeploymentRegistries {
		t.Fatal("新运行没有固定仓库语义", err)
	}
	for _, node := range workflowTasks(snapshot.Stages) {
		if node.Type == model.WorkflowNodeDeploy && node.Config.DeploymentRegistryID != savedID {
			t.Fatal("上游仓库覆盖了已保存选择")
		}
	}
	plan.ExecutionConfig.DeploymentRegistryID = upstream.ID
	if err := f.db.Save(&plan).Error; err != nil {
		t.Fatal(err)
	}
	old, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range workflowTasks(old.Stages) {
		if node.Type == model.WorkflowNodeDeploy && node.Config.DeploymentRegistryID != savedID {
			t.Fatal("方案修改污染已启动运行")
		}
	}
}

func TestSavedRegistryFreezeRejectsUnavailableCrossProjectAndRevokedAuthority(t *testing.T) {
	s, db, _, _ := newPipelineTestService(t)
	authorizeRegistryTestActors(t, s, "admin")
	ctx := deliveryTestContext()
	registry := createTestImageRegistry(t, s, "选择的仓库")
	for _, kind := range []model.DeploymentPlanKind{model.DeploymentPlanDocker, model.DeploymentPlanCompose, model.DeploymentPlanComposeStack} {
		node := model.WorkflowNode{ID: "deploy"}
		if err := s.freezeSavedDeploymentRegistry(ctx, db, &node, kind, "admin"); !errors.Is(err, ErrDeploymentRegistryRequired) {
			t.Fatalf("%s 缺少仓库未拒绝: %v", kind, err)
		}
		node.Config.DeploymentRegistryID = registry.ID
		if err := s.freezeSavedDeploymentRegistry(ctx, db, &node, kind, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	node := model.WorkflowNode{ID: "deploy", Config: model.WorkflowNodeConfig{DeploymentRegistryID: registry.ID}}
	if err := db.Model(registry).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.freezeSavedDeploymentRegistry(ctx, db, &node, model.DeploymentPlanDocker, "admin"); err == nil {
		t.Fatal("接受了已停用仓库")
	}
	if err := db.Model(registry).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	other := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: database.DefaultDepartmentID, ProjectID: "another-project"})
	if err := s.freezeSavedDeploymentRegistry(other, db, &node, model.DeploymentPlanDocker, "admin"); err == nil {
		t.Fatal("接受了其他项目仓库")
	}
	if err := db.Model(&model.User{}).Where("uuid = ?", "admin").Update("is_superuser", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.freezeSavedDeploymentRegistry(ctx, db, &node, model.DeploymentPlanDocker, "admin"); !errors.Is(err, cicd.ErrExecutionAuthorization) {
		t.Fatal("撤权后接受仓库执行", err)
	}
	if err := s.freezeSavedDeploymentRegistry(ctx, db, &model.WorkflowNode{}, model.DeploymentPlanScript, "admin"); err != nil {
		t.Fatal("自定义部署不应强制镜像仓库", err)
	}
}
