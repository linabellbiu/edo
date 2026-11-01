package pipeline

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/environment"
	"edo/internal/model"
)

func TestInactiveEnvironmentRejectsExistingDeploymentPlanActivation(t *testing.T) {
	f := newInactiveEnvironmentGuardFixture(t)
	ctx := context.Background()
	if err := f.service.SetDeploymentPlanActive(ctx, f.plan.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := f.service.SetDeploymentPlanActive(ctx, f.plan.ID, true); err != nil {
		t.Fatalf("环境启用时正常启用方案失败，不能用此前提失败判 RED: %v", err)
	}
	if err := f.service.SetDeploymentPlanActive(ctx, f.plan.ID, false); err != nil {
		t.Fatal(err)
	}
	f.disableEnvironment(t)
	if err := f.service.SetDeploymentPlanActive(ctx, f.plan.ID, true); err == nil {
		t.Error("停用环境仍允许正常启用旧部署方案")
	}
	var current model.DeploymentPlan
	if err := f.db.First(&current, "uuid = ?", f.plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.IsActive {
		t.Error("被拒绝的启用操作改变了部署方案状态")
	}
}

func TestInactiveEnvironmentRejectsWorkflowValidationAndActivation(t *testing.T) {
	for _, operation := range []string{"validate", "activate"} {
		t.Run(operation, func(t *testing.T) {
			f := newInactiveEnvironmentGuardFixture(t)
			f.disableEnvironment(t)
			input := inactiveEnvironmentWorkflowInput(f.draft, true)
			var result *WorkflowResult
			var err error
			if operation == "validate" {
				result, err = f.service.ValidateApplicationWorkflow(context.Background(), f.application.ID, f.draft.ID, input)
				if err != nil {
					t.Fatalf("正常校验入口失败而非返回配置问题: %v", err)
				}
			} else {
				result, err = f.service.SaveApplicationWorkflow(context.Background(), f.application.ID, f.draft.ID, "admin", input)
				if !errors.Is(err, ErrInvalidWorkflow) {
					t.Errorf("启用引用停用环境的工作流未返回配置拒绝: %v", err)
				}
			}
			if result == nil || result.Valid || len(result.Issues) == 0 {
				t.Errorf("停用环境未产生工作流校验问题: result=%+v", result)
			}
			var current model.ReleaseWorkflow
			if err := f.db.First(&current, "uuid = ?", f.draft.ID).Error; err != nil {
				t.Fatal(err)
			}
			if current.IsActive || current.Revision != f.draft.Revision ||
				!reflect.DeepEqual(current.Source, f.draft.Source) || !reflect.DeepEqual(current.Stages, f.draft.Stages) {
				t.Error("校验或拒绝启用后改变了旧工作流状态、版本或业务图")
			}
		})
	}
}

func TestInactiveEnvironmentRejectsNewSnapshotWithoutChangingFrozenSnapshot(t *testing.T) {
	f := newInactiveEnvironmentGuardFixture(t)
	ctx := context.Background()
	// activeWorkflow 是正常启用成功时读到的 DTO，用于模拟执行请求先读配置后环境被停用。
	// 此处直接调用既有快照构造边界，不宣称模拟了 HTTP 并发或执行中的 worker。
	old, err := f.service.newResolvedWorkflowRun(ctx, f.application, f.activeWorkflow, f.activeWorkflow.Source,
		"manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("环境启用时合法快照前提失败: %v", err)
	}
	frozen := old.WorkflowSnapshot
	if frozen == "" {
		t.Fatal("合法运行没有冻结配置")
	}
	f.disableEnvironment(t)
	next, err := f.service.newResolvedWorkflowRun(ctx, f.application, f.activeWorkflow, f.activeWorkflow.Source,
		"manual", "refs/heads/main", strings.Repeat("b", 40), "admin", "", time.Now().UTC())
	if !errors.Is(err, ErrInvalidWorkflow) || next != nil {
		t.Errorf("新快照仍接受停用环境: next_present=%t err=%v", next != nil, err)
	}
	if old.WorkflowSnapshot != frozen {
		t.Fatal("环境状态变化修改了已冻结的运行快照")
	}
	snapshot, err := parseWorkflowSnapshot(old)
	if err != nil {
		t.Fatalf("原冻结快照不再可读: %v", err)
	}
	if snapshot.DeploymentTargets["deploy"].EnvironmentID != f.environmentID {
		t.Fatal("原快照未保留原环境身份")
	}
}

func TestReusableDeploymentPlanActivationDoesNotRequireEnvironment(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	plan, err := service.CreateDeploymentPlan(context.Background(), "admin", DeploymentPlanInput{
		Name: "无目标通用方案正向", Kind: model.DeploymentPlanDocker, Reusable: true, TimeoutSeconds: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.DeploymentTargetID != "" {
		t.Fatal("通用方案意外生成执行目标")
	}
	if err := service.SetDeploymentPlanActive(context.Background(), plan.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := service.SetDeploymentPlanActive(context.Background(), plan.ID, true); err != nil {
		t.Fatalf("不应要求尚未绑定执行目标的通用方案选择环境: %v", err)
	}
}

type inactiveEnvironmentGuardFixture struct {
	service        *Service
	db             *gorm.DB
	application    *model.Application
	plan           *model.DeploymentPlan
	draft          *model.ReleaseWorkflow
	activeWorkflow *model.ReleaseWorkflow
	environmentID  string
}

func newInactiveEnvironmentGuardFixture(t *testing.T) inactiveEnvironmentGuardFixture {
	t.Helper()
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	build, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "环境启用边界构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "环境启用边界部署", Kind: model.DeploymentPlanDocker, TimeoutSeconds: 120,
		DeploymentTarget: deploymentPlanTargetInput(t, service, model.DeploymentTarget{
			Name: "环境启用边界目标", Platform: model.DeploymentDocker, RuntimeID: "local", RolloutTimeout: 120,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "inactive_environment_fixture", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, stages := testStageWorkflowGraph(build.ID, plan.ID)
	active, err := service.SaveApplicationWorkflow(ctx, application.ID, workflow.Workflow.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name, Revision: workflow.Workflow.Revision,
		Activate: true, Source: source, Stages: stages,
	})
	if err != nil || active == nil || !active.Valid || active.Workflow == nil || !active.Workflow.IsActive {
		t.Fatalf("完整有效工作流不能正常启用，不能将此算作环境 RED: result=%+v err=%v", active, err)
	}
	application, err = service.FindApplication(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := service.SaveApplicationWorkflow(ctx, application.ID, active.Workflow.ID, "admin",
		inactiveEnvironmentWorkflowInput(active.Workflow, false))
	if err != nil || draft == nil || !draft.Valid || draft.Workflow == nil || draft.Workflow.IsActive {
		t.Fatalf("正常停用工作流前提失败: result=%+v err=%v", draft, err)
	}
	if plan.DeploymentTarget == nil || plan.DeploymentTarget.EnvironmentID == "" {
		t.Fatal("方案缺少已正常创建的环境目标")
	}
	t.Cleanup(func() {
		for _, record := range []any{&model.PipelineRun{}, &model.Job{}} {
			var count int64
			if err := db.Model(record).Count(&count).Error; err != nil || count != 0 {
				t.Errorf("配置测试意外持久化运行或任务: model=%T count=%d err=%v", record, count, err)
			}
		}
	})
	return inactiveEnvironmentGuardFixture{service: service, db: db, application: application, plan: plan,
		draft: draft.Workflow, activeWorkflow: active.Workflow, environmentID: plan.DeploymentTarget.EnvironmentID}
}

func (f inactiveEnvironmentGuardFixture) disableEnvironment(t *testing.T) {
	t.Helper()
	if err := environment.NewService(f.db).SetActive(context.Background(), f.environmentID, false); err != nil {
		t.Fatalf("正常停用环境前提失败: %v", err)
	}
	var target model.DeploymentTarget
	if err := f.db.First(&target, "uuid = ?", f.plan.DeploymentTargetID).Error; err != nil || !target.IsActive {
		t.Fatalf("旧目标应仍启用，以隔离停用环境边界: active=%t err=%v", target.IsActive, err)
	}
}

func inactiveEnvironmentWorkflowInput(workflow *model.ReleaseWorkflow, active bool) WorkflowInput {
	return WorkflowInput{SchemaVersion: workflow.SchemaVersion, Name: workflow.Name, Revision: workflow.Revision,
		Activate: active, Source: workflow.Source, Stages: workflow.Stages}
}
