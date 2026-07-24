package pipeline

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/model"
	"zrt/internal/repository"
	"zrt/internal/secret"
)

func TestResourcesCanBeBoundAndPrepared(t *testing.T) {
	service, db, secretManager, repositoryID := newPipelineTestService(t)
	ctx := context.Background()

	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "镜像构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile",
	})
	if err != nil {
		t.Fatalf("创建构建方案失败: %v", err)
	}
	credential := "registry-password"
	registry, err := service.CreateRegistry(ctx, "admin", RegistryInput{
		Name: "团队镜像仓库", Provider: model.RegistryHarbor,
		Endpoint: "https://harbor.example.com", Namespace: "zrt", Username: "robot",
		Credential: &credential,
	})
	if err != nil {
		t.Fatalf("创建镜像仓库失败: %v", err)
	}
	if registry.CredentialCiphertext == "" || registry.CredentialCiphertext == credential {
		t.Fatal("镜像仓库凭据没有加密保存")
	}
	plaintext, err := secretManager.Decrypt(registry.CredentialCiphertext, []byte("image_registry:"+registry.Name+":credential"))
	if err != nil || plaintext != credential {
		t.Fatalf("镜像仓库凭据无法解密: plaintext=%q err=%v", plaintext, err)
	}
	releasePlan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Name: "Helm 发布", Kind: model.ReleasePlanHelm, HelmChart: "deploy/chart",
	})
	if err != nil {
		t.Fatalf("创建发布方案失败: %v", err)
	}
	target := model.DeploymentTarget{
		ID: "target-1", Name: "测试环境", Platform: model.DeploymentKubernetes,
		Environment: model.EnvironmentDevelopment, RuntimeID: "cluster-1", Namespace: "default",
		WorkloadName: "zrt-api", ContainerName: "api", RolloutTimeout: 300,
		IsActive: true, CreatedBy: "admin", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("创建测试发布目标失败: %v", err)
	}

	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "订单服务", RepositoryID: repositoryID, Branch: "main",
		PollEnabled: true, PollIntervalSeconds: 60, WatchPush: true,
		BuildPlanID: buildPlan.ID, ImageRegistryID: registry.ID,
		ReleasePlanID: releasePlan.ID, DeploymentTargetID: target.ID,
	})
	if err != nil {
		t.Fatalf("创建应用失败: %v", err)
	}
	if application.Repository.ID != repositoryID || application.BuildPlan == nil ||
		application.ImageRegistry == nil || application.ReleasePlan == nil || application.DeploymentTarget == nil {
		t.Fatalf("应用资源关联没有完整加载: %+v", application)
	}

	blocked, err := service.PrepareRun(ctx, application.ID, "admin")
	if err != ErrPipelineIncomplete || blocked.Status != model.PipelineRunBlocked {
		t.Fatalf("未观察到代码版本时流水线应被阻止: run=%+v err=%v", blocked, err)
	}
	if err := db.Model(&model.Application{}).Where("id = ?", application.ID).Updates(map[string]any{
		"last_observed_ref": "refs/heads/main", "last_observed_commit": "0123456789012345678901234567890123456789",
	}).Error; err != nil {
		t.Fatalf("设置测试代码版本失败: %v", err)
	}
	ready, err := service.PrepareRun(ctx, application.ID, "admin")
	if err != nil || ready.Status != model.PipelineRunReady || ready.Stage != "configured" {
		t.Fatalf("完整绑定的流水线未进入就绪状态: run=%+v err=%v", ready, err)
	}
}

