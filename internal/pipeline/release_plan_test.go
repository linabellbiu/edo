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
	commitSHA := "0123456789012345678901234567890123456789"
	plan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Name: "八月发布列车", Version: "2026.08", Description: "八月应用发布",
		Applications: []ReleaseApplicationInput{
			{ApplicationID: first.ID, ManualDeploy: true, SourceType: model.ReleaseApplicationSourceBranch, SourceValue: "main"},
			{ApplicationID: second.ID, ManualDeploy: true, SourceType: model.ReleaseApplicationSourceCommit, SourceValue: commitSHA},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 1 || len(plan.Groups[0].Applications) != 2 ||
		plan.Groups[0].Applications[0].SourceValue != "main" ||
		plan.Groups[0].Applications[1].SourceValue != commitSHA {
		t.Fatalf("创建发布计划时未保存应用及手动版本来源: %+v", plan)
	}
	firstGroupID := plan.Groups[0].ID
	plan, err = service.UpdateReleaseGroup(ctx, plan.ID, firstGroupID, ReleaseGroupInput{
		Name: "基础服务", Mode: model.ReleaseGroupParallel, FailurePolicy: model.ReleaseGroupStopOnFailure,
		Applications: []ReleaseApplicationInput{
			{ApplicationID: first.ID, ManualDeploy: true, SourceType: model.ReleaseApplicationSourceBranch, SourceValue: "main"},
			{ApplicationID: second.ID},
		},
	})
	if err != nil || len(plan.Groups) != 1 || len(plan.Groups[0].Applications) != 2 ||
		!plan.Groups[0].Applications[0].ManualDeploy || plan.Groups[0].Applications[0].SourceValue != "main" ||
		plan.Groups[0].Applications[1].ManualDeploy || plan.Groups[0].Applications[1].SourceValue != "" {
		t.Fatalf("创建并行发布组失败: plan=%+v err=%v", plan, err)
	}
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

func TestReleasePlanGeneratesInternalIdentity(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "发布应用", RepositoryID: repositoryID, Branch: "main", PollEnabled: true, WatchPush: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Description: "修复订单查询并发布", Applications: []ReleaseApplicationInput{{ApplicationID: application.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Description != "修复订单查询并发布" || plan.Name == "" || plan.Version == "" {
		t.Fatalf("发布计划没有生成内部标识: %+v", plan)
	}
	if _, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Applications: []ReleaseApplicationInput{{ApplicationID: application.ID}},
	}); !errors.Is(err, ErrInvalidReleasePlan) {
		t.Fatalf("缺少说明的发布计划应被拒绝: %v", err)
	}
}

func TestReleasePlanVersionIsUnique(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "发布应用", RepositoryID: repositoryID, Branch: "main", PollEnabled: true, WatchPush: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	applications := []ReleaseApplicationInput{{ApplicationID: application.ID}}
	if _, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{Name: "第一批", Version: "2026.08", Description: "第一批发布", Applications: applications}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{Name: "第二批", Version: "2026.08", Description: "第二批发布", Applications: applications}); !errors.Is(err, ErrReleasePlanExists) {
		t.Fatalf("重复发布版本未被拒绝: %v", err)
	}
}

func TestReleasePlanRejectsInvalidManualSource(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "手动发布应用", RepositoryID: repositoryID, Branch: "main", PollEnabled: true, WatchPush: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Name: "错误版本来源", Version: "2026.09", Description: "验证错误版本来源",
		Applications: []ReleaseApplicationInput{{
			ApplicationID: application.ID, ManualDeploy: true,
			SourceType: model.ReleaseApplicationSourceCommit, SourceValue: "not-a-commit",
		}},
	})
	if !errors.Is(err, ErrInvalidReleasePlan) {
		t.Fatalf("无效 Commit 未被拒绝: %v", err)
	}
}
