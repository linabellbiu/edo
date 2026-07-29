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
)

func TestExecuteBlockedManualRunWithSelectedCommit(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	commitSHA := strings.Repeat("a", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}},
		}}, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "手动选择版本")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	run, err := service.PrepareRun(context.Background(), application.ID, "admin")
	if err != nil || run.Status != model.PipelineRunBlocked {
		t.Fatalf("开启手动选项的代码触发节点应创建待选择版本的运行: run=%+v err=%v", run, err)
	}

	run, err = service.ExecuteRun(context.Background(), run.ID, "admin", "refs/heads/main", commitSHA, "")
	if err != nil {
		t.Fatal(err)
	}
	if run.Ref != "refs/heads/main" || run.CommitSHA != commitSHA || run.Status != model.PipelineRunRunning || run.CurrentNodeID != "deploy-dev" || run.ExecutionJobID == "" {
		t.Fatalf("流水线运行没有使用所选版本提交真实执行任务: %+v", run)
	}
}

func TestExecuteManualRunStartsFromManualCodeTrigger(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	commitSHA := strings.Repeat("d", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}},
		}}, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "代码触发手动入口")
	workflowResult, err := service.GetWorkflow(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	workflow := workflowResult.Workflow
	var triggerID string
	for i := range workflow.Nodes {
		if workflow.Nodes[i].Type == model.WorkflowNodeTrigger {
			triggerID = workflow.Nodes[i].ID
			break
		}
	}
	if triggerID == "" {
		t.Fatal("默认流水线缺少代码触发节点")
	}
	if _, err := service.SaveWorkflow(context.Background(), application.ID, "admin", WorkflowInput{
		Name: workflow.Name, Revision: workflow.Revision, Activate: true,
		Nodes: workflow.Nodes, Edges: workflow.Edges, Viewport: workflow.Viewport,
	}); err != nil {
		t.Fatalf("包含手动选项的代码触发流水线未能启用: %v", err)
	}

	run, err := service.PrepareRun(context.Background(), application.ID, "admin")
	if err != nil || run.Status != model.PipelineRunBlocked {
		t.Fatalf("代码触发节点的手动选项没有进入代码版本选择: run=%+v err=%v", run, err)
	}
	run, err = service.ExecuteRun(
		context.Background(), run.ID, "admin", "refs/heads/main", commitSHA, triggerID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if run.CurrentNodeID != "deploy-dev" || run.Status != model.PipelineRunRunning ||
		run.Stage != "queued" || run.ExecutionJobID == "" {
		t.Fatalf("手动运行没有从代码触发节点进入部署: %+v", run)
	}
}

func TestExecuteManualRunRejectsAutomaticOnlyCodeTrigger(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	commitSHA := strings.Repeat("8", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}},
		}}, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "拒绝自动入口手动执行")
	result, err := service.GetWorkflow(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	deployID := ""
	for i := range result.Workflow.Nodes {
		if result.Workflow.Nodes[i].Type == model.WorkflowNodeDeploy {
			deployID = result.Workflow.Nodes[i].ID
			break
		}
	}
	if deployID == "" {
		t.Fatal("默认流水线缺少部署节点")
	}
	result.Workflow.Nodes = append(result.Workflow.Nodes, model.WorkflowNode{
		ID: "trigger-auto-only", Type: model.WorkflowNodeTrigger, Name: "仅自动触发",
		Config: model.WorkflowNodeConfig{Environment: "dev", Branch: "main", Events: []string{"push"}},
	})
	result.Workflow.Edges = append(result.Workflow.Edges, model.WorkflowEdge{
		ID: "auto-only-to-deploy", Source: "trigger-auto-only", Target: deployID,
	})
	saved, err := service.SaveWorkflow(context.Background(), application.ID, "admin", WorkflowInput{
		Name: result.Workflow.Name, Revision: result.Workflow.Revision, Activate: true,
		Nodes: result.Workflow.Nodes, Edges: result.Workflow.Edges, Viewport: result.Workflow.Viewport,
	})
	if err != nil {
		t.Fatalf("准备自动和手动代码触发入口失败: %v", err)
	}
	if !saved.Workflow.IsActive {
		t.Fatal("测试流水线未启用")
	}
	run, err := service.PrepareRun(context.Background(), application.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ExecuteRun(
		context.Background(), run.ID, "admin", "refs/heads/main", commitSHA, "trigger-auto-only",
	)
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("未开启 manual 的代码触发节点仍可作为手动入口: %v", err)
	}
	if err := db.First(run, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.PipelineRunBlocked || run.CommitSHA != "" || run.ExecutionJobID != "" {
		t.Fatalf("拒绝自动入口后产生了执行副作用: %+v", run)
	}
}

func TestExecuteManualRunRejectsStaleCommit(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	remoteCommit := strings.Repeat("b", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: remoteCommit}},
		}}, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "拒绝过期版本")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	run, _ := service.PrepareRun(context.Background(), application.ID, "admin")

	_, err := service.ExecuteRun(context.Background(), run.ID, "admin", "refs/heads/main", strings.Repeat("c", 40), "")
	if !errors.Is(err, ErrManualCommitNotFound) {
		t.Fatalf("远端已经变化时应拒绝执行: %v", err)
	}
	if err := db.First(run, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.PipelineRunBlocked || run.CommitSHA != "" {
		t.Fatalf("校验失败不应修改流水线运行: %+v", run)
	}
}

