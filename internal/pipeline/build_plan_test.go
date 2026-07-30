package pipeline

import (
	"context"
	"errors"
	"testing"

	"zrt/internal/model"
)

func TestBuildPlanCanBeUpdatedDisabledAndSoftDeleted(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()

	plan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "原始构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile",
	})
	if err != nil {
		t.Fatalf("创建构建方案失败: %v", err)
	}
	plan, err = service.UpdateBuildPlan(ctx, plan.ID, BuildPlanInput{
		Name: "服务镜像", Kind: model.BuildPlanDockerfile, Description: "构建 API 镜像",
		DockerfilePath: "deploy/Dockerfile", ContextPath: "services/api",
		TimeoutSeconds: 900,
	})
	if err != nil {
		t.Fatalf("更新构建方案失败: %v", err)
	}
	if plan.Name != "服务镜像" || plan.DockerfilePath != "deploy/Dockerfile" ||
		plan.ContextPath != "services/api" || !plan.Pull || !plan.CacheEnabled || plan.TimeoutSeconds != 900 {
		t.Fatalf("构建方案更新内容不完整: %+v", plan)
	}

	if err := service.SetBuildPlanActive(ctx, plan.ID, false); err != nil {
		t.Fatalf("停用构建方案失败: %v", err)
	}
	var disabled model.BuildPlan
	if err := db.First(&disabled, "id = ?", plan.ID).Error; err != nil || disabled.IsActive {
		t.Fatalf("构建方案停用状态未保存: plan=%+v err=%v", disabled, err)
	}
	if err := service.SetBuildPlanActive(ctx, plan.ID, true); err != nil {
		t.Fatalf("重新启用构建方案失败: %v", err)
	}

	target := model.DeploymentTarget{
		ID: "build-plan-delete-target", Name: "构建方案删除测试目标", Platform: model.DeploymentDocker,
		RuntimeID: "local", WorkloadName: "build-plan-delete", RolloutTimeout: 120,
		IsActive: true, CreatedBy: "admin",
	}
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "构建方案删除测试部署", Kind: model.DeploymentPlanDocker,
		ServiceName: "build-plan-delete", TimeoutSeconds: 120, DeploymentTarget: deploymentPlanTargetInput(t, service, target),
	})
	if err != nil {
		t.Fatalf("创建测试部署方案失败: %v", err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "流水线引用构建方案的应用", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatalf("创建测试应用失败: %v", err)
	}
	workflow, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatalf("读取测试流水线失败: %v", err)
	}
	source, stages := testStageWorkflowGraph(plan.ID, deploymentPlan.ID)
	active, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name,
		Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: stages,
	})
	if err != nil {
		t.Fatalf("启用引用构建方案的流水线失败: %v", err)
	}
	if err := service.DeleteBuildPlan(ctx, plan.ID); !errors.Is(err, ErrBuildPlanInUse) {
		t.Fatalf("仍被已启用流水线引用的构建方案允许删除: %v", err)
	}
	if _, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: active.Workflow.Name,
		Revision: active.Workflow.Revision, Source: source, Stages: []model.WorkflowStage{},
	}); err != nil {
		t.Fatalf("停用并解除流水线构建方案引用失败: %v", err)
	}
	if err := service.DeleteBuildPlan(ctx, plan.ID); err != nil {
		t.Fatalf("删除未引用的构建方案失败: %v", err)
	}
	plans, err := service.ListBuildPlans(ctx)
	if err != nil {
		t.Fatalf("查询构建方案失败: %v", err)
	}
	for _, item := range plans {
		if item.ID == plan.ID {
			t.Fatalf("普通列表仍返回已删除构建方案: %+v", item)
		}
	}
	var deleted model.BuildPlan
	if err := db.Unscoped().First(&deleted, "id = ?", plan.ID).Error; err != nil || !deleted.DeletedAt.Valid {
		t.Fatalf("构建方案没有软删除保留: plan=%+v err=%v", deleted, err)
	}
}

func TestScriptBuildPlanPreservesExactBytes(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	original := "\n  set -eu\n  printf 'build' > dist/app  \n\n"
	plan, err := service.CreateBuildPlan(context.Background(), "admin", BuildPlanInput{
		Name: "保留构建脚本字节", Kind: model.BuildPlanScript,
		Script: original, ArtifactPath: "dist", TimeoutSeconds: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Script != original {
		t.Fatalf("构建脚本被静默改写: got=%q want=%q", plan.Script, original)
	}
	if plan.RuntimeImage != model.DefaultRuntimeImage {
		t.Fatalf("脚本构建未使用默认运行镜像: got=%q want=%q", plan.RuntimeImage, model.DefaultRuntimeImage)
	}
}

func TestScriptBuildPlanRuntimeImageMustBePinned(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	valid := []string{
		"alpine:3.22",
		"registry.example.com/team/build:v1.2.3",
		"alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	for index, runtimeImage := range valid {
		plan, err := service.CreateBuildPlan(context.Background(), "admin", BuildPlanInput{
			Name: "固定运行镜像 " + string(rune('A'+index)), Kind: model.BuildPlanScript,
			Script: "printf done > output", ArtifactPath: "output", RuntimeImage: runtimeImage,
		})
		if err != nil || plan.RuntimeImage != runtimeImage {
			t.Fatalf("合法运行镜像被拒绝: image=%q plan=%+v err=%v", runtimeImage, plan, err)
		}
	}

	for index, runtimeImage := range []string{"alpine", "alpine:latest", "registry.example.com/team/build", "not a reference"} {
		_, err := service.CreateBuildPlan(context.Background(), "admin", BuildPlanInput{
			Name: "浮动运行镜像 " + string(rune('A'+index)), Kind: model.BuildPlanScript,
			Script: "printf done > output", ArtifactPath: "output", RuntimeImage: runtimeImage,
		})
		if !errors.Is(err, ErrInvalidBuildPlan) {
			t.Fatalf("非法运行镜像未被拒绝: image=%q err=%v", runtimeImage, err)
		}
	}
}

func TestScriptBuildPlanRejectsReservedEnvironmentVariables(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	for _, name := range []string{
		"CI", "HOME", "TMPDIR", "ZRT_PIPELINE_RUN_ID", "ZRT_APPLICATION_ID", "ZRT_GIT_REF", "ZRT_COMMIT_SHA",
	} {
		_, err := service.CreateBuildPlan(context.Background(), "admin", BuildPlanInput{
			Name: "保留变量 " + name, Kind: model.BuildPlanScript,
			Script: "printf done > output", ArtifactPath: "output",
			EnvironmentVariables: map[string]string{name: "untrusted"},
		})
		if !errors.Is(err, ErrInvalidScriptEnvironment) {
			t.Fatalf("脚本构建允许覆盖系统保留变量 %s: %v", name, err)
		}
	}
}

func TestDockerBuildPlanRejectsDockerRuntimeControlArguments(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	for _, name := range []string{"DOCKER_CONFIG", "DOCKER_HOST", "BUILDX_BUILDER", "BUILDKIT_HOST", "PATH", "HOME"} {
		_, err := service.CreateBuildPlan(context.Background(), "admin", BuildPlanInput{
			Name: "非法参数 " + name, Kind: model.BuildPlanDockerfile,
			DockerfilePath: "Dockerfile", BuildArgs: map[string]string{name: "untrusted"},
		})
		if !errors.Is(err, ErrInvalidBuildPlan) {
			t.Fatalf("构建方案允许覆盖 Docker 运行时变量 %s: %v", name, err)
		}
	}
}