func TestRepositoryEventsFollowApplicationTriggers(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "支付服务", RepositoryID: repositoryID, Branch: "main",
		PollEnabled: true, PollIntervalSeconds: 60, WatchPush: true,
		WatchPullRequest: true, WatchTags: true, TagPattern: "v*",
	})
	if err != nil {
		t.Fatalf("创建应用失败: %v", err)
	}

	events := []repository.WebhookTaskPayload{
		{RepositoryID: repositoryID, EventType: "branch_push", Ref: "refs/heads/feature", CommitSHA: "skip-feature"},
		{RepositoryID: repositoryID, EventType: "branch_push", Ref: "refs/heads/main", CommitSHA: "commit-main"},
		{RepositoryID: repositoryID, EventType: "branch_push", Ref: "refs/heads/main", CommitSHA: "commit-main"},
		{RepositoryID: repositoryID, EventType: "tag_push", Ref: "refs/tags/nightly", CommitSHA: "skip-tag"},
		{RepositoryID: repositoryID, EventType: "tag_push", Ref: "refs/tags/v1.0.0", CommitSHA: "commit-tag"},
		{RepositoryID: repositoryID, EventType: "pull_request", Ref: "refs/heads/main", CommitSHA: "commit-pr"},
	}
	for _, event := range events {
		if err := service.HandleRepositoryEvent(ctx, event); err != nil {
			t.Fatalf("处理仓库事件失败: event=%+v err=%v", event, err)
		}
	}
	var count int64
	if err := db.Model(&model.PipelineRun{}).Where("application_id = ?", application.ID).Count(&count).Error; err != nil {
		t.Fatalf("查询流水线记录失败: %v", err)
	}
	if count != 3 {
		t.Fatalf("触发规则生成了错误的流水线记录数: got=%d want=3", count)
	}
	var stored model.Application
	if err := db.First(&stored, "id = ?", application.ID).Error; err != nil {
		t.Fatalf("查询应用失败: %v", err)
	}
	if stored.LastObservedCommit != "commit-pr" || stored.SyncStatus != model.ApplicationSyncChanged {
		t.Fatalf("应用没有保存最后一次代码变化: %+v", stored)
	}
}

func TestReleaseWorkflowRequiresIndependentApproval(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	releasePlan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Name: "workflow-helm", Kind: model.ReleasePlanHelm, HelmChart: "deploy/chart",
	})
	if err != nil {
		t.Fatalf("创建发布方案失败: %v", err)
	}
	now := time.Now().UTC()
	testTarget := model.DeploymentTarget{
		ID: "workflow-test-target", Name: "workflow-test", Platform: model.DeploymentKubernetes,
		Environment: model.EnvironmentStaging, RuntimeID: "cluster-1", Namespace: "default",
		WorkloadName: "zrt-test", ContainerName: "api", RolloutTimeout: 300,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	prodTarget := model.DeploymentTarget{
		ID: "workflow-prod-target", Name: "workflow-prod", Platform: model.DeploymentKubernetes,
		Environment: model.EnvironmentProduction, RuntimeID: "cluster-1", Namespace: "default",
		WorkloadName: "zrt-prod", ContainerName: "api", RolloutTimeout: 300,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&[]model.DeploymentTarget{testTarget, prodTarget}).Error; err != nil {
		t.Fatalf("创建发布目标失败: %v", err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "审批流程应用", RepositoryID: repositoryID, ReleaseApprovalEnabled: true,
		Environments: []EnvironmentInput{
			{Key: "test", Name: "测试环境", Branch: "test", WatchPush: true, WatchPullRequest: true, ReleasePlanID: releasePlan.ID, DeploymentTargetID: testTarget.ID},
			{Key: "prod", Name: "生产环境", Branch: "release", WatchTags: true, TagPattern: "v*", ReleasePlanID: releasePlan.ID, DeploymentTargetID: prodTarget.ID},
		},
	})
	if err != nil {
		t.Fatalf("创建带审核的应用失败: %v", err)
	}
	workflowResult, err := service.GetWorkflow(ctx, application.ID)
	if err != nil || !workflowResult.Valid {
		t.Fatalf("默认发布计划应当有效: result=%+v err=%v", workflowResult, err)
	}
	workflowResult, err = service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		Name: workflowResult.Workflow.Name, Revision: workflowResult.Workflow.Revision, Activate: true,
		Nodes: workflowResult.Workflow.Nodes, Edges: workflowResult.Workflow.Edges,
		Viewport: workflowResult.Workflow.Viewport,
	})
	if err != nil || !workflowResult.Workflow.IsActive {
		t.Fatalf("发布计划未能启用: result=%+v err=%v", workflowResult, err)
	}
	if err := db.Model(&model.Application{}).Where("id = ?", application.ID).Updates(map[string]any{
		"last_observed_ref": "refs/heads/test", "last_observed_commit": "0123456789012345678901234567890123456789",
	}).Error; err != nil {
		t.Fatalf("设置测试代码版本失败: %v", err)
	}
	run, err := service.PrepareRun(ctx, application.ID, "admin")
	if err != nil || run.CurrentNodeID != "trigger-test" {
		t.Fatalf("发布计划没有从测试触发节点启动: run=%+v err=%v", run, err)
	}
	for _, expectedNode := range []string{"deploy-test", "promote-prod", "approval-prod"} {
		run, err = service.AdvanceRun(ctx, run.ID, "admin", "")
		if err != nil || run.CurrentNodeID != expectedNode {
			t.Fatalf("发布计划没有进入节点 %s: run=%+v err=%v", expectedNode, run, err)
		}
	}
	if run.Status != model.PipelineRunAwaitingApproval {
		t.Fatalf("生产发布没有等待审核: %+v", run)
	}
	if _, err := service.ApproveRun(ctx, run.ID, "admin"); !errors.Is(err, ErrWorkflowSelfApproval) {
		t.Fatalf("申请人不应能审核自己的发布计划: %v", err)
	}
	run, err = service.ApproveRun(ctx, run.ID, "reviewer")
	if err != nil || run.Status != model.PipelineRunRunning {
		t.Fatalf("其他成员未能通过审核: run=%+v err=%v", run, err)
	}
	run, err = service.AdvanceRun(ctx, run.ID, "reviewer", "")
	if err != nil || run.CurrentNodeID != "deploy-prod" || run.Status != model.PipelineRunReady {
		t.Fatalf("审核后未能进入生产部署: run=%+v err=%v", run, err)
	}
	run, err = service.AdvanceRun(ctx, run.ID, "reviewer", "")
	if err != nil || run.Status != model.PipelineRunSucceeded {
		t.Fatalf("发布计划未能完成: run=%+v err=%v", run, err)
	}
	if err := db.Model(&model.ApplicationEnvironment{}).
		Where("application_id = ? AND key = ?", application.ID, "test").
		Updates(map[string]any{"last_observed_ref": "refs/heads/test", "last_observed_commit": "preserved-commit"}).Error; err != nil {
		t.Fatalf("设置环境监听基线失败: %v", err)
	}
	updated, err := service.UpdateApplication(ctx, application.ID, ApplicationInput{
		Name: application.Name, Description: "只修改应用说明", RepositoryID: repositoryID,
		ReleaseApprovalEnabled: true,
		Environments: []EnvironmentInput{
			{Key: "test", Name: "测试环境", Branch: "test", WatchPush: true, WatchPullRequest: true, ReleasePlanID: releasePlan.ID, DeploymentTargetID: testTarget.ID},
			{Key: "prod", Name: "生产环境", Branch: "release", WatchTags: true, TagPattern: "v*", ReleasePlanID: releasePlan.ID, DeploymentTargetID: prodTarget.ID},
		},
	})
	if err != nil || updated.Workflow == nil || !updated.Workflow.IsActive || updated.Environments[0].LastObservedCommit != "preserved-commit" {
		t.Fatalf("只修改应用说明时不应停用计划或丢失环境基线: application=%+v err=%v", updated, err)
	}
}

