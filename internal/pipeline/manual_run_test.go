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
	if !errors.Is(err, ErrPipelineIncomplete) || run.Status != model.PipelineRunBlocked {
		t.Fatalf("缺少代码版本时应创建待选择版本的计划: run=%+v err=%v", run, err)
	}

	run, err = service.ExecuteRun(context.Background(), run.ID, "admin", "refs/heads/main", commitSHA, "")
	if err != nil {
		t.Fatal(err)
	}
	if run.Ref != "refs/heads/main" || run.CommitSHA != commitSHA || run.Status != model.PipelineRunRunning || run.CurrentNodeID != "deploy-dev" || run.ExecutionJobID == "" {
		t.Fatalf("流水线运行没有使用所选版本提交真实执行任务: %+v", run)
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
	if !errors.Is(err, ErrPipelineIncomplete) {
		t.Fatalf("尚未选择代码版本时应该生成配置不完整的运行: %v", err)
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
		{ID: "manual-prod", Type: model.WorkflowNodeManualRelease, Name: "手动生产发布"},
		{ID: "trigger-main", Type: model.WorkflowNodeTrigger, Name: "主分支", Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"push"}}},
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
	return application
}

func TestManualWorkflowSourceUsesSelectedManualReleaseNode(t *testing.T) {
	workflow := &model.ReleaseWorkflow{Nodes: []model.WorkflowNode{
		{ID: "trigger-main", Type: model.WorkflowNodeTrigger, Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"push"}}},
		{ID: "manual-test", Type: model.WorkflowNodeManualRelease, Name: "手动发布测试环境"},
		{ID: "manual-prod", Type: model.WorkflowNodeManualRelease, Name: "手动发布生产环境"},
	}}

	selected := manualWorkflowSource(workflow, "refs/heads/main", "manual-prod")
	if selected == nil || selected.ID != "manual-prod" {
		t.Fatalf("没有使用用户选择的手动发布入口: %+v", selected)
	}
	if manualWorkflowSource(workflow, "refs/heads/main", "trigger-main") != nil {
		t.Fatal("手动执行入口不能指向代码触发节点")
	}
	fallback := manualWorkflowSource(workflow, "refs/heads/main", "")
	if fallback == nil || fallback.ID != "manual-test" {
		t.Fatalf("未指定入口时应使用第一个手动发布节点: %+v", fallback)
	}
}

func TestWorkflowAllowsManualReleaseAsOnlySource(t *testing.T) {
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
		{ID: "manual-prod", Type: model.WorkflowNodeManualRelease, Name: "手动发布", Config: model.WorkflowNodeConfig{Environment: "prod"}},
		{ID: "deploy-prod", Type: model.WorkflowNodeDeploy, Name: "部署生产", Config: model.WorkflowNodeConfig{Environment: "prod", DeploymentPlanID: deploymentPlan.ID, DeploymentTargetID: target.ID}},
	}
	edges := []model.WorkflowEdge{{ID: "edge-manual-deploy", Source: "manual-prod", Target: "deploy-prod"}}

	if issues := service.validateWorkflow(ctx, application, nodes, edges); len(issues) != 0 {
		t.Fatalf("只包含手动发布入口的流水线应该有效: %+v", issues)
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
