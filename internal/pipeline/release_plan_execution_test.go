package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/credential"
	"zrt/internal/model"
	"zrt/internal/repository"
	"zrt/internal/secret"
)

type releasePlanExecutionTestApplication struct {
	applicationID    string
	workflowID       string
	sourceNodeID     string
	workflowRevision uint64
}

func TestReleasePlanExecutionStartsParallelApplicationsTogether(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	first := createReleasePlanExecutionTestApplication(t, service, db, repositoryID, "并行订单服务")
	second := createReleasePlanExecutionTestApplication(t, service, db, repositoryID, "并行库存服务")
	commitSHA := strings.Repeat("a", 40)
	setReleasePlanExecutionTestRefs(service, db, secrets, commitSHA)
	plan := createReleasePlanExecutionTestPlan(t, service, []releasePlanExecutionTestApplication{first, second})

	execution, err := service.CreateReleasePlanExecution(
		context.Background(), plan.ID, "admin",
		releasePlanExecutionTestInput(plan, "parallel-request", commitSHA, first, second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Status != model.ReleasePlanExecutionPending || len(execution.Items) != 2 {
		t.Fatalf("发布计划没有原子创建全部待执行项: %+v", execution)
	}
	assertReleasePlanRunsBlocked(t, db, execution.Items)

	execution, err = service.ReconcileReleasePlanExecution(context.Background(), execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range execution.Items {
		if execution.Items[i].Status != model.ReleasePlanExecutionItemRunning {
			t.Fatalf("并行组没有同时启动全部应用: %+v", execution.Items)
		}
	}
	markReleasePlanExecutionRuns(t, db, execution.Items, model.PipelineRunSucceeded)
	execution, err = service.ReconcileReleasePlanExecution(context.Background(), execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Status != model.ReleasePlanExecutionSucceeded || execution.FinishedAt == nil {
		t.Fatalf("全部应用成功后计划执行没有完成: %+v", execution)
	}
	var storedPlan model.ReleasePlan
	if err := db.First(&storedPlan, "id = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedPlan.Status != model.ReleasePlanCompleted {
		t.Fatalf("计划没有随执行完成: %+v", storedPlan)
	}
}

func TestReleasePlanExecutionQueuesBuildFromStageSnapshot(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	application := createReleasePlanExecutionTestApplication(t, service, db, repositoryID, "阶段式发布服务")
	commitSHA := strings.Repeat("7", 40)
	setReleasePlanExecutionTestRefs(service, db, secrets, commitSHA)
	plan := createReleasePlanExecutionTestPlan(t, service, []releasePlanExecutionTestApplication{application})

	execution, err := service.CreateReleasePlanExecution(
		context.Background(), plan.ID, "admin",
		releasePlanExecutionTestInput(plan, "stage-workflow", commitSHA, application),
	)
	if err != nil {
		t.Fatalf("阶段式流水线无法从发布计划执行: %v", err)
	}
	assertReleasePlanRunsBlocked(t, db, execution.Items)
	execution, err = service.ReconcileReleasePlanExecution(context.Background(), execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	var run model.PipelineRun
	if err := db.First(&run, "id = ?", execution.Items[0].PipelineRunID).Error; err != nil {
		t.Fatal(err)
	}
	if run.CurrentNodeID != "build" || run.Status != model.PipelineRunRunning || run.Stage != "queued" || run.ExecutionJobID == "" {
		t.Fatalf("发布计划没有从代码源提交构建任务: %+v", run)
	}
	assertPipelineBuildJob(t, db, run.ExecutionJobID)
	snapshot, err := parseWorkflowSnapshot(&run)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.BuildPlans["build"].ID == "" || snapshot.DeploymentPlans["deploy-dev"].ID == "" ||
		snapshot.DeploymentTargets["deploy-dev"].ID == "" {
		t.Fatalf("发布计划运行没有保存完整的构建与部署任务快照: %+v", snapshot)
	}
}

func TestReleasePlanExecutionStartsSequentialApplicationsOneAtATime(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	first := createReleasePlanExecutionTestApplication(t, service, db, repositoryID, "串行订单服务")
	second := createReleasePlanExecutionTestApplication(t, service, db, repositoryID, "串行库存服务")
	commitSHA := strings.Repeat("b", 40)
	setReleasePlanExecutionTestRefs(service, db, secrets, commitSHA)
	plan := createReleasePlanExecutionTestPlan(t, service, []releasePlanExecutionTestApplication{first, second})
	var err error
	plan, err = service.UpdateReleaseGroup(context.Background(), plan.ID, plan.Groups[0].ID, ReleaseGroupInput{
		Name: "串行发布组", Mode: model.ReleaseGroupSequential, FailurePolicy: model.ReleaseGroupStopOnFailure,
		ApplicationIDs: []string{first.applicationID, second.applicationID},
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := service.CreateReleasePlanExecution(
		context.Background(), plan.ID, "admin",
		releasePlanExecutionTestInput(plan, "sequential-request", commitSHA, first, second),
	)
	if err != nil {
		t.Fatal(err)
	}
	execution, err = service.ReconcileReleasePlanExecution(context.Background(), execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	running, pending := releasePlanExecutionItemsByStatus(execution.Items)
	if len(running) != 1 || len(pending) != 1 {
		t.Fatalf("串行组第一次调和应只启动一个应用: %+v", execution.Items)
	}
	markReleasePlanExecutionRuns(t, db, running, model.PipelineRunSucceeded)
	execution, err = service.ReconcileReleasePlanExecution(context.Background(), execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	running, pending = releasePlanExecutionItemsByStatus(execution.Items)
	if len(running) != 1 || len(pending) != 0 || running[0].ApplicationID == first.applicationID {
		t.Fatalf("前一个应用完成后没有启动下一个应用: %+v", execution.Items)
	}
}

func TestReleasePlanExecutionDependencyFailurePolicies(t *testing.T) {
	for _, testCase := range []struct {
		name                string
		failurePolicy       model.ReleaseGroupFailurePolicy
		wantDependentStatus model.ReleasePlanExecutionItemStatus
	}{
		{name: "停止策略阻断依赖组", failurePolicy: model.ReleaseGroupStopOnFailure, wantDependentStatus: model.ReleasePlanExecutionItemSkipped},
		{name: "继续策略启动依赖组", failurePolicy: model.ReleaseGroupContinue, wantDependentStatus: model.ReleasePlanExecutionItemRunning},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service, db, secrets, repositoryID := newPipelineTestService(t)
			first := createReleasePlanExecutionTestApplication(t, service, db, repositoryID, "依赖基础服务")
			second := createReleasePlanExecutionTestApplication(t, service, db, repositoryID, "依赖业务服务")
			commitSHA := strings.Repeat("c", 40)
			setReleasePlanExecutionTestRefs(service, db, secrets, commitSHA)
			plan := createReleasePlanExecutionTestPlan(t, service, []releasePlanExecutionTestApplication{first})
			rootGroupID := plan.Groups[0].ID
			var err error
			plan, err = service.UpdateReleaseGroup(context.Background(), plan.ID, rootGroupID, ReleaseGroupInput{
				Name: "基础组", Mode: model.ReleaseGroupSequential, FailurePolicy: testCase.failurePolicy,
				ApplicationIDs: []string{first.applicationID},
			})
			if err != nil {
				t.Fatal(err)
			}
			plan, err = service.CreateReleaseGroup(context.Background(), plan.ID, ReleaseGroupInput{
				Name: "依赖组", Mode: model.ReleaseGroupSequential, FailurePolicy: model.ReleaseGroupStopOnFailure,
				ApplicationIDs: []string{second.applicationID}, DependsOnGroupIDs: []string{rootGroupID},
			})
			if err != nil {
				t.Fatal(err)
			}
			execution, err := service.CreateReleasePlanExecution(
				context.Background(), plan.ID, "admin",
				releasePlanExecutionTestInput(plan, "dependency-"+string(testCase.failurePolicy), commitSHA, first, second),
			)
			if err != nil {
				t.Fatal(err)
			}
			execution, err = service.ReconcileReleasePlanExecution(context.Background(), execution.ID)
			if err != nil {
				t.Fatal(err)
			}
			root := releasePlanExecutionItemForApplication(t, execution.Items, first.applicationID)
			dependent := releasePlanExecutionItemForApplication(t, execution.Items, second.applicationID)
			if root.Status != model.ReleasePlanExecutionItemRunning || dependent.Status != model.ReleasePlanExecutionItemPending {
				t.Fatalf("依赖组在上游结束前被错误启动: %+v", execution.Items)
			}
			markReleasePlanExecutionRuns(t, db, []model.ReleasePlanExecutionItem{root}, model.PipelineRunFailed)
			execution, err = service.ReconcileReleasePlanExecution(context.Background(), execution.ID)
			if err != nil {
				t.Fatal(err)
			}
			dependent = releasePlanExecutionItemForApplication(t, execution.Items, second.applicationID)
			if dependent.Status != testCase.wantDependentStatus {
				t.Fatalf("失败策略没有按快照生效: %+v", execution.Items)
			}
			if testCase.failurePolicy == model.ReleaseGroupStopOnFailure {
				if execution.Status != model.ReleasePlanExecutionFailed {
					t.Fatalf("停止策略结束后执行状态错误: %+v", execution)
				}
				var skippedRun model.PipelineRun
				if err := db.First(&skippedRun, "id = ?", dependent.PipelineRunID).Error; err != nil {
					t.Fatal(err)
				}
				if skippedRun.Status != model.PipelineRunCanceled || skippedRun.Stage != "canceled" {
					t.Fatalf("跳过执行项后仍留下可操作的阻塞运行: %+v", skippedRun)
				}
				return
			}
			markReleasePlanExecutionRuns(t, db, []model.ReleasePlanExecutionItem{dependent}, model.PipelineRunSucceeded)
			execution, err = service.ReconcileReleasePlanExecution(context.Background(), execution.ID)
			if err != nil {
				t.Fatal(err)
			}
			if execution.Status != model.ReleasePlanExecutionFailed {
				t.Fatalf("继续策略允许后续执行，但总体应保留失败结果: %+v", execution)
			}
		})
	}
}

func TestCreateReleasePlanExecutionPreflightIsAtomic(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	first := createReleasePlanExecutionTestApplication(t, service, db, repositoryID, "原子校验订单服务")
	second := createReleasePlanExecutionTestApplication(t, service, db, repositoryID, "原子校验库存服务")
	commitSHA := strings.Repeat("d", 40)
	setReleasePlanExecutionTestRefs(service, db, secrets, commitSHA)
	plan := createReleasePlanExecutionTestPlan(t, service, []releasePlanExecutionTestApplication{first, second})
	input := releasePlanExecutionTestInput(plan, "atomic-request", commitSHA, first, second)
	input.Selections[1].CommitSHA = strings.Repeat("e", 40)

	_, err := service.CreateReleasePlanExecution(context.Background(), plan.ID, "admin", input)
	if !errors.Is(err, ErrReleasePlanExecutionVersionChanged) {
		t.Fatalf("远端版本变化没有阻止整批执行: %v", err)
	}
	for _, value := range []any{&model.ReleasePlanExecution{}, &model.ReleasePlanExecutionItem{}} {
		var count int64
		if err := db.Model(value).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("预检失败后写入了执行数据: model=%T count=%d err=%v", value, count, err)
		}
	}
	var runCount int64
	if err := db.Model(&model.PipelineRun{}).Where("release_plan_execution_id <> ''").Count(&runCount).Error; err != nil || runCount != 0 {
		t.Fatalf("预检失败后写入了流水线运行: count=%d err=%v", runCount, err)
	}
	var storedPlan model.ReleasePlan
	if err := db.First(&storedPlan, "id = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedPlan.Status != model.ReleasePlanDraft {
		t.Fatalf("预检失败改变了发布计划状态: %+v", storedPlan)
	}
}

func TestCreateReleasePlanExecutionRejectsAutomaticOnlyTrigger(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	application := createReleasePlanExecutionTestApplication(t, service, db, repositoryID, "仅自动触发服务")
	commitSHA := strings.Repeat("6", 40)
	setReleasePlanExecutionTestRefs(service, db, secrets, commitSHA)
	result, err := service.GetWorkflow(context.Background(), application.applicationID)
	if err != nil {
		t.Fatal(err)
	}
	events := result.Workflow.Source.Config.Events[:0]
	for _, event := range result.Workflow.Source.Config.Events {
		if event != "manual" {
			events = append(events, event)
		}
	}
	result.Workflow.Source.Config.Events = events
	saved, err := service.SaveWorkflow(context.Background(), application.applicationID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: result.Workflow.Name,
		Revision: result.Workflow.Revision, Activate: true,
		Source: result.Workflow.Source, Stages: result.Workflow.Stages,
	})
	if err != nil {
		t.Fatalf("准备仅自动触发的流水线失败: %v", err)
	}
	application.workflowRevision = saved.Workflow.Revision
	plan := createReleasePlanExecutionTestPlan(t, service, []releasePlanExecutionTestApplication{application})

	_, err = service.CreateReleasePlanExecution(
		context.Background(), plan.ID, "admin",
		releasePlanExecutionTestInput(plan, "auto-only-trigger", commitSHA, application),
	)
	if !errors.Is(err, ErrInvalidReleasePlanExecution) {
		t.Fatalf("未开启 manual 的代码触发节点仍可用于发布计划: %v", err)
	}
	for _, value := range []any{&model.ReleasePlanExecution{}, &model.ReleasePlanExecutionItem{}} {
		var count int64
		if err := db.Model(value).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("自动触发入口校验失败后写入了执行数据: model=%T count=%d err=%v", value, count, err)
		}
	}
}

func TestCreateReleasePlanExecutionRejectsApplicationRepeatedAcrossGroups(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createReleasePlanExecutionTestApplication(t, service, db, repositoryID, "跨组重复服务")
	plan := createReleasePlanExecutionTestPlan(t, service, []releasePlanExecutionTestApplication{application})
	_, err := service.CreateReleaseGroup(context.Background(), plan.ID, ReleaseGroupInput{
		Name: "重复应用组", Mode: model.ReleaseGroupParallel, FailurePolicy: model.ReleaseGroupStopOnFailure,
		ApplicationIDs: []string{application.applicationID},
	})
	if !errors.Is(err, ErrReleaseApplicationAssigned) {
		t.Fatalf("同一应用跨组重复没有在保存发布组时被拒绝: %v", err)
	}
	var count int64
	if err := db.Model(&model.ReleasePlanExecution{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("跨组重复预检失败后产生了执行记录: count=%d err=%v", count, err)
	}
}

func TestCreateReleasePlanExecutionIsIdempotent(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	application := createReleasePlanExecutionTestApplication(t, service, db, repositoryID, "幂等发布服务")
	commitSHA := strings.Repeat("f", 40)
	setReleasePlanExecutionTestRefs(service, db, secrets, commitSHA)
	plan := createReleasePlanExecutionTestPlan(t, service, []releasePlanExecutionTestApplication{application})
	plan, err := service.UpdateReleasePlan(context.Background(), plan.ID, "admin", ReleasePlanInput{
		Name: plan.Name, Version: plan.Version, Description: plan.Description, Status: model.ReleasePlanActive,
	})
	if err != nil {
		t.Fatalf("准备历史 active 发布计划失败: %v", err)
	}
	input := releasePlanExecutionTestInput(plan, "same-request", commitSHA, application)
	first, err := service.CreateReleasePlanExecution(context.Background(), plan.ID, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateReleasePlanExecution(context.Background(), plan.ID, "admin", input)
	if err != nil || second.ID != first.ID {
		t.Fatalf("相同请求没有返回同一执行: first=%+v second=%+v err=%v", first, second, err)
	}
	input.RequestID = "different-request"
	if _, err := service.CreateReleasePlanExecution(context.Background(), plan.ID, "admin", input); !errors.Is(err, ErrReleasePlanExecutionExists) {
		t.Fatalf("同一计划的不同请求没有被拒绝: %v", err)
	}
	var count int64
	if err := db.Model(&model.ReleasePlanExecution{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("幂等请求创建了重复执行: count=%d err=%v", count, err)
	}
}

func TestReleasePlanPendingRunCannotBypassGroupScheduling(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	application := createReleasePlanExecutionTestApplication(t, service, db, repositoryID, "调度边界服务")
	commitSHA := strings.Repeat("3", 40)
	setReleasePlanExecutionTestRefs(service, db, secrets, commitSHA)
	plan := createReleasePlanExecutionTestPlan(t, service, []releasePlanExecutionTestApplication{application})
	execution, err := service.CreateReleasePlanExecution(
		context.Background(), plan.ID, "admin",
		releasePlanExecutionTestInput(plan, "scheduling-guard", commitSHA, application),
	)
	if err != nil {
		t.Fatal(err)
	}
	runID := execution.Items[0].PipelineRunID
	if _, err := service.AdvanceRun(context.Background(), runID, "admin", ""); !errors.Is(err, ErrPipelineRunAwaitingReleasePlan) {
		t.Fatalf("待调度子运行可通过 AdvanceRun 提前执行: %v", err)
	}
	if _, err := service.ExecuteRun(context.Background(), runID, "admin", "", "", ""); !errors.Is(err, ErrPipelineRunAwaitingReleasePlan) {
		t.Fatalf("待调度子运行可通过 ExecuteRun 提前执行: %v", err)
	}
	assertReleasePlanRunsBlocked(t, db, execution.Items)
}

func TestReleasePlanExecutionRecoversClaimedBlockedRunFromSnapshot(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	application := createReleasePlanExecutionTestApplication(t, service, db, repositoryID, "崩溃恢复服务")
	commitSHA := strings.Repeat("2", 40)
	setReleasePlanExecutionTestRefs(service, db, secrets, commitSHA)
	plan := createReleasePlanExecutionTestPlan(t, service, []releasePlanExecutionTestApplication{application})
	execution, err := service.CreateReleasePlanExecution(
		context.Background(), plan.ID, "admin",
		releasePlanExecutionTestInput(plan, "recovery-request", commitSHA, application),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ReleasePlanExecutionItem{}).Where("id = ?", execution.Items[0].ID).
		Update("status", model.ReleasePlanExecutionItemRunning).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.applicationID).
		Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	execution, err = service.ReconcileReleasePlanExecution(context.Background(), execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Items[0].Status != model.ReleasePlanExecutionItemRunning {
		t.Fatalf("已领取但未启动的执行项没有恢复并重新启动: %+v", execution.Items[0])
	}
	var run model.PipelineRun
	if err := db.First(&run, "id = ?", execution.Items[0].PipelineRunID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.PipelineRunRunning || run.CurrentNodeID != "build" || run.ExecutionJobID == "" {
		t.Fatalf("调和器没有使用固化快照恢复构建任务: %+v", run)
	}
	assertPipelineBuildJob(t, db, run.ExecutionJobID)
}

func TestReleasePlanAutomaticAdvanceDoesNotPassStaleManualGate(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "过期推进防护服务")
	now := time.Now().UTC()
	snapshot, err := workflowSnapshotJSON(application.Workflow)
	if err != nil {
		t.Fatal(err)
	}
	execution := model.ReleasePlanExecution{
		ID: "stale-execution", ReleasePlanID: "stale-plan", RequestID: "stale-request",
		Status: model.ReleasePlanExecutionRunning, Snapshot: "{}", CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	run := model.PipelineRun{
		ID: "stale-run", ApplicationID: application.ID,
		ReleasePlanExecutionID: execution.ID, ReleasePlanExecutionItemID: "stale-item",
		Trigger: "release_plan", Ref: "refs/heads/main", CommitSHA: strings.Repeat("5", 40),
		Status: model.PipelineRunAwaitingApproval, Stage: string(model.WorkflowNodeApproval), CurrentNodeID: "approval",
		WorkflowID: application.Workflow.ID, WorkflowRevision: application.Workflow.Revision,
		WorkflowSnapshot: snapshot, ApprovalRequired: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	item := model.ReleasePlanExecutionItem{
		ID: run.ReleasePlanExecutionItemID, ReleasePlanExecutionID: execution.ID,
		ReleaseGroupID: "stale-group", ReleaseGroupApplicationID: "stale-group-application",
		ApplicationID: run.ApplicationID, PipelineRunID: run.ID,
		Status: model.ReleasePlanExecutionItemRunning, Ref: run.Ref, CommitSHA: run.CommitSHA,
		SourceNodeID: "manual-source", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PipelineRunApproval{
		ID: "stale-approval", PipelineRunID: run.ID, NodeID: "approval",
		RequestedBy: "admin", ApprovedBy: "reviewer", ApprovedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	expected := run
	advanced, err := service.AdvanceRun(context.Background(), run.ID, "admin", "")
	if err != nil || advanced.CurrentNodeID != "manual" {
		t.Fatalf("没有先进入人工放行节点: run=%+v err=%v", advanced, err)
	}
	current, advancedStale, err := service.advanceRunIfCurrent(context.Background(), expected, "admin", "")
	if err != nil {
		t.Fatal(err)
	}
	if advancedStale || current.CurrentNodeID != "manual" || current.ExecutionJobID != "" {
		t.Fatalf("过期的自动推进越过了人工放行节点: advanced=%t run=%+v", advancedStale, current)
	}
}

func createReleasePlanExecutionTestApplication(
	t *testing.T,
	service *Service,
	db *gorm.DB,
	repositoryID, name string,
) releasePlanExecutionTestApplication {
	t.Helper()
	application := createManualRunTestApplication(t, service, db, repositoryID, name)
	result, err := service.GetWorkflow(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	workflow := result.Workflow
	triggerID := workflow.Source.ID
	if triggerID == "" {
		t.Fatal("测试流水线缺少代码触发节点")
	}
	if !containsEvent(workflow.Source.Config.Events, "manual") {
		workflow.Source.Config.Events = append(workflow.Source.Config.Events, "manual")
	}
	saved, err := service.SaveWorkflow(context.Background(), application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Name,
		Revision: workflow.Revision, Activate: true, Source: workflow.Source, Stages: workflow.Stages,
	})
	if err != nil {
		t.Fatalf("启用发布计划测试流水线失败: %v", err)
	}
	return releasePlanExecutionTestApplication{
		applicationID: application.ID, workflowID: saved.Workflow.ID,
		sourceNodeID: triggerID, workflowRevision: saved.Workflow.Revision,
	}
}

func setReleasePlanExecutionTestRefs(service *Service, db *gorm.DB, secrets *secret.Manager, commitSHA string) {
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}},
		}}, 4,
	)
}

func createReleasePlanExecutionTestPlan(
	t *testing.T,
	service *Service,
	applications []releasePlanExecutionTestApplication,
) *model.ReleasePlan {
	t.Helper()
	inputs := make([]ReleaseApplicationInput, 0, len(applications))
	for i := range applications {
		inputs = append(inputs, ReleaseApplicationInput{ApplicationID: applications[i].applicationID})
	}
	plan, err := service.CreateReleasePlan(context.Background(), "admin", ReleasePlanInput{
		Description: "验证发布计划批量执行", Groups: defaultReleasePlanGroups(inputs),
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func releasePlanExecutionTestInput(
	plan *model.ReleasePlan,
	requestID, commitSHA string,
	applications ...releasePlanExecutionTestApplication,
) ReleasePlanExecutionInput {
	applicationByID := make(map[string]releasePlanExecutionTestApplication, len(applications))
	for i := range applications {
		applicationByID[applications[i].applicationID] = applications[i]
	}
	selections := make([]ReleasePlanExecutionSelection, 0, len(applications))
	for groupIndex := range plan.Groups {
		for applicationIndex := range plan.Groups[groupIndex].Applications {
			groupApplication := plan.Groups[groupIndex].Applications[applicationIndex]
			application := applicationByID[groupApplication.ApplicationID]
			selections = append(selections, ReleasePlanExecutionSelection{
				ReleaseGroupApplicationID: groupApplication.ID,
				WorkflowID:                application.workflowID,
				ExpectedWorkflowRevision:  application.workflowRevision,
				SourceNodeID:              application.sourceNodeID,
				Ref:                       "refs/heads/main",
				CommitSHA:                 commitSHA,
			})
		}
	}
	return ReleasePlanExecutionInput{
		RequestID: requestID, ExpectedPlanUpdatedAt: plan.UpdatedAt, Selections: selections,
	}
}

func assertReleasePlanRunsBlocked(t *testing.T, db *gorm.DB, items []model.ReleasePlanExecutionItem) {
	t.Helper()
	for i := range items {
		var run model.PipelineRun
		if err := db.First(&run, "id = ?", items[i].PipelineRunID).Error; err != nil {
			t.Fatal(err)
		}
		if run.Status != model.PipelineRunBlocked || run.WorkflowSnapshot == "" ||
			run.ReleasePlanExecutionID == "" || run.ReleasePlanExecutionItemID != items[i].ID ||
			run.CurrentNodeID != items[i].SourceNodeID || run.Stage != string(model.WorkflowNodeTrigger) {
			t.Fatalf("计划子运行没有预创建不可变阻塞快照: %+v", run)
		}
	}
}

func releasePlanExecutionItemsByStatus(items []model.ReleasePlanExecutionItem) (
	running []model.ReleasePlanExecutionItem,
	pending []model.ReleasePlanExecutionItem,
) {
	for i := range items {
		switch items[i].Status {
		case model.ReleasePlanExecutionItemRunning:
			running = append(running, items[i])
		case model.ReleasePlanExecutionItemPending:
			pending = append(pending, items[i])
		}
	}
	return running, pending
}

func markReleasePlanExecutionRuns(
	t *testing.T,
	db *gorm.DB,
	items []model.ReleasePlanExecutionItem,
	status model.PipelineRunStatus,
) {
	t.Helper()
	ids := make([]string, 0, len(items))
	for i := range items {
		ids = append(ids, items[i].PipelineRunID)
	}
	stage := "completed"
	if status == model.PipelineRunFailed {
		stage = "failed"
	}
	if err := db.Model(&model.PipelineRun{}).Where("id IN ?", ids).
		Updates(map[string]any{"status": status, "stage": stage}).Error; err != nil {
		t.Fatal(err)
	}
}

func releasePlanExecutionItemForApplication(
	t *testing.T,
	items []model.ReleasePlanExecutionItem,
	applicationID string,
) model.ReleasePlanExecutionItem {
	t.Helper()
	for i := range items {
		if items[i].ApplicationID == applicationID {
			return items[i]
		}
	}
	t.Fatalf("没有找到应用 %s 的发布计划执行项", applicationID)
	return model.ReleasePlanExecutionItem{}
}