func TestExecuteWithoutImageRegistryUsesDockerSSHFallback(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	commitSHA := strings.Repeat("f", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}},
		}}, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "先选版本再配置")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Application{}).Where("id = ?", application.ID).Update("image_registry_id", "").Error; err != nil {
		t.Fatal(err)
	}
	run, err := service.PrepareRun(context.Background(), application.ID, "admin")
	if err != nil {
		t.Fatalf("尚未选择代码版本时应该生成待手动选择的运行: %v", err)
	}

	run, err = service.ExecuteRun(context.Background(), run.ID, "admin", "refs/heads/main", commitSHA, "")
	if err != nil {
		t.Fatalf("Docker SSH 发布不应强制绑定镜像仓库: %v", err)
	}
	if run.Status != model.PipelineRunRunning || run.Ref != "refs/heads/main" || run.CommitSHA != commitSHA || run.ExecutionJobID == "" {
		t.Fatalf("未绑定镜像仓库时没有提交 Docker SSH 构建发布任务: %+v", run)
	}
	var component model.PipelineRunRepository
	if err := db.First(&component, "pipeline_run_id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if component.ImageRegistryID != "" {
		t.Fatalf("无仓库运行不应写入不存在的镜像仓库快照: %+v", component)
	}
}

func TestLocalExecutionImageUsesCommitAndRunIdentity(t *testing.T) {
	prepared := &executionContext{
		application: model.Application{ID: "application-id", Name: "Order API"},
		run: model.PipelineRun{
			ID: "12345678-abcd-efab-cdef-1234567890ab", CommitSHA: "abcdef1234567890abcdef1234567890abcdef12",
		},
	}
	image, err := localExecutionImage(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if image != "zrt.local/order-api:abcdef123456-12345678" {
		t.Fatalf("本地镜像标签没有同时固定 Commit 和运行身份: %q", image)
	}
}

func TestPipelineRunKeepsExactSSHDeploymentPlanSnapshot(t *testing.T) {
	script := "printf 'release'  \n\n"
	plan := &model.DeploymentPlan{
		ID: "script-plan", Kind: model.DeploymentPlanScript, Script: script, TimeoutSeconds: 180,
	}
	application := &model.Application{
		RepositoryID: "repository-1", DeploymentPlanID: plan.ID, DeploymentPlan: plan,
	}
	components := pipelineRunRepositories(application, "run-1", "refs/heads/main", strings.Repeat("a", 40), time.Now().UTC())
	if len(components) != 1 {
		t.Fatalf("流水线仓库快照数量错误: %d", len(components))
	}
	wantDigest := model.DeploymentPlanExecutionDigest(plan.Kind, script, plan.TimeoutSeconds)
	if components[0].DeploymentPlanScript != script || components[0].DeploymentPlanKind != plan.Kind ||
		components[0].DeploymentPlanTimeoutSeconds != plan.TimeoutSeconds || components[0].DeploymentPlanDigest != wantDigest {
		t.Fatalf("SSH 部署方案没有按原始字节创建不可变快照: %+v", components[0])
	}
	plan.Script = "echo modified\n"
	plan.TimeoutSeconds = 30
	if components[0].DeploymentPlanScript != script || components[0].DeploymentPlanDigest != wantDigest {
		t.Fatal("应用部署方案后续修改污染了已经创建的流水线运行")
	}
}

func TestRetryFailedRunCreatesNewAuditedExecution(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "重新执行")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	failed := model.PipelineRun{
		ID: "failed-run", ApplicationID: application.ID, Trigger: "manual",
		Ref: "refs/heads/main", CommitSHA: strings.Repeat("d", 40),
		Status: model.PipelineRunFailed, Stage: "failed", Message: "构建失败",
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&failed).Error; err != nil {
		t.Fatal(err)
	}

	retried, err := service.RetryRun(context.Background(), failed.ID, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if retried.ID == failed.ID || retried.RetryOfID != failed.ID || retried.Ref != failed.Ref || retried.CommitSHA != failed.CommitSHA {
		t.Fatalf("重新执行没有创建相同代码版本的新运行: %+v", retried)
	}
	if retried.Status != model.PipelineRunRunning || retried.ExecutionJobID == "" {
		t.Fatalf("重新执行没有提交新的真实任务: %+v", retried)
	}
	var original model.PipelineRun
	if err := db.First(&original, "id = ?", failed.ID).Error; err != nil {
		t.Fatal(err)
	}
	if original.Status != model.PipelineRunFailed || original.ExecutionJobID != "" {
		t.Fatalf("重新执行不应覆盖原失败记录: %+v", original)
	}
	var job model.Job
	if err := db.First(&job, "id = ?", retried.ExecutionJobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Kind != "pipeline.deploy" || job.MaxAttempts != 1 {
		t.Fatalf("重新执行任务的副作用保护不正确: %+v", job)
	}
}

func TestRetryRunRejectsNonFailedRun(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "拒绝重复执行")
	now := time.Now().UTC()
	run := model.PipelineRun{
		ID: "succeeded-run", ApplicationID: application.ID, Trigger: "manual",
		Ref: "refs/heads/main", CommitSHA: strings.Repeat("e", 40),
		Status: model.PipelineRunSucceeded, Stage: "completed", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.RetryRun(context.Background(), run.ID, "operator"); !errors.Is(err, ErrPipelineRunNotRetryable) {
		t.Fatalf("成功运行不应允许重新执行: %v", err)
	}
}

func TestRetryWorkflowSourceKeepsMatchingCodeTrigger(t *testing.T) {
	workflow := &model.ReleaseWorkflow{Nodes: []model.WorkflowNode{
		{ID: "trigger-main", Type: model.WorkflowNodeTrigger, Name: "主分支", Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"push", "manual"}}},
	}}
	source := retryWorkflowSource(workflow, &model.PipelineRun{Ref: "refs/heads/main"})
	if source == nil || source.ID != "trigger-main" {
		t.Fatalf("重新执行应保留匹配的代码触发入口: %+v", source)
	}
}

