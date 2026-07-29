package pipeline

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/credential"
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
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "Helm 发布", Kind: model.DeploymentPlanHelm, HelmChart: "deploy/chart",
	})
	if err != nil {
		t.Fatalf("创建部署方案失败: %v", err)
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
		DeploymentPlanID: deploymentPlan.ID, DeploymentTargetID: target.ID,
	})
	if err != nil {
		t.Fatalf("创建应用失败: %v", err)
	}
	if application.Repository.ID != repositoryID || application.BuildPlan == nil ||
		application.ImageRegistry == nil || application.DeploymentPlan == nil || application.DeploymentTarget == nil {
		t.Fatalf("应用资源关联没有完整加载: %+v", application)
	}
	if len(application.Environments) != 1 || application.Environments[0].DeploymentPlanID != deploymentPlan.ID {
		t.Fatalf("环境没有使用应用级部署方案: %+v", application.Environments)
	}
	workflowResult, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatalf("读取应用流水线失败: %v", err)
	}
	for i := range workflowResult.Workflow.Nodes {
		if workflowResult.Workflow.Nodes[i].Type == model.WorkflowNodeTrigger {
			workflowResult.Workflow.Nodes[i].Config.Events = append(workflowResult.Workflow.Nodes[i].Config.Events, "manual")
			break
		}
	}
	workflowResult, err = service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		Name: workflowResult.Workflow.Name, Revision: workflowResult.Workflow.Revision, Activate: true,
		Nodes: workflowResult.Workflow.Nodes, Edges: workflowResult.Workflow.Edges, Viewport: workflowResult.Workflow.Viewport,
	})
	if err != nil || !workflowResult.Workflow.IsActive {
		t.Fatalf("启用可手动发布的应用流水线失败: result=%+v err=%v", workflowResult, err)
	}

	blocked, err := service.PrepareRun(ctx, application.ID, "admin")
	if err != nil || blocked.Status != model.PipelineRunBlocked {
		t.Fatalf("手动执行应先等待选择代码版本: run=%+v err=%v", blocked, err)
	}
	if blocked.Message != "请选择每个代码仓库要发布的 Commit" {
		t.Fatalf("手动执行提示没有明确要求选择代码版本: %q", blocked.Message)
	}
	if len(blocked.Repositories) != 1 || blocked.Repositories[0].BuildPlanID != buildPlan.ID ||
		blocked.Repositories[0].DeploymentPlanID != deploymentPlan.ID {
		t.Fatalf("流水线运行没有保存应用方案快照: %+v", blocked.Repositories)
	}
	if err := service.DeleteRun(ctx, blocked.ID); err != nil {
		t.Fatalf("删除未执行的阻塞计划失败: %v", err)
	}
	if err := db.Model(&model.Application{}).Where("id = ?", application.ID).Updates(map[string]any{
		"last_observed_ref": "refs/heads/main", "last_observed_commit": "0123456789012345678901234567890123456789",
	}).Error; err != nil {
		t.Fatalf("设置测试代码版本失败: %v", err)
	}
	ready, err := service.PrepareRun(ctx, application.ID, "admin")
	if err != nil || ready.Status != model.PipelineRunBlocked || ready.Ref != "" || ready.CommitSHA != "" {
		t.Fatalf("手动执行不应静默复用后台最近观察到的代码版本: run=%+v err=%v", ready, err)
	}
	updated, err := service.UpdateApplication(ctx, application.ID, ApplicationInput{
		Name: application.Name, RepositoryID: repositoryID, Branch: application.Branch,
		PollEnabled: true, PollIntervalSeconds: 60, WatchPush: true,
		BuildPlanID: buildPlan.ID, ImageRegistrySet: true,
		DeploymentPlanID: deploymentPlan.ID, DeploymentTargetID: target.ID,
	})
	if err != nil {
		t.Fatalf("解绑镜像仓库失败: %v", err)
	}
	if updated.ImageRegistryID != "" || updated.ImageRegistry != nil {
		t.Fatalf("显式选择不绑定时仍保留了镜像仓库: %+v", updated)
	}
}