func TestPublicWorkflowTemplateIsCopiedWhenApplicationIsCreated(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	releasePlan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Name: "公共模板 Helm", Kind: model.ReleasePlanHelm, HelmChart: "deploy/chart",
	})
	if err != nil {
		t.Fatalf("创建发布方案失败: %v", err)
	}
	now := time.Now().UTC()
	testTarget := model.DeploymentTarget{
		ID: "template-test-target", Name: "模板测试目标", Platform: model.DeploymentKubernetes,
		Environment: model.EnvironmentStaging, RuntimeID: "cluster-1", Namespace: "default",
		WorkloadName: "zrt-test", ContainerName: "api", RolloutTimeout: 300,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	prodTarget := model.DeploymentTarget{
		ID: "template-prod-target", Name: "模板生产目标", Platform: model.DeploymentKubernetes,
		Environment: model.EnvironmentProduction, RuntimeID: "cluster-1", Namespace: "default",
		WorkloadName: "zrt-prod", ContainerName: "api", RolloutTimeout: 300,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&[]model.DeploymentTarget{testTarget, prodTarget}).Error; err != nil {
		t.Fatalf("创建发布目标失败: %v", err)
	}
	nodes := []model.WorkflowNode{
		{ID: "trigger-test", Type: model.WorkflowNodeTrigger, Name: "测试分支", Position: model.WorkflowPosition{X: 80, Y: 80}, Config: model.WorkflowNodeConfig{Environment: "test", Branch: "test", Events: []string{"push", "pr"}}},
		{ID: "deploy-test", Type: model.WorkflowNodeDeploy, Name: "部署测试", Position: model.WorkflowPosition{X: 360, Y: 80}, Config: model.WorkflowNodeConfig{Environment: "test", ReleasePlanID: releasePlan.ID, DeploymentTargetID: testTarget.ID}},
		{ID: "promote-prod", Type: model.WorkflowNodeManual, Name: "放行生产", Position: model.WorkflowPosition{X: 640, Y: 80}, Config: model.WorkflowNodeConfig{Environment: "prod"}},
		{ID: "approve-prod", Type: model.WorkflowNodeApproval, Name: "生产审核", Position: model.WorkflowPosition{X: 920, Y: 80}, Config: model.WorkflowNodeConfig{Environment: "prod"}},
		{ID: "deploy-prod", Type: model.WorkflowNodeDeploy, Name: "部署生产", Position: model.WorkflowPosition{X: 1200, Y: 80}, Config: model.WorkflowNodeConfig{Environment: "prod", ReleasePlanID: releasePlan.ID, DeploymentTargetID: prodTarget.ID}},
	}
	edges := []model.WorkflowEdge{
		{ID: "edge-1", Source: "trigger-test", Target: "deploy-test"},
		{ID: "edge-2", Source: "deploy-test", Target: "promote-prod"},
		{ID: "edge-3", Source: "promote-prod", Target: "approve-prod"},
		{ID: "edge-4", Source: "approve-prod", Target: "deploy-prod"},
	}
	templateResult, err := service.CreateWorkflowTemplate(ctx, "admin", WorkflowTemplateInput{
		Description:   "测试通过后发布生产",
		WorkflowInput: WorkflowInput{Name: "测试到生产", Activate: true, Nodes: nodes, Edges: edges, Viewport: model.WorkflowViewport{Zoom: 0.8}},
	})
	if err != nil || !templateResult.Valid || !templateResult.WorkflowTemplate.IsActive {
		t.Fatalf("公共发布计划未能启用: result=%+v err=%v", templateResult, err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "使用公共计划的应用", RepositoryID: repositoryID, PollIntervalSeconds: 60,
		WorkflowTemplateID: templateResult.WorkflowTemplate.ID, ReleaseApprovalEnabled: true,
	})
	if err != nil {
		t.Fatalf("使用公共发布计划创建应用失败: %v", err)
	}
	if application.WorkflowTemplate == nil || application.Workflow == nil || !application.Workflow.IsActive ||
		application.Workflow.WorkflowTemplateRevision != 1 || len(application.Environments) != 2 {
		t.Fatalf("应用没有完整复制公共发布计划: %+v", application)
	}
	if application.Environments[0].Key != "test" || application.Environments[0].Branch != "test" || !application.Environments[0].WatchPullRequest {
		t.Fatalf("应用环境没有从画布节点生成: %+v", application.Environments)
	}

	updatedNodes, updatedEdges := cloneWorkflowGraph(nodes, edges)
	updatedNodes[0].Name = "修改后的测试触发"
	if _, err := service.SaveWorkflowTemplate(ctx, templateResult.WorkflowTemplate.ID, "admin", WorkflowTemplateInput{
		Description:   "模板第二版",
		WorkflowInput: WorkflowInput{Name: "测试到生产", Revision: 1, Activate: true, Nodes: updatedNodes, Edges: updatedEdges, Viewport: model.WorkflowViewport{Zoom: 0.8}},
	}); err != nil {
		t.Fatalf("更新公共发布计划失败: %v", err)
	}
	updatedApplication, err := service.UpdateApplication(ctx, application.ID, ApplicationInput{
		Name: application.Name, Description: "只修改应用说明", RepositoryID: repositoryID,
		PollIntervalSeconds: 60, WorkflowTemplateID: templateResult.WorkflowTemplate.ID,
		ReleaseApprovalEnabled: true,
	})
	if err != nil || updatedApplication.Workflow == nil || !updatedApplication.Workflow.IsActive ||
		updatedApplication.Environments[0].Branch != "test" {
		t.Fatalf("修改应用不应重新套用公共计划的新版本: application=%+v err=%v", updatedApplication, err)
	}
	stored, err := service.GetWorkflow(ctx, application.ID)
	if err != nil || stored.Workflow.Nodes[0].Name != "测试分支" || stored.Workflow.WorkflowTemplateRevision != 1 {
		t.Fatalf("模板更新不应静默改变应用快照: workflow=%+v err=%v", stored, err)
	}
}

func newPipelineTestService(t *testing.T) (*Service, *gorm.DB, *secret.Manager, string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开流水线测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移流水线测试数据库失败: %v", err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	secretManager, err := secret.New(key)
	if err != nil {
		t.Fatalf("初始化流水线测试密钥失败: %v", err)
	}
	repositoryService := repository.NewService(
		db, secretManager, repository.NewGitClient(config.Git{Command: "git", Timeout: time.Second}), 4,
	)
	repo, _, err := repositoryService.Create(context.Background(), "admin", repository.Input{
		Name: "pipeline-repository", Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/app.git", DefaultBranch: "main", AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatalf("创建流水线测试仓库失败: %v", err)
	}
	return NewService(db, repositoryService, secretManager), db, secretManager, repo.ID
}