func createManualRunTestApplication(t *testing.T, service *Service, db *gorm.DB, repositoryID, name string) *model.Application {
	t.Helper()
	ctx := context.Background()
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: name + "构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile", ContextPath: ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := service.CreateRegistry(ctx, "admin", RegistryInput{
		Name: name + "镜像", Provider: model.RegistryGeneric, Endpoint: "https://registry.example.com", Namespace: "zrt",
	})
	if err != nil {
		t.Fatal(err)
	}
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: name + "发布", Kind: model.DeploymentPlanDocker, ServiceName: "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	target := model.DeploymentTarget{
		ID: "target-" + strings.ReplaceAll(name, " ", "-"), Name: name + "环境", Platform: model.DeploymentDocker,
		Environment: model.EnvironmentStaging, RuntimeID: "docker-1", WorkloadName: "api", RolloutTimeout: 300,
		IsActive: true, CreatedBy: "admin", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: name, RepositoryID: repositoryID, Branch: "main", WatchPush: true,
		BuildPlanID: buildPlan.ID, ImageRegistryID: registry.ID, DeploymentPlanID: deploymentPlan.ID,
		Environments: []EnvironmentInput{{
			Key: "dev", Name: "开发环境", Branch: "main", WatchPush: true,
			DeploymentPlanID: deploymentPlan.ID, DeploymentTargetID: target.ID,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	workflowResult, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	manualTriggerFound := false
	for i := range workflowResult.Workflow.Nodes {
		node := &workflowResult.Workflow.Nodes[i]
		if node.Type != model.WorkflowNodeTrigger {
			continue
		}
		node.Config.Events = append(node.Config.Events, "manual")
		manualTriggerFound = true
		break
	}
	if !manualTriggerFound {
		t.Fatal("默认流水线缺少代码触发节点")
	}
	if err := db.Save(workflowResult.Workflow).Error; err != nil {
		t.Fatal(err)
	}
	application.Workflow = workflowResult.Workflow
	return application
}

func TestManualWorkflowSourceUsesOnlyManualCodeTriggers(t *testing.T) {
	workflow := &model.ReleaseWorkflow{Nodes: []model.WorkflowNode{
		{ID: "trigger-auto", Type: model.WorkflowNodeTrigger, Name: "自动 main", Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"push"}}},
		{ID: "trigger-test", Type: model.WorkflowNodeTrigger, Name: "手动测试", Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"push", "manual"}}},
		{ID: "trigger-prod", Type: model.WorkflowNodeTrigger, Name: "手动生产", Config: model.WorkflowNodeConfig{Branch: "release/*", Events: []string{"manual"}}},
	}}

	selected := manualWorkflowSource(workflow, "refs/heads/release/v1", "trigger-prod")
	if selected == nil || selected.ID != "trigger-prod" {
		t.Fatalf("没有使用用户选择的发布路径: %+v", selected)
	}
	if manualWorkflowSource(workflow, "refs/heads/main", "trigger-auto") != nil {
		t.Fatal("未开启 manual 的自动代码触发节点不能作为手动来源")
	}
	if fallback := manualWorkflowSource(workflow, "refs/heads/main", ""); fallback != nil {
		t.Fatalf("存在多个手动代码触发节点时不得静默选择入口: %+v", fallback)
	}
	single := &model.ReleaseWorkflow{Nodes: workflow.Nodes[:2]}
	if fallback := manualWorkflowSource(single, "refs/heads/main", ""); fallback == nil || fallback.ID != "trigger-test" {
		t.Fatalf("只有一个手动代码触发节点时应允许兼容缺省入口: %+v", fallback)
	}
}