func TestApplicationRejectsDeploymentPlanTargetMismatch(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	plan, err := service.CreateDeploymentPlan(context.Background(), "admin", DeploymentPlanInput{
		Name: "SSH 命令部署", Kind: model.DeploymentPlanScript, Script: "echo deploy\n", TimeoutSeconds: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "docker-target-for-script", Name: "Docker 目标", Platform: model.DeploymentDocker,
		Environment: model.EnvironmentDevelopment, RuntimeID: "docker-1", WorkloadName: "api",
		RolloutTimeout: 120, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "错误绑定应用", RepositoryID: repositoryID, Branch: "main", WatchPush: true,
		PollIntervalSeconds: 60, DeploymentPlanID: plan.ID, DeploymentTargetID: target.ID,
	}); !errors.Is(err, ErrDeploymentPlanTargetMismatch) {
		t.Fatalf("Script 部署方案绑定 Docker 目标未返回稳定错误: %v", err)
	}
}

func TestWorkflowDraftRejectsDeploymentPlanTargetMismatch(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	plan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "SSH 流水线部署", Kind: model.DeploymentPlanScript, Script: "echo deploy\n", TimeoutSeconds: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sshTarget := model.DeploymentTarget{
		ID: "workflow-ssh-target", Name: "SSH 目标", Platform: model.DeploymentSSH,
		Environment: model.EnvironmentDevelopment, EnvironmentID: "environment-1", HostID: "host-1",
		RolloutTimeout: 120, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	dockerTarget := model.DeploymentTarget{
		ID: "workflow-docker-target", Name: "Docker 目标", Platform: model.DeploymentDocker,
		Environment: model.EnvironmentDevelopment, RuntimeID: "docker-1", WorkloadName: "api",
		RolloutTimeout: 120, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&[]model.DeploymentTarget{sshTarget, dockerTarget}).Error; err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "SSH 流水线应用", RepositoryID: repositoryID, Branch: "main", WatchPush: true,
		PollIntervalSeconds: 60, DeploymentPlanID: plan.ID, DeploymentTargetID: sshTarget.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	nodes, edges := cloneWorkflowGraph(workflow.Workflow.Nodes, workflow.Workflow.Edges)
	for i := range nodes {
		if nodes[i].Type == model.WorkflowNodeDeploy {
			nodes[i].Config.DeploymentTargetID = dockerTarget.ID
		}
	}
	result, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		Name: workflow.Workflow.Name, Revision: workflow.Workflow.Revision, Activate: false,
		Nodes: nodes, Edges: edges, Viewport: workflow.Workflow.Viewport,
	})
	if !errors.Is(err, ErrInvalidWorkflow) || result == nil || !hasWorkflowIssue(result.Issues, "deployment_plan_target_mismatch") {
		t.Fatalf("流水线草稿保存未拒绝方案与目标错配: result=%+v err=%v", result, err)
	}
	stored, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Workflow.Revision != workflow.Workflow.Revision {
		t.Fatalf("被拒绝的错配草稿不应写入数据库: before=%d after=%d", workflow.Workflow.Revision, stored.Workflow.Revision)
	}
}

func TestDeploymentPlanTargetCompatibilityMatrix(t *testing.T) {
	tests := []struct {
		kind     model.DeploymentPlanKind
		platform model.DeploymentPlatform
		want     bool
	}{
		{model.DeploymentPlanScript, model.DeploymentSSH, true},
		{model.DeploymentPlanScript, model.DeploymentDocker, false},
		{model.DeploymentPlanHelm, model.DeploymentKubernetes, true},
		{model.DeploymentPlanHelm, model.DeploymentDocker, false},
		{model.DeploymentPlanDocker, model.DeploymentDocker, true},
		{model.DeploymentPlanCompose, model.DeploymentDocker, true},
		{model.DeploymentPlanDocker, model.DeploymentKubernetes, false},
	}
	for _, test := range tests {
		if got := deploymentPlanSupportsTarget(test.kind, test.platform); got != test.want {
			t.Fatalf("部署方案/目标兼容矩阵错误: kind=%s platform=%s got=%t want=%t", test.kind, test.platform, got, test.want)
		}
	}
}

