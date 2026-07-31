package pipeline

import (
	"context"
	"errors"
	"testing"

	"zrt/internal/model"
)

func defaultReleasePlanGroups(applications []ReleaseApplicationInput) []ReleaseGroupInput {
	return []ReleaseGroupInput{{
		Name: "默认发布组", Mode: model.ReleaseGroupParallel,
		FailurePolicy: model.ReleaseGroupStopOnFailure, Applications: applications,
	}}
}

func TestReleasePlanContainsOrderedApplicationGroups(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	first, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "order_service", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "inventory_service", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	commitSHA := "0123456789012345678901234567890123456789"
	plan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Name: "八月发布列车", Version: "2026.08", Description: "八月应用发布",
		Groups: defaultReleasePlanGroups([]ReleaseApplicationInput{
			{ApplicationID: first.ID, ManualDeploy: true, SourceType: model.ReleaseApplicationSourceBranch, SourceValue: "main"},
			{ApplicationID: second.ID, ManualDeploy: true, SourceType: model.ReleaseApplicationSourceCommit, SourceValue: commitSHA},
		}),
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
		},
	})
	if err != nil || len(plan.Groups) != 1 || len(plan.Groups[0].Applications) != 1 ||
		!plan.Groups[0].Applications[0].ManualDeploy || plan.Groups[0].Applications[0].SourceValue != "main" {
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

func TestReleasePlanCreatesGroupsBeforeAssigningApplications(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	applications := make([]*model.Application, 0, 3)
	for _, name := range []string{"gateway", "order_service", "notification_service"} {
		application, err := service.CreateApplication(ctx, "admin", ApplicationInput{Name: name, RepositoryID: repositoryID})
		if err != nil {
			t.Fatal(err)
		}
		applications = append(applications, application)
	}
	groups := []ReleaseGroupInput{
		{
			Name: "基础服务", Mode: model.ReleaseGroupSequential, FailurePolicy: model.ReleaseGroupStopOnFailure,
			Applications: []ReleaseApplicationInput{{ApplicationID: applications[0].ID}, {ApplicationID: applications[1].ID}},
		},
		{
			Name: "异步服务", Mode: model.ReleaseGroupParallel, FailurePolicy: model.ReleaseGroupContinue,
			Applications: []ReleaseApplicationInput{{ApplicationID: applications[2].ID}},
		},
	}
	plan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{Description: "验证计划、发布组和应用层级", Groups: groups})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 2 || plan.Groups[0].Name != "基础服务" || plan.Groups[0].SortOrder != 0 ||
		len(plan.Groups[0].Applications) != 2 || plan.Groups[1].Name != "异步服务" || plan.Groups[1].SortOrder != 1 ||
		len(plan.Groups[1].Applications) != 1 {
		t.Fatalf("发布计划没有按发布组保存应用: %+v", plan.Groups)
	}
	duplicateGroups := []ReleaseGroupInput{
		{Name: "第一组", Applications: []ReleaseApplicationInput{{ApplicationID: applications[0].ID}}},
		{Name: "第二组", Applications: []ReleaseApplicationInput{{ApplicationID: applications[0].ID}}},
	}
	if _, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{Description: "拒绝跨组重复应用", Groups: duplicateGroups}); !errors.Is(err, ErrInvalidReleasePlan) {
		t.Fatalf("创建发布计划没有拒绝跨组重复应用: %v", err)
	}
}

func TestReleasePlanGeneratesInternalIdentity(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "release_app", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Description: "修复订单查询并发布", Groups: defaultReleasePlanGroups([]ReleaseApplicationInput{{ApplicationID: application.ID}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Description != "修复订单查询并发布" || plan.Name == "" || plan.Version == "" {
		t.Fatalf("发布计划没有生成内部标识: %+v", plan)
	}
	if _, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Groups: defaultReleasePlanGroups([]ReleaseApplicationInput{{ApplicationID: application.ID}}),
	}); !errors.Is(err, ErrInvalidReleasePlan) {
		t.Fatalf("缺少说明的发布计划应被拒绝: %v", err)
	}
}

func TestReleasePlanVersionIsUnique(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "release_app", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	applications := []ReleaseApplicationInput{{ApplicationID: application.ID}}
	if _, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{Name: "第一批", Version: "2026.08", Description: "第一批发布", Groups: defaultReleasePlanGroups(applications)}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{Name: "第二批", Version: "2026.08", Description: "第二批发布", Groups: defaultReleasePlanGroups(applications)}); !errors.Is(err, ErrReleasePlanExists) {
		t.Fatalf("重复发布版本未被拒绝: %v", err)
	}
}