func TestListApplicationRefsReturnsOnlyManualCodeTriggers(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	commitSHA := strings.Repeat("7", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}},
		}}, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "手动入口列表")
	result, err := service.GetWorkflow(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	result.Workflow.Nodes = append(result.Workflow.Nodes, model.WorkflowNode{
		ID: "trigger-auto-only", Type: model.WorkflowNodeTrigger, Name: "仅自动触发",
		Config: model.WorkflowNodeConfig{Environment: "dev", Branch: "main", Events: []string{"push"}},
	})
	if err := db.Save(result.Workflow).Error; err != nil {
		t.Fatal(err)
	}

	options, err := service.ListApplicationRefs(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(options.ManualSources) != 1 || options.ManualSources[0].ID != "trigger-dev" {
		t.Fatalf("手动入口列表包含了未开启 manual 的代码触发节点: %+v", options.ManualSources)
	}
}

func TestWorkflowAllowsManualOnlyCodeTriggerAsSource(t *testing.T) {
	service, db, _, _ := newPipelineTestService(t)
	ctx := context.Background()
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "手动发布方案", Kind: model.DeploymentPlanDocker, ServiceName: "zrt-api",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "manual-prod-target", Name: "手动发布生产环境", Platform: model.DeploymentDocker,
		Environment: model.EnvironmentProduction, RuntimeID: "docker-1", WorkloadName: "zrt-api",
		RolloutTimeout: 300, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	application := &model.Application{Environments: []model.ApplicationEnvironment{{Key: "prod", Name: "生产环境"}}}
	nodes := []model.WorkflowNode{
		{ID: "trigger-prod", Type: model.WorkflowNodeTrigger, Name: "生产代码", Config: model.WorkflowNodeConfig{Environment: "prod", Branch: "release/*", Events: []string{"manual"}}},
		{ID: "approval-prod", Type: model.WorkflowNodeApproval, Name: "生产发布审核", Config: model.WorkflowNodeConfig{Environment: "prod"}},
		{ID: "deploy-prod", Type: model.WorkflowNodeDeploy, Name: "部署生产", Config: model.WorkflowNodeConfig{Environment: "prod", DeploymentPlanID: deploymentPlan.ID, DeploymentTargetID: target.ID}},
	}
	edges := []model.WorkflowEdge{
		{ID: "edge-trigger-approval", Source: "trigger-prod", Target: "approval-prod"},
		{ID: "edge-approval-deploy", Source: "approval-prod", Target: "deploy-prod"},
	}

	if issues := service.validateWorkflow(ctx, application, nodes, edges); len(issues) != 0 {
		t.Fatalf("只开启 manual 的代码触发入口应该有效: %+v", issues)
	}
}

func TestWorkflowValidatesCodeTriggerAsOnlyEntryType(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "入口连线校验")
	application, err := service.FindApplication(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	var deploy model.WorkflowNode
	for i := range application.Workflow.Nodes {
		if application.Workflow.Nodes[i].Type == model.WorkflowNodeDeploy {
			deploy = application.Workflow.Nodes[i]
			break
		}
	}
	if deploy.ID == "" {
		t.Fatal("默认流水线缺少部署节点")
	}
	trigger := model.WorkflowNode{
		ID: "code-trigger", Type: model.WorkflowNodeTrigger, Name: "main 代码",
		Config: model.WorkflowNodeConfig{Environment: "dev", Branch: "main", Events: []string{"push", "manual"}},
	}
	manualGate := model.WorkflowNode{
		ID: "manual-gate", Type: model.WorkflowNodeManual, Name: "人工放行",
		Config: model.WorkflowNodeConfig{Environment: "dev"},
	}

	validIssues := service.validateWorkflow(context.Background(), application,
		[]model.WorkflowNode{trigger, deploy},
		[]model.WorkflowEdge{
			{ID: "trigger-deploy", Source: trigger.ID, Target: deploy.ID},
		},
	)
	if len(validIssues) != 0 {
		t.Fatalf("包含 manual 选项的代码触发入口应该有效: %+v", validIssues)
	}

	issues := service.validateWorkflow(context.Background(), application,
		[]model.WorkflowNode{manualGate, trigger, deploy},
		[]model.WorkflowEdge{
			{ID: "gate-trigger", Source: manualGate.ID, Target: trigger.ID},
			{ID: "trigger-deploy", Source: trigger.ID, Target: deploy.ID},
		},
	)
	foundUpstream := false
	for i := range issues {
		if issues[i].Code == "trigger_has_upstream" && issues[i].NodeID == trigger.ID {
			foundUpstream = true
			break
		}
	}
	if !foundUpstream {
		t.Fatalf("代码触发入口存在上游时未被拒绝: %+v", issues)
	}

	legacy := model.WorkflowNode{
		ID: "legacy-manual", Type: model.WorkflowNodeManualRelease, Name: "旧手动发布",
		Config: model.WorkflowNodeConfig{Environment: "dev"},
	}
	issues = service.validateWorkflow(context.Background(), application,
		[]model.WorkflowNode{legacy, deploy},
		[]model.WorkflowEdge{{ID: "legacy-deploy", Source: legacy.ID, Target: deploy.ID}},
	)
	foundInvalidType := false
	for i := range issues {
		if issues[i].Code == "invalid_node_type" && issues[i].NodeID == legacy.ID {
			foundInvalidType = true
			break
		}
	}
	if !foundInvalidType {
		t.Fatalf("新工作流仍接受独立 manual_release 节点: %+v", issues)
	}
}

func TestWorkflowRejectsMultipleOutgoingEdges(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "多路径校验")
	application, err := service.FindApplication(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	var deploy model.WorkflowNode
	for i := range application.Workflow.Nodes {
		if application.Workflow.Nodes[i].Type == model.WorkflowNodeDeploy {
			deploy = application.Workflow.Nodes[i]
			break
		}
	}
	if deploy.ID == "" {
		t.Fatal("默认流水线缺少部署节点")
	}
	trigger := model.WorkflowNode{
		ID: "trigger-main", Type: model.WorkflowNodeTrigger, Name: "main 代码",
		Config: model.WorkflowNodeConfig{Environment: "dev", Branch: "main", Events: []string{"push"}},
	}
	firstDeploy, secondDeploy := deploy, deploy
	firstDeploy.ID, firstDeploy.Name = "deploy-first", "部署一"
	secondDeploy.ID, secondDeploy.Name = "deploy-second", "部署二"
	issues := service.validateWorkflow(context.Background(), application,
		[]model.WorkflowNode{trigger, firstDeploy, secondDeploy},
		[]model.WorkflowEdge{
			{ID: "trigger-first", Source: trigger.ID, Target: firstDeploy.ID},
			{ID: "trigger-second", Source: trigger.ID, Target: secondDeploy.ID},
		},
	)
	found := false
	for i := range issues {
		if issues[i].Code == "multiple_outgoing_edges" && issues[i].NodeID == trigger.ID {
			if issues[i].Message != "节点只能连接一个下游节点，自动推进无法选择多条路径" {
				t.Fatalf("多路径校验返回了不稳定文案: %q", issues[i].Message)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("非部署节点的多个出边未被拒绝: %+v", issues)
	}

	rootDeploy := deploy
	rootDeploy.ID, rootDeploy.Name = "deploy-root", "首个部署"
	issues = service.validateWorkflow(context.Background(), application,
		[]model.WorkflowNode{trigger, rootDeploy, firstDeploy, secondDeploy},
		[]model.WorkflowEdge{
			{ID: "trigger-root", Source: trigger.ID, Target: rootDeploy.ID},
			{ID: "root-first", Source: rootDeploy.ID, Target: firstDeploy.ID},
			{ID: "root-second", Source: rootDeploy.ID, Target: secondDeploy.ID},
		},
	)
	found = false
	for i := range issues {
		if issues[i].Code == "multiple_outgoing_edges" && issues[i].NodeID == rootDeploy.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("部署节点的多个出边未被拒绝: %+v", issues)
	}
}

func TestProductionWorkflowDoesNotRequireImplicitApprovalNode(t *testing.T) {
	service, db, _, _ := newPipelineTestService(t)
	deploymentPlan, err := service.CreateDeploymentPlan(context.Background(), "admin", DeploymentPlanInput{
		Name: "无隐式审核部署方案", Kind: model.DeploymentPlanDocker, ServiceName: "zrt-api",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "production-without-approval", Name: "生产环境", Platform: model.DeploymentDocker,
		Environment: model.EnvironmentProduction, RuntimeID: "docker-1", WorkloadName: "zrt-api",
		RolloutTimeout: 300, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	application := &model.Application{
		DeploymentPlanID: deploymentPlan.ID,
		Environments:     []model.ApplicationEnvironment{{Key: "prod", Name: "生产环境"}},
	}
	nodes := []model.WorkflowNode{
		{ID: "trigger-prod", Type: model.WorkflowNodeTrigger, Name: "生产代码", Config: model.WorkflowNodeConfig{Environment: "prod", Branch: "release/*", Events: []string{"manual"}}},
		{ID: "deploy-prod", Type: model.WorkflowNodeDeploy, Name: "部署生产", Config: model.WorkflowNodeConfig{Environment: "prod", DeploymentPlanID: deploymentPlan.ID, DeploymentTargetID: target.ID}},
	}
	edges := []model.WorkflowEdge{{ID: "edge-trigger-deploy", Source: "trigger-prod", Target: "deploy-prod"}}

	issues := service.validateWorkflow(context.Background(), application, nodes, edges)
	if len(issues) != 0 {
		t.Fatalf("没有审核节点的生产发布路径应由画布配置决定: %+v", issues)
	}
}

func TestExplicitApprovalNodeControlsWorkflowRun(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "明确审核节点")
	var target model.DeploymentTarget
	if err := db.First(&target, "id = ?", "target-明确审核节点").Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	workflow := &model.ReleaseWorkflow{
		ID: "explicit-approval-workflow",
		Nodes: []model.WorkflowNode{
			{ID: "trigger", Type: model.WorkflowNodeTrigger, Name: "代码触发", Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"manual"}}},
			{ID: "approval", Type: model.WorkflowNodeApproval, Name: "负责人审核"},
			{ID: "deploy", Type: model.WorkflowNodeDeploy, Name: "执行部署", Config: model.WorkflowNodeConfig{
				Environment: "dev", DeploymentPlanID: application.DeploymentPlanID, DeploymentTargetID: target.ID,
			}},
		},
		Edges: []model.WorkflowEdge{
			{ID: "trigger-approval", Source: "trigger", Target: "approval"},
			{ID: "approval-deploy", Source: "approval", Target: "deploy"},
		},
	}
	run, err := newWorkflowRun(
		application, workflow, workflow.Nodes[0],
		"manual", "refs/heads/main", strings.Repeat("a", 40), "requester", "手动执行", now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(run).Error; err != nil {
		t.Fatal(err)
	}
	run, err = service.AdvanceRun(context.Background(), run.ID, "requester", "")
	if err != nil || run.CurrentNodeID != "approval" || run.Status != model.PipelineRunAwaitingApproval {
		t.Fatalf("明确配置的审核节点没有拦截流水线: run=%+v err=%v", run, err)
	}
	if _, err := service.ApproveRun(context.Background(), run.ID, "requester"); !errors.Is(err, ErrWorkflowSelfApproval) {
		t.Fatalf("审核节点没有阻止申请人自审: %v", err)
	}
	run, err = service.ApproveRun(context.Background(), run.ID, "reviewer")
	if err != nil || run.Status != model.PipelineRunRunning || run.CurrentNodeID != "deploy" || run.Stage != "queued" || run.ExecutionJobID == "" {
		t.Fatalf("审核通过后没有自动进入部署节点: run=%+v err=%v", run, err)
	}
	if run.ApprovedBy == nil || *run.ApprovedBy != "reviewer" {
		t.Fatalf("审核节点没有记录审核人: %+v", run)
	}
}

func TestApplicationRepositoryLinksIgnoreLegacyExtraRepositories(t *testing.T) {
	application := &model.Application{
		RepositoryID: "primary",
		Repositories: []model.ApplicationRepository{
			{RepositoryID: "legacy-extra", SortOrder: 0},
			{RepositoryID: "primary", SortOrder: 1},
		},
	}
	links := applicationRepositoryLinks(application)
	if len(links) != 1 || links[0].RepositoryID != "primary" || links[0].SortOrder != 0 {
		t.Fatalf("应用只能使用主仓库: %+v", links)
	}
}