func TestScriptDeploymentPlanPreservesExactBytes(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	original := "\n  printf 'deploy'  \n\n"
	plan, err := service.CreateDeploymentPlan(context.Background(), "admin", DeploymentPlanInput{
		Name: "保留脚本字节", Kind: model.DeploymentPlanScript, Script: original, TimeoutSeconds: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Script != original {
		t.Fatalf("创建部署方案时修改了脚本原始字节: got=%q want=%q", plan.Script, original)
	}
	updatedScript := "\tprintf 'updated'\n"
	updated, err := service.UpdateDeploymentPlan(context.Background(), plan.ID, DeploymentPlanInput{
		Name: plan.Name, Kind: model.DeploymentPlanScript, Script: updatedScript, TimeoutSeconds: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Script != updatedScript {
		t.Fatalf("更新部署方案时修改了脚本原始字节: got=%q want=%q", updated.Script, updatedScript)
	}
}

func TestPipelineIncompleteMessageListsEveryMissingItem(t *testing.T) {
	application := &model.Application{BuildPlanID: "build-1"}
	message := pipelineIncompleteMessage(application)
	if message != "缺少：代码仓库、部署方案、代码版本" {
		t.Fatalf("阻塞原因没有列出全部缺失项: %q", message)
	}
}

func TestCreateRegistryAcceptsRepositoryStyleDisplayName(t *testing.T) {
	service, _, secretManager, _ := newPipelineTestService(t)
	credential := " ucloud-registry-token "
	registry, err := service.CreateRegistry(context.Background(), "admin", RegistryInput{
		Name:       "uhub.service.ucloud.cn/zrt-application",
		Provider:   model.RegistryGeneric,
		Endpoint:   "https://uhub.service.ucloud.cn",
		Namespace:  "zrt-application",
		Username:   "852519822@qq.com",
		Credential: &credential,
	})
	if err != nil {
		t.Fatalf("合法的 UCloud 镜像仓库配置不应被拒绝: %v", err)
	}
	if registry.Name != "uhub.service.ucloud.cn/zrt-application" ||
		registry.Endpoint != "https://uhub.service.ucloud.cn" || registry.Namespace != "zrt-application" {
		t.Fatalf("镜像仓库配置保存错误: %+v", registry)
	}
	plaintext, err := secretManager.Decrypt(registry.CredentialCiphertext, []byte("image_registry:"+registry.Name+":credential"))
	if err != nil || plaintext != credential {
		t.Fatalf("镜像仓库凭据不应被静默修改: plaintext=%q err=%v", plaintext, err)
	}
}

func TestCreateRegistryReturnsFieldSpecificValidationErrors(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	tests := []struct {
		name  string
		input RegistryInput
		want  error
	}{
		{
			name:  "名称",
			input: RegistryInput{Name: "?", Provider: model.RegistryGeneric, Endpoint: "https://registry.example.com"},
			want:  ErrInvalidRegistryName,
		},
		{
			name:  "类型",
			input: RegistryInput{Name: "测试仓库", Provider: "unknown", Endpoint: "https://registry.example.com"},
			want:  ErrInvalidRegistryProvider,
		},
		{
			name:  "地址",
			input: RegistryInput{Name: "测试仓库", Provider: model.RegistryGeneric, Endpoint: "https://user:password@registry.example.com"},
			want:  ErrInvalidRegistryEndpoint,
		},
		{
			name:  "HTTP",
			input: RegistryInput{Name: "测试仓库", Provider: model.RegistryGeneric, Endpoint: "http://registry.example.com"},
			want:  ErrInsecureRegistryEndpoint,
		},
		{
			name:  "命名空间",
			input: RegistryInput{Name: "测试仓库", Provider: model.RegistryGeneric, Endpoint: "https://registry.example.com", Namespace: "Upper/Project"},
			want:  ErrInvalidRegistryNamespace,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.CreateRegistry(context.Background(), "admin", test.input); !errors.Is(err, test.want) {
				t.Fatalf("镜像仓库字段错误不明确: want=%v err=%v", test.want, err)
			}
		})
	}
}

func TestRegistryLoginUsesOCIAuthentication(t *testing.T) {
	const username = "robot$zrt"
	const password = "registry-token"
	registryServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/" {
			http.NotFound(response, request)
			return
		}
		actualUser, actualPassword, ok := request.BasicAuth()
		if !ok || actualUser != username || actualPassword != password {
			response.Header().Set("WWW-Authenticate", `Basic realm="ZRT test registry"`)
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		response.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		response.WriteHeader(http.StatusOK)
	}))
	defer registryServer.Close()

	service, db, _, _ := newPipelineTestService(t)
	credential := password
	input := RegistryInput{
		Name: "测试镜像仓库", Provider: model.RegistryGeneric, Endpoint: registryServer.URL,
		Username: username, Credential: &credential, AllowInsecureHTTP: true,
	}
	if err := service.TestRegistry(context.Background(), input); err != nil {
		t.Fatalf("正确凭据未通过 OCI 登录测试: %v", err)
	}
	wrongCredential := "wrong-token"
	input.Credential = &wrongCredential
	if err := service.TestRegistry(context.Background(), input); !errors.Is(err, ErrRegistryLoginFailed) {
		t.Fatalf("错误凭据未被识别为登录失败: %v", err)
	}
	var count int64
	if err := db.Model(&model.ImageRegistry{}).Count(&count).Error; err != nil {
		t.Fatalf("查询镜像仓库记录失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("登录测试不应保存镜像仓库: got=%d", count)
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
		{RepositoryID: repositoryID, EventType: "branch_push", Ref: "refs/heads/main", CommitSHA: "commit-main", Message: "修复支付回调\n\n补充异常分支测试"},
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
	var mainRun model.PipelineRun
	if err := db.First(&mainRun, "application_id = ? AND commit_sha = ?", application.ID, "commit-main").Error; err != nil || mainRun.CommitMessage != "修复支付回调" {
		t.Fatalf("Webhook 提交说明没有保存到流水线运行: run=%+v err=%v", mainRun, err)
	}
	var stored model.Application
	if err := db.First(&stored, "id = ?", application.ID).Error; err != nil {
		t.Fatalf("查询应用失败: %v", err)
	}
	if stored.LastObservedCommit != "commit-pr" || stored.SyncStatus != model.ApplicationSyncChanged {
		t.Fatalf("应用没有保存最后一次代码变化: %+v", stored)
	}
}

func TestBackfillCommitMessages(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	repositoryPath := t.TempDir()
	local, err := git.PlainInit(repositoryPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repositoryPath+"/README.md", []byte("ZRT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktree, err := local.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	hash, err := worktree.Commit("补齐历史提交说明\n\n提交正文", &git.CommitOptions{Author: &object.Signature{
		Name: "ZRT Test", Email: "zrt@example.com", When: time.Now().UTC(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repositoryPath+"/README.md", []byte("ZRT pipeline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Commit("当前提交", &git.CommitOptions{Author: &object.Signature{
		Name: "ZRT Test", Email: "zrt@example.com", When: time.Now().UTC().Add(time.Second),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.GitRepository{}).Where("id = ?", repositoryID).Update("clone_url", repositoryPath).Error; err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "历史应用", RepositoryID: repositoryID, Branch: "master",
		PollEnabled: true, PollIntervalSeconds: 60, WatchPush: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := model.PipelineRun{
		ID: "historical-run", ApplicationID: application.ID, Trigger: "poll",
		Ref: "refs/heads/master", CommitSHA: hash.String(), Status: model.PipelineRunDetected,
		Stage: "detected", CreatedBy: "system", CreatedAt: now, UpdatedAt: now,
	}
	component := model.PipelineRunRepository{
		ID: "historical-component", PipelineRunID: run.ID, RepositoryID: repositoryID,
		Ref: run.Ref, CommitSHA: run.CommitSHA, Status: model.PipelineRunRepositoryPending,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&component).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.BackfillCommitMessages(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&run, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.CommitMessage != "补齐历史提交说明" {
		t.Fatalf("历史提交说明未补齐: %q", run.CommitMessage)
	}
}

func TestApplicationRepositoryCheckInterval(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()

	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "默认仓库检查间隔", RepositoryID: repositoryID, PollEnabled: true, WatchPush: true,
	})
	if err != nil {
		t.Fatalf("使用默认仓库检查间隔创建应用失败: %v", err)
	}
	if application.PollIntervalSeconds != 3 {
		t.Fatalf("默认仓库检查间隔错误: got=%d want=3", application.PollIntervalSeconds)
	}

	for _, interval := range []int{3, 5, 10, 60} {
		if !validPollIntervalSeconds(interval) {
			t.Fatalf("设置项中的仓库检查间隔被拒绝: %d", interval)
		}
	}
	if validPollIntervalSeconds(30) {
		t.Fatal("未拒绝设置项之外的仓库检查间隔")
	}
	if got := repositoryWatcherScanInterval(15 * time.Second); got != 3*time.Second {
		t.Fatalf("仓库检查扫描精度错误: got=%s want=3s", got)
	}
}

func TestReleaseWorkflowUsesOnlyExplicitApprovalNodes(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "workflow-build", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile", ContextPath: ".",
	})
	if err != nil {
		t.Fatalf("创建构建方案失败: %v", err)
	}
	registry, err := service.CreateRegistry(ctx, "admin", RegistryInput{
		Name: "workflow-registry", Provider: model.RegistryGeneric, Endpoint: "https://registry.example.com", Namespace: "zrt",
	})
	if err != nil {
		t.Fatalf("创建镜像仓库失败: %v", err)
	}
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "workflow-helm", Kind: model.DeploymentPlanHelm, HelmChart: "deploy/chart",
	})
	if err != nil {
		t.Fatalf("创建部署方案失败: %v", err)
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
		Name: "画布流程应用", RepositoryID: repositoryID,
		BuildPlanID: buildPlan.ID, ImageRegistryID: registry.ID, DeploymentPlanID: deploymentPlan.ID,
		Environments: []EnvironmentInput{
			{Key: "test", Name: "测试环境", Branch: "test", WatchPush: true, WatchPullRequest: true, DeploymentPlanID: deploymentPlan.ID, DeploymentTargetID: testTarget.ID},
			{Key: "prod", Name: "生产环境", Branch: "release", WatchTags: true, TagPattern: "v*", DeploymentPlanID: deploymentPlan.ID, DeploymentTargetID: prodTarget.ID},
		},
	})
	if err != nil {
		t.Fatalf("创建应用失败: %v", err)
	}
	workflowResult, err := service.GetWorkflow(ctx, application.ID)
	if err != nil || !workflowResult.Valid {
		t.Fatalf("默认应用流水线应当有效: result=%+v err=%v", workflowResult, err)
	}
	workflowResult, err = service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		Name: workflowResult.Workflow.Name, Revision: workflowResult.Workflow.Revision, Activate: true,
		Nodes: workflowResult.Workflow.Nodes, Edges: workflowResult.Workflow.Edges,
		Viewport: workflowResult.Workflow.Viewport,
	})
	if err != nil || !workflowResult.Workflow.IsActive {
		t.Fatalf("应用流水线未能启用: result=%+v err=%v", workflowResult, err)
	}
	if err := db.Model(&model.Application{}).Where("id = ?", application.ID).Updates(map[string]any{
		"last_observed_ref": "refs/heads/test", "last_observed_commit": "0123456789012345678901234567890123456789",
	}).Error; err != nil {
		t.Fatalf("设置测试代码版本失败: %v", err)
	}
	application, err = service.FindApplication(ctx, application.ID)
	if err != nil {
		t.Fatalf("重新读取应用失败: %v", err)
	}
	var source *model.WorkflowNode
	for i := range application.Workflow.Nodes {
		if application.Workflow.Nodes[i].ID == "trigger-test" {
			source = &application.Workflow.Nodes[i]
			break
		}
	}
	if source == nil {
		t.Fatal("启用后的流水线缺少测试代码触发节点")
	}
	runNow := time.Now().UTC()
	run, err := service.newResolvedWorkflowRun(
		ctx, application, application.Workflow, *source, "poll_push",
		application.LastObservedRef, application.LastObservedCommit, "system", "检测到测试分支更新", runNow,
	)
	if err != nil {
		t.Fatalf("创建代码事件流水线运行失败: %v", err)
	}
	components := pipelineRunRepositories(application, run.ID, run.Ref, run.CommitSHA, runNow)
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		return tx.Create(&components).Error
	}); err != nil {
		t.Fatalf("保存代码事件流水线运行失败: %v", err)
	}
	run.Repositories = components
	if run.CurrentNodeID != "trigger-test" {
		t.Fatalf("流水线运行没有从测试触发节点启动: run=%+v err=%v", run, err)
	}
	if run.ApprovalRequired {
		t.Fatalf("默认流水线不应按生产环境隐式增加审核要求: %+v", run)
	}
	listedRuns, err := service.ListRuns(ctx, 10)
	if err != nil || len(listedRuns) == 0 || listedRuns[0].ApprovalRequired || listedRuns[0].CurrentNodeName == "" {
		t.Fatalf("流水线运行列表错误地增加了审核要求: runs=%+v err=%v", listedRuns, err)
	}
	if listedRuns[0].ExecutionGraph == nil || len(listedRuns[0].ExecutionGraph.Nodes) != len(workflowResult.Workflow.Nodes) || len(listedRuns[0].ExecutionGraph.Edges) != len(workflowResult.Workflow.Edges) {
		t.Fatalf("流水线运行列表没有返回只读执行拓扑: graph=%+v", listedRuns[0].ExecutionGraph)
	}
	run, err = service.AdvanceRun(ctx, run.ID, "admin", "")
	if err != nil || run.CurrentNodeID != "deploy-test" || run.Status != model.PipelineRunRunning {
		t.Fatalf("流水线运行没有提交测试部署任务: run=%+v err=%v", run, err)
	}
	if err := db.Model(&model.PipelineRun{}).Where("id = ?", run.ID).Updates(map[string]any{"status": model.PipelineRunReady, "stage": "deploy_succeeded"}).Error; err != nil {
		t.Fatal(err)
	}
	run.Status, run.Stage = model.PipelineRunReady, "deploy_succeeded"
	for _, expectedNode := range []string{"promote-prod", "deploy-prod"} {
		run, err = service.AdvanceRun(ctx, run.ID, "admin", "")
		if err != nil || run.CurrentNodeID != expectedNode {
			t.Fatalf("流水线运行没有进入节点 %s: run=%+v err=%v", expectedNode, run, err)
		}
	}
	if run.Status != model.PipelineRunRunning {
		t.Fatalf("生产部署被环境级别隐式拦截: %+v", run)
	}
	if err := db.Model(&model.PipelineRun{}).Where("id = ?", run.ID).Updates(map[string]any{"status": model.PipelineRunReady, "stage": "deploy_succeeded"}).Error; err != nil {
		t.Fatal(err)
	}
	run.Status, run.Stage = model.PipelineRunReady, "deploy_succeeded"
	run, err = service.AdvanceRun(ctx, run.ID, "reviewer", "")
	if err != nil || run.Status != model.PipelineRunSucceeded {
		t.Fatalf("流水线运行未能完成: run=%+v err=%v", run, err)
	}
	if err := db.Model(&model.PipelineRun{}).Where("id = ?", run.ID).Update("status", model.PipelineRunRunning).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteRun(ctx, run.ID); err != nil {
		t.Fatalf("执行中的流水线运行也应该可以删除: %v", err)
	}
	if err := db.First(&model.PipelineRun{}, "id = ?", run.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("流水线运行没有删除: %v", err)
	}
	if err := db.Model(&model.ApplicationEnvironment{}).
		Where("application_id = ? AND key = ?", application.ID, "test").
		Updates(map[string]any{"last_observed_ref": "refs/heads/test", "last_observed_commit": "preserved-commit"}).Error; err != nil {
		t.Fatalf("设置环境监听基线失败: %v", err)
	}
	updated, err := service.UpdateApplication(ctx, application.ID, ApplicationInput{
		Name: application.Name, Description: "只修改应用说明", RepositoryID: repositoryID,
		Environments: []EnvironmentInput{
			{Key: "test", Name: "测试环境", Branch: "test", WatchPush: true, WatchPullRequest: true, DeploymentPlanID: deploymentPlan.ID, DeploymentTargetID: testTarget.ID},
			{Key: "prod", Name: "生产环境", Branch: "release", WatchTags: true, TagPattern: "v*", DeploymentPlanID: deploymentPlan.ID, DeploymentTargetID: prodTarget.ID},
		},
	})
	if err != nil || updated.Workflow == nil || !updated.Workflow.IsActive || updated.Environments[0].LastObservedCommit != "preserved-commit" {
		t.Fatalf("只修改应用说明时不应停用计划或丢失环境基线: application=%+v err=%v", updated, err)
	}
}