func TestReleasePlanRejectsInvalidManualSource(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "manual_release_app", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Name: "错误版本来源", Version: "2026.09", Description: "验证错误版本来源",
		Groups: defaultReleasePlanGroups([]ReleaseApplicationInput{{
			ApplicationID: application.ID, ManualDeploy: true,
			SourceType: model.ReleaseApplicationSourceCommit, SourceValue: "not-a-commit",
		}}),
	})
	if !errors.Is(err, ErrInvalidReleasePlan) {
		t.Fatalf("无效 Commit 未被拒绝: %v", err)
	}
}

func TestReleasePlanDetailsStatusAndDeletion(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "plan_maintenance_app", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Description: "原始说明", Groups: defaultReleasePlanGroups([]ReleaseApplicationInput{{ApplicationID: application.ID}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsActive {
		t.Fatal("新建发布计划应默认启用")
	}
	originalName, originalVersion := plan.Name, plan.Version
	plan, err = service.UpdateReleasePlan(ctx, plan.ID, "admin", ReleasePlanInput{Description: "修改后的说明"})
	if err != nil {
		t.Fatalf("只更新说明失败: %v", err)
	}
	if plan.Name != originalName || plan.Version != originalVersion || plan.Description != "修改后的说明" {
		t.Fatalf("更新说明时没有保留内部标识: %+v", plan)
	}
	plan, err = service.SetReleasePlanActive(ctx, plan.ID, "admin", false)
	if err != nil {
		t.Fatalf("停用发布计划失败: %v", err)
	}
	if plan.IsActive || plan.Status != model.ReleasePlanDraft {
		t.Fatalf("停用不应改写生命周期状态: %+v", plan)
	}
	if _, err := service.CreateReleasePlanExecution(ctx, plan.ID, "admin", ReleasePlanExecutionInput{
		RequestID: "disabled-plan", ExpectedPlanUpdatedAt: plan.UpdatedAt,
	}); !errors.Is(err, ErrReleasePlanDisabled) {
		t.Fatalf("停用计划仍然允许执行: %v", err)
	}
	if _, err := service.SetReleasePlanActive(ctx, plan.ID, "admin", true); err != nil {
		t.Fatalf("重新启用发布计划失败: %v", err)
	}
	if err := service.DeleteReleasePlan(ctx, plan.ID); err != nil {
		t.Fatalf("删除未执行发布计划失败: %v", err)
	}
	if _, err := service.FindReleasePlan(ctx, plan.ID); !errors.Is(err, ErrReleasePlanNotFound) {
		t.Fatalf("普通查询仍返回已删除发布计划: %v", err)
	}
	var deletedPlan model.ReleasePlan
	if err := db.Unscoped().First(&deletedPlan, "id = ?", plan.ID).Error; err != nil || !deletedPlan.DeletedAt.Valid {
		t.Fatalf("发布计划没有软删除保留: plan=%+v err=%v", deletedPlan, err)
	}
	var groupCount int64
	if err := db.Model(&model.ReleaseGroup{}).Where("release_plan_id = ?", plan.ID).Count(&groupCount).Error; err != nil || groupCount != 1 {
		t.Fatalf("软删除发布计划清除了发布组: count=%d err=%v", groupCount, err)
	}
}

func TestReleaseGroupPersistsApplicationOrderAndParallelDefault(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	applications := make([]*model.Application, 0, 3)
	for _, name := range []string{"order_service", "inventory_service", "payment_service"} {
		application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
			Name: name, RepositoryID: repositoryID,
		})
		if err != nil {
			t.Fatal(err)
		}
		applications = append(applications, application)
	}
	plan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Description: "验证发布顺序", Groups: defaultReleasePlanGroups([]ReleaseApplicationInput{
			{ApplicationID: applications[0].ID}, {ApplicationID: applications[1].ID},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	groupID := plan.Groups[0].ID
	plan, err = service.UpdateReleaseGroup(ctx, plan.ID, groupID, ReleaseGroupInput{
		Name: "核心服务", Mode: model.ReleaseGroupSequential,
		Applications: []ReleaseApplicationInput{
			{ApplicationID: applications[1].ID},
			{ApplicationID: applications[0].ID},
			{ApplicationID: applications[2].ID},
		},
	})
	if err != nil {
		t.Fatalf("调整发布组应用顺序失败: %v", err)
	}
	group := plan.Groups[0]
	if group.Mode != model.ReleaseGroupSequential || len(group.Applications) != 3 {
		t.Fatalf("串行发布组保存错误: %+v", group)
	}
	for index, applicationID := range []string{applications[1].ID, applications[0].ID, applications[2].ID} {
		if group.Applications[index].ApplicationID != applicationID || group.Applications[index].SortOrder != index {
			t.Fatalf("应用拖动顺序未持久化: %+v", group.Applications)
		}
	}
	plan, err = service.UpdateReleaseGroup(ctx, plan.ID, groupID, ReleaseGroupInput{
		Name: "核心服务", Applications: []ReleaseApplicationInput{{ApplicationID: applications[2].ID}},
	})
	if err != nil {
		t.Fatalf("删除发布组应用失败: %v", err)
	}
	if plan.Groups[0].Mode != model.ReleaseGroupParallel || len(plan.Groups[0].Applications) != 1 ||
		plan.Groups[0].Applications[0].ApplicationID != applications[2].ID {
		t.Fatalf("关闭顺序发布后未按并行模式保存: %+v", plan.Groups[0])
	}
	plan, err = service.UpdateReleaseGroup(ctx, plan.ID, groupID, ReleaseGroupInput{Name: "核心服务"})
	if err != nil {
		t.Fatalf("清空发布组应用失败: %v", err)
	}
	if len(plan.Groups[0].Applications) != 0 {
		t.Fatalf("空发布组未保存: %+v", plan.Groups[0].Applications)
	}
}

func TestReleaseGroupRejectsApplicationAssignedToAnotherGroup(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "unique_release_app", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Description: "验证跨组重复", Groups: defaultReleasePlanGroups([]ReleaseApplicationInput{{ApplicationID: application.ID}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateReleaseGroup(ctx, plan.ID, ReleaseGroupInput{
		Name: "重复发布组", Applications: []ReleaseApplicationInput{{ApplicationID: application.ID}},
	}); !errors.Is(err, ErrReleaseApplicationAssigned) {
		t.Fatalf("跨发布组重复应用未被拒绝: %v", err)
	}
}

func TestReleasePlanWithCompletedExecutionCanBeSoftDeleted(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "execution_history_app", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Description: "保留执行历史", Groups: defaultReleasePlanGroups([]ReleaseApplicationInput{{ApplicationID: application.ID}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := plan.UpdatedAt
	execution := model.ReleasePlanExecution{
		ID: "execution-history", ReleasePlanID: plan.ID, RequestID: "execution-history",
		Status: model.ReleasePlanExecutionSucceeded, Snapshot: "{}", CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteReleasePlan(ctx, plan.ID); err != nil {
		t.Fatalf("已结束执行的发布计划不能软删除: %v", err)
	}
	if _, err := service.FindReleasePlan(ctx, plan.ID); !errors.Is(err, ErrReleasePlanNotFound) {
		t.Fatalf("普通详情查询仍返回软删除计划: %v", err)
	}
	plans, err := service.ListReleasePlans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, listed := range plans {
		if listed.ID == plan.ID {
			t.Fatalf("普通列表仍返回软删除计划: %+v", listed)
		}
	}
	var deletedPlan model.ReleasePlan
	if err := db.Unscoped().First(&deletedPlan, "id = ?", plan.ID).Error; err != nil || !deletedPlan.DeletedAt.Valid {
		t.Fatalf("软删除计划审计记录缺失: plan=%+v err=%v", deletedPlan, err)
	}
	var groupCount int64
	if err := db.Model(&model.ReleaseGroup{}).Where("release_plan_id = ?", plan.ID).Count(&groupCount).Error; err != nil || groupCount != 1 {
		t.Fatalf("软删除计划的发布组未保留: count=%d err=%v", groupCount, err)
	}
	storedExecution, err := service.FindReleasePlanExecution(ctx, execution.ID)
	if err != nil || storedExecution.Status != model.ReleasePlanExecutionSucceeded {
		t.Fatalf("软删除计划后历史执行不可查询: execution=%+v err=%v", storedExecution, err)
	}
}

func TestRunningReleasePlanBlocksStructuralChangesButCanBeDisabled(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "running_plan_app", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Description: "验证执行中修改边界", Groups: defaultReleasePlanGroups([]ReleaseApplicationInput{{ApplicationID: application.ID}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := plan.UpdatedAt
	execution := model.ReleasePlanExecution{
		ID: "running-execution", ReleasePlanID: plan.ID, RequestID: "running-execution",
		Status: model.ReleasePlanExecutionRunning, Snapshot: "{}", CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateReleasePlan(ctx, plan.ID, "admin", ReleasePlanInput{Description: "不应保存"}); !errors.Is(err, ErrReleasePlanNotEditable) {
		t.Fatalf("执行中发布计划仍允许修改: %v", err)
	}
	if _, err := service.UpdateReleaseGroup(ctx, plan.ID, plan.Groups[0].ID, ReleaseGroupInput{
		Name: "不应保存", Applications: []ReleaseApplicationInput{{ApplicationID: application.ID}},
	}); !errors.Is(err, ErrReleasePlanNotEditable) {
		t.Fatalf("执行中发布组仍允许修改: %v", err)
	}
	if _, err := service.SaveReleasePlanConfiguration(ctx, plan.ID, "admin", ReleasePlanConfigurationInput{
		Description: "不应保存", Groups: []ReleaseGroupConfigurationInput{{
			ID: plan.Groups[0].ID, Name: plan.Groups[0].Name,
			Applications: []ReleaseApplicationInput{{ApplicationID: application.ID}},
		}},
	}); !errors.Is(err, ErrReleasePlanNotEditable) {
		t.Fatalf("执行中发布计划仍允许批量改写: %v", err)
	}
	plan, err = service.SetReleasePlanActive(ctx, plan.ID, "admin", false)
	if err != nil || plan.IsActive {
		t.Fatalf("执行中计划不能独立停用: plan=%+v err=%v", plan, err)
	}
	if err := service.DeleteReleasePlan(ctx, plan.ID); !errors.Is(err, ErrReleasePlanNotEditable) {
		t.Fatalf("执行中发布计划仍允许删除: %v", err)
	}
	if _, err := service.FindReleasePlan(ctx, plan.ID); err != nil {
		t.Fatalf("删除被拒绝后发布计划不可查询: %v", err)
	}
}

func TestSaveReleasePlanConfigurationMovesApplicationsAtomically(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	applications := make([]*model.Application, 0, 2)
	for _, name := range []string{"atomic_order_service", "atomic_inventory_service"} {
		application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
			Name: name, RepositoryID: repositoryID,
		})
		if err != nil {
			t.Fatal(err)
		}
		applications = append(applications, application)
	}
	plan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Description: "原始批量配置", Groups: defaultReleasePlanGroups([]ReleaseApplicationInput{
			{ApplicationID: applications[0].ID}, {ApplicationID: applications[1].ID},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	firstGroupID := plan.Groups[0].ID
	plan, err = service.CreateReleaseGroup(ctx, plan.ID, ReleaseGroupInput{Name: "第二发布组"})
	if err != nil {
		t.Fatalf("创建空发布组失败: %v", err)
	}
	secondGroupID := plan.Groups[1].ID
	plan, err = service.SaveReleasePlanConfiguration(ctx, plan.ID, "admin", ReleasePlanConfigurationInput{
		Description: "原子调整完成",
		Groups: []ReleaseGroupConfigurationInput{
			{
				ID: secondGroupID, Name: "第二发布组", Mode: model.ReleaseGroupSequential,
				Applications: []ReleaseApplicationInput{{ApplicationID: applications[1].ID}},
			},
			{
				ID: firstGroupID, Name: "第一发布组", Mode: model.ReleaseGroupParallel,
				Applications: []ReleaseApplicationInput{{ApplicationID: applications[0].ID}},
			},
		},
	})
	if err != nil {
		t.Fatalf("跨组移动应用失败: %v", err)
	}
	if plan.Description != "原子调整完成" || len(plan.Groups) != 2 ||
		plan.Groups[0].ID != secondGroupID || plan.Groups[0].SortOrder != 0 || plan.Groups[0].Mode != model.ReleaseGroupSequential ||
		len(plan.Groups[0].Applications) != 1 || plan.Groups[0].Applications[0].ApplicationID != applications[1].ID ||
		plan.Groups[1].ID != firstGroupID || plan.Groups[1].SortOrder != 1 ||
		len(plan.Groups[1].Applications) != 1 || plan.Groups[1].Applications[0].ApplicationID != applications[0].ID {
		t.Fatalf("批量配置没有保存组和应用顺序: %+v", plan)
	}
	_, err = service.SaveReleasePlanConfiguration(ctx, plan.ID, "admin", ReleasePlanConfigurationInput{
		Description: "不应部分保存",
		Groups: []ReleaseGroupConfigurationInput{
			{ID: secondGroupID, Name: "第二发布组", Applications: []ReleaseApplicationInput{{ApplicationID: applications[0].ID}}},
			{ID: firstGroupID, Name: "第一发布组", Applications: []ReleaseApplicationInput{{ApplicationID: applications[0].ID}}},
		},
	})
	if !errors.Is(err, ErrReleaseApplicationAssigned) {
		t.Fatalf("批量配置没有拒绝跨组重复应用: %v", err)
	}
	stored, err := service.FindReleasePlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Description != "原子调整完成" || len(stored.Groups) != 2 || stored.Groups[0].ID != secondGroupID {
		t.Fatalf("失败的批量配置留下了部分更新: %+v", stored)
	}
}
