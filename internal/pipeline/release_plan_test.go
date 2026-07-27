package pipeline

import (
	"context"
	"errors"
	"testing"

	"zrt/internal/model"
)

func TestReleasePlanContainsOrderedApplicationGroups(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	first, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "订单服务", RepositoryID: repositoryID, Branch: "main", PollEnabled: true, WatchPush: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "库存服务", RepositoryID: repositoryID, Branch: "main", PollEnabled: true, WatchPush: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{Name: "八月发布列车", Version: "2026.08"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = service.CreateReleaseGroup(ctx, plan.ID, ReleaseGroupInput{
		Name: "基础服务", Mode: model.ReleaseGroupParallel, FailurePolicy: model.ReleaseGroupStopOnFailure,
		ApplicationIDs: []string{first.ID, second.ID},
	})
	if err != nil || len(plan.Groups) != 1 || len(plan.Groups[0].Applications) != 2 {
		t.Fatalf("创建并行发布组失败: plan=%+v err=%v", plan, err)
	}
	firstGroupID := plan.Groups[0].ID
	plan, err = service.CreateReleaseGroup(ctx, plan.ID, ReleaseGroupInput{
		Name: "业务服务", Mode: model.ReleaseGroupSequential, FailurePolicy: model.ReleaseGroupContinue,
		ApplicationIDs: []string{second.ID}, DependsOnGroupIDs: []string{firstGroupID},
	})
	if err != nil || len(plan.Groups) != 2 || len(plan.Groups[1].Dependencies) != 1 ||
		plan.Groups[1].Dependencies[0].DependsOnGroupID != firstGroupID {
		t.Fatalf("创建依赖发布组失败: plan=%+v err=%v", plan, err)
	}
	if _, err := service.UpdateReleaseGroup(ctx, plan.ID, firstGroupID, ReleaseGroupInput{
		Name: "基础服务", Mode: model.ReleaseGroupParallel, FailurePolicy: model.ReleaseGroupStopOnFailure,
		ApplicationIDs: []string{first.ID}, DependsOnGroupIDs: []string{plan.Groups[1].ID},
	}); !errors.Is(err, ErrReleaseGroupDependency) {
		t.Fatalf("循环依赖未被拒绝: %v", err)
	}
}

func TestReleasePlanVersionIsUnique(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	ctx := context.Background()
	if _, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{Name: "第一批", Version: "2026.08"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{Name: "第二批", Version: "2026.08"}); !errors.Is(err, ErrReleasePlanExists) {
		t.Fatalf("重复发布版本未被拒绝: %v", err)
	}
}