func TestPublicWorkflowTemplateSyncsLinkedApplications(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "公共模板 Helm", Kind: model.DeploymentPlanHelm, HelmChart: "deploy/chart",
	})
	if err != nil {
		t.Fatalf("创建部署方案失败: %v", err)
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
		{ID: "deploy-test", Type: model.WorkflowNodeDeploy, Name: "部署测试", Position: model.WorkflowPosition{X: 360, Y: 80}, Config: model.WorkflowNodeConfig{Environment: "test", DeploymentPlanID: deploymentPlan.ID, DeploymentTargetID: testTarget.ID}},
		{ID: "promote-prod", Type: model.WorkflowNodeManual, Name: "放行生产", Position: model.WorkflowPosition{X: 640, Y: 80}, Config: model.WorkflowNodeConfig{Environment: "prod"}},
		{ID: "approve-prod", Type: model.WorkflowNodeApproval, Name: "生产审核", Position: model.WorkflowPosition{X: 920, Y: 80}, Config: model.WorkflowNodeConfig{Environment: "prod"}},
		{ID: "deploy-prod", Type: model.WorkflowNodeDeploy, Name: "部署生产", Position: model.WorkflowPosition{X: 1200, Y: 80}, Config: model.WorkflowNodeConfig{Environment: "prod", DeploymentPlanID: deploymentPlan.ID, DeploymentTargetID: prodTarget.ID}},
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
		t.Fatalf("公共流水线方案未能启用: result=%+v err=%v", templateResult, err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "使用公共计划的应用", RepositoryID: repositoryID, PollIntervalSeconds: 60,
		WorkflowTemplateID: templateResult.WorkflowTemplate.ID,
	})
	if err != nil {
		t.Fatalf("使用公共流水线方案创建应用失败: %v", err)
	}
	if application.WorkflowTemplate == nil || application.Workflow == nil || !application.Workflow.IsActive ||
		application.Workflow.WorkflowTemplateRevision != 1 || len(application.Environments) != 2 {
		t.Fatalf("应用没有完整关联流水线方案: %+v", application)
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
		t.Fatalf("更新公共流水线方案失败: %v", err)
	}
	stored, err := service.GetWorkflow(ctx, application.ID)
	if err != nil || stored.Workflow.Nodes[0].Name != "修改后的测试触发" || stored.Workflow.WorkflowTemplateRevision != 2 {
		t.Fatalf("启用方案的新版本没有同步到关联应用: workflow=%+v err=%v", stored, err)
	}
	updatedApplication, err := service.UpdateApplication(ctx, application.ID, ApplicationInput{
		Name: application.Name, Description: "只修改应用说明", RepositoryID: repositoryID,
		PollIntervalSeconds: 60, WorkflowTemplateID: templateResult.WorkflowTemplate.ID,
	})
	if err != nil || updatedApplication.Workflow == nil || !updatedApplication.Workflow.IsActive ||
		updatedApplication.Environments[0].Branch != "test" {
		t.Fatalf("修改应用说明不应停用关联流水线: application=%+v err=%v", updatedApplication, err)
	}
	stored, err = service.GetWorkflow(ctx, application.ID)
	if err != nil || stored.Workflow.Nodes[0].Name != "修改后的测试触发" || stored.Workflow.WorkflowTemplateRevision != 2 {
		t.Fatalf("修改应用说明不应回退已经同步的流水线: workflow=%+v err=%v", stored, err)
	}
	if err := service.DeleteWorkflowTemplate(ctx, templateResult.WorkflowTemplate.ID); !errors.Is(err, ErrWorkflowTemplateInUse) {
		t.Fatalf("仍被应用使用的流水线方案不应允许删除: %v", err)
	}

	customNodes, customEdges := cloneWorkflowGraph(stored.Workflow.Nodes, stored.Workflow.Edges)
	customNodes[0].Name = "应用自定义触发"
	custom, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		Name: stored.Workflow.Name, Revision: stored.Workflow.Revision, Activate: true,
		Nodes: customNodes, Edges: customEdges, Viewport: stored.Workflow.Viewport,
	})
	if err != nil || custom.Workflow.WorkflowTemplateID != "" || custom.Workflow.WorkflowTemplateRevision != 0 {
		t.Fatalf("单独修改应用流水线后没有解除方案关联: workflow=%+v err=%v", custom, err)
	}
	var detached model.Application
	if err := db.First(&detached, "id = ?", application.ID).Error; err != nil || detached.WorkflowTemplateID != "" {
		t.Fatalf("应用仍然引用已经解除关联的流水线方案: application=%+v err=%v", detached, err)
	}

	thirdNodes, thirdEdges := cloneWorkflowGraph(updatedNodes, updatedEdges)
	thirdNodes[0].Name = "方案第三版触发"
	if _, err := service.SaveWorkflowTemplate(ctx, templateResult.WorkflowTemplate.ID, "admin", WorkflowTemplateInput{
		Description:   "模板第三版",
		WorkflowInput: WorkflowInput{Name: "测试到生产", Revision: 2, Activate: true, Nodes: thirdNodes, Edges: thirdEdges, Viewport: model.WorkflowViewport{Zoom: 0.8}},
	}); err != nil {
		t.Fatalf("更新解除关联后的流水线方案失败: %v", err)
	}
	stored, err = service.GetWorkflow(ctx, application.ID)
	if err != nil || stored.Workflow.Nodes[0].Name != "应用自定义触发" {
		t.Fatalf("方案更新覆盖了应用的自定义流水线: workflow=%+v err=%v", stored, err)
	}
	if err := service.DeleteWorkflowTemplate(ctx, templateResult.WorkflowTemplate.ID); err != nil {
		t.Fatalf("解除全部应用关联后仍不能删除流水线方案: %v", err)
	}
	unused, err := service.CreateWorkflowTemplate(ctx, "admin", WorkflowTemplateInput{
		Description:   "可删除的未使用方案",
		WorkflowInput: WorkflowInput{Name: "未使用流水线方案", Nodes: nodes, Edges: edges, Viewport: model.WorkflowViewport{Zoom: 0.8}},
	})
	if err != nil {
		t.Fatalf("创建未使用流水线方案失败: %v", err)
	}
	if err := service.DeleteWorkflowTemplate(ctx, unused.WorkflowTemplate.ID); err != nil {
		t.Fatalf("删除未使用流水线方案失败: %v", err)
	}
	if _, err := service.GetWorkflowTemplate(ctx, unused.WorkflowTemplate.ID); !errors.Is(err, ErrWorkflowTemplateNotFound) {
		t.Fatalf("已删除的流水线方案仍可读取: %v", err)
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
		db, secretManager, credential.NewService(db, secretManager), repository.NewGitClient(config.Git{Timeout: time.Second}), 4,
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
