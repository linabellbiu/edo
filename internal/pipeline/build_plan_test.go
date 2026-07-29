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
		DockerfilePath: "deploy/Dockerfile", ContextPath: "services/api", ArtifactPath: "dist",
		TimeoutSeconds: 900,
	})
	if err != nil {
		t.Fatalf("更新构建方案失败: %v", err)
	}
	if plan.Name != "服务镜像" || plan.DockerfilePath != "deploy/Dockerfile" ||
		plan.ContextPath != "services/api" || plan.ArtifactPath != "dist" || plan.TimeoutSeconds != 900 {
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

	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "引用构建方案的应用", RepositoryID: repositoryID, Branch: "main",
		PollEnabled: true, PollIntervalSeconds: 60, WatchPush: true, BuildPlanID: plan.ID,
	})
	if err != nil {
		t.Fatalf("创建引用构建方案的应用失败: %v", err)
	}
	if err := service.DeleteBuildPlan(ctx, plan.ID); !errors.Is(err, ErrBuildPlanInUse) {
		t.Fatalf("仍被应用引用的构建方案允许删除: %v", err)
	}
	if err := db.Model(&model.Application{}).Where("id = ?", application.ID).Update("build_plan_id", "").Error; err != nil {
		t.Fatalf("解除测试应用的构建方案引用失败: %v", err)
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
