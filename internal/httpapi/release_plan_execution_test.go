package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/account"
	"edo/internal/audit"
	"edo/internal/auth"
	"edo/internal/cache"
	"edo/internal/config"
	"edo/internal/configuration"
	"edo/internal/credential"
	"edo/internal/database"
	"edo/internal/deployment"
	"edo/internal/dockerengine"
	"edo/internal/logging"
	"edo/internal/model"
	"edo/internal/pipeline"
	"edo/internal/repository"
	"edo/internal/secret"
)

func TestReleasePlanExecutionRequiresDeliveryExecute(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	adminLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if adminLogin.Code != http.StatusOK {
		t.Fatalf("管理员登录失败: status=%d body=%s", adminLogin.Code, adminLogin.Body.String())
	}
	adminCookie := adminLogin.Result().Cookies()[0]

	roleResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/roles", map[string]any{
		"name": "release-plan-reader", "display_name": "发布计划查看员", "permissions": []string{"delivery.read"},
	}, adminCookie)
	var rolePayload struct {
		Role struct {
			ID string `json:"id"`
		} `json:"role"`
	}
	if roleResponse.Code != http.StatusCreated || json.Unmarshal(roleResponse.Body.Bytes(), &rolePayload) != nil || rolePayload.Role.ID == "" {
		t.Fatalf("创建测试角色失败: status=%d body=%s", roleResponse.Code, roleResponse.Body.String())
	}

	userResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "release-plan-reader", "nickname": "发布计划查看员", "password": "correct horse battery staple",
		"role_ids": []string{rolePayload.Role.ID},
	}, adminCookie)
	var userPayload struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if userResponse.Code != http.StatusCreated || json.Unmarshal(userResponse.Body.Bytes(), &userPayload) != nil || userPayload.User.ID == "" {
		t.Fatalf("创建测试用户失败: status=%d body=%s", userResponse.Code, userResponse.Body.String())
	}

	readerLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "release-plan-reader", "password": "correct horse battery staple",
	}, nil)
	if readerLogin.Code != http.StatusOK {
		t.Fatalf("测试用户登录失败: status=%d body=%s", readerLogin.Code, readerLogin.Body.String())
	}
	readerCookie := readerLogin.Result().Cookies()[0]

	denied := performJSONRequest(t, router, http.MethodPost, "/api/v1/release-plans/not-found/executions", map[string]any{}, readerCookie)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺少 delivery.execute 时未拒绝执行发布计划: status=%d body=%s", denied.Code, denied.Body.String())
	}

	grant := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userPayload.User.ID+"/permissions", map[string]any{
		"allow": []string{"delivery.execute"}, "deny": []string{},
	}, adminCookie)
	if grant.Code != http.StatusNoContent {
		t.Fatalf("授予 delivery.execute 失败: status=%d body=%s", grant.Code, grant.Body.String())
	}
	invalid := performJSONRequest(t, router, http.MethodPost, "/api/v1/release-plans/not-found/executions", map[string]any{}, readerCookie)
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), `"code":"invalid_release_plan_execution"`) {
		t.Fatalf("授予 delivery.execute 后应进入请求校验并返回 400: status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestCreateReleasePlanExecutionReturnsAccepted(t *testing.T) {
	const commitSHA = "0123456789abcdef0123456789abcdef01234567"
	fixture := newReleasePlanExecutionHTTPFixture(t, commitSHA)
	defer fixture.close()

	ctx := context.Background()
	repositoryItem, _, err := fixture.repositories.Create(ctx, "admin", repository.Input{
		Name: "发布计划执行测试仓库", Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/release-plan.git", DefaultBranch: "main", AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatalf("创建测试仓库失败: %v", err)
	}
	application, workflowID, sourceNodeID, workflowRevision := createReleasePlanExecutionHTTPApplication(
		t, fixture.pipelines, fixture.db, repositoryItem.ID,
	)
	plan, err := fixture.pipelines.CreateReleasePlan(ctx, "admin", pipeline.ReleasePlanInput{
		Description: "验证发布计划 HTTP 批量执行",
		Groups: []pipeline.ReleaseGroupInput{{
			Name: "默认发布组", Applications: []pipeline.ReleaseApplicationInput{{ApplicationID: application.ID}},
		}},
	})
	if err != nil {
		t.Fatalf("创建测试发布计划失败: %v", err)
	}
	if len(plan.Groups) != 1 || len(plan.Groups[0].Applications) != 1 {
		t.Fatalf("发布计划没有生成默认发布组: %+v", plan)
	}

	login := performJSONRequest(t, fixture.router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("管理员登录失败: status=%d body=%s", login.Code, login.Body.String())
	}
	response := performJSONRequest(t, fixture.router, http.MethodPost, "/api/v1/release-plans/"+plan.ID+"/executions", map[string]any{
		"request_id":               "release-plan-http-accepted",
		"expected_plan_updated_at": plan.UpdatedAt,
		"selections": []map[string]any{{
			"release_group_application_id": plan.Groups[0].Applications[0].ID,
			"workflow_id":                  workflowID,
			"expected_workflow_revision":   workflowRevision,
			"source_node_id":               sourceNodeID,
			"ref":                          "refs/heads/main",
			"commit_sha":                   commitSHA,
		}},
	}, login.Result().Cookies()[0])
	var payload struct {
		Execution struct {
			ID            string                           `json:"id"`
			ReleasePlanID string                           `json:"release_plan_id"`
			Status        model.ReleasePlanExecutionStatus `json:"status"`
			Items         []struct {
				ApplicationID string `json:"application_id"`
				PipelineRunID string `json:"pipeline_run_id"`
				SourceNodeID  string `json:"source_node_id"`
			} `json:"items"`
		} `json:"release_plan_execution"`
	}
	if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &payload) != nil {
		t.Fatalf("创建发布计划执行失败: status=%d body=%s", response.Code, response.Body.String())
	}
	if payload.Execution.ID == "" || payload.Execution.ReleasePlanID != plan.ID || payload.Execution.Status != model.ReleasePlanExecutionRunning ||
		len(payload.Execution.Items) != 1 || payload.Execution.Items[0].ApplicationID != application.ID ||
		payload.Execution.Items[0].PipelineRunID == "" || payload.Execution.Items[0].SourceNodeID != sourceNodeID {
		t.Fatalf("202 响应缺少完整的发布计划执行: %+v body=%s", payload.Execution, response.Body.String())
	}
	var run model.PipelineRun
	if err := fixture.db.First(&run, "id = ?", payload.Execution.Items[0].PipelineRunID).Error; err != nil {
		t.Fatalf("读取发布计划创建的流水线运行失败: %v", err)
	}
	if run.Status != model.PipelineRunRunning || run.CurrentNodeID != "build" || run.Stage != "queued" || run.ExecutionJobID == "" {
		t.Fatalf("发布计划没有先进入构建任务: %+v", run)
	}
	var job model.Job
	if err := fixture.db.First(&job, "id = ?", run.ExecutionJobID).Error; err != nil {
		t.Fatalf("读取发布计划构建任务失败: %v", err)
	}
	if job.Kind != "pipeline.build" || job.Subject != "edo.task.pipeline.build" {
		t.Fatalf("发布计划首个任务不是构建任务: %+v", job)
	}
}

func TestManualAndReleasePlanHTTPCanSelectPreviousArtifact(t *testing.T) {
	const commitSHA = "6789abcdef0123456789abcdef0123456789abcd"
	fixture := newReleasePlanExecutionHTTPFixture(t, commitSHA)
	defer fixture.close()

	ctx := context.Background()
	repositoryItem, _, err := fixture.repositories.Create(ctx, "admin", repository.Input{
		Name: "历史制品执行测试仓库", Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/artifact-release.git", DefaultBranch: "main", AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatalf("创建测试仓库失败: %v", err)
	}
	application, workflowID, sourceNodeID, workflowRevision := createReleasePlanExecutionHTTPApplication(
		t, fixture.pipelines, fixture.db, repositoryItem.ID,
	)
	artifact := createReleasePlanExecutionHTTPArtifact(t, fixture.db, application, commitSHA)
	login := performJSONRequest(t, fixture.router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("管理员登录失败: status=%d body=%s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]

	options := performJSONRequest(t, fixture.router, http.MethodGet,
		"/api/v1/applications/"+application.ID+"/workflows/"+workflowID+"/repository-refs", nil, cookie)
	if options.Code != http.StatusOK || !strings.Contains(options.Body.String(), artifact.ID) {
		t.Fatalf("手动执行选项没有返回历史制品: status=%d body=%s", options.Code, options.Body.String())
	}
	prepared := performJSONRequest(t, fixture.router, http.MethodPost,
		"/api/v1/applications/"+application.ID+"/workflows/"+workflowID+"/pipeline-runs", nil, cookie)
	var preparedPayload struct {
		PipelineRun struct {
			ID string `json:"id"`
		} `json:"pipeline_run"`
	}
	if prepared.Code != http.StatusCreated || json.Unmarshal(prepared.Body.Bytes(), &preparedPayload) != nil || preparedPayload.PipelineRun.ID == "" {
		t.Fatalf("准备手动流水线失败: status=%d body=%s", prepared.Code, prepared.Body.String())
	}
	executed := performJSONRequest(t, fixture.router, http.MethodPost,
		"/api/v1/pipeline-runs/"+preparedPayload.PipelineRun.ID+"/execute", map[string]any{
			"source_node_id": sourceNodeID,
			"artifact_id":    artifact.ID,
		}, cookie)
	var executedPayload struct {
		PipelineRun model.PipelineRun `json:"pipeline_run"`
	}
	if executed.Code != http.StatusOK || json.Unmarshal(executed.Body.Bytes(), &executedPayload) != nil {
		t.Fatalf("使用历史制品手动执行流水线失败: status=%d body=%s", executed.Code, executed.Body.String())
	}
	if executedPayload.PipelineRun.ArtifactID != artifact.ID || executedPayload.PipelineRun.CurrentNodeID != "approval" ||
		executedPayload.PipelineRun.Status != model.PipelineRunAwaitingApproval {
		t.Fatalf("手动执行没有跳过构建和构建后 Shell: %+v", executedPayload.PipelineRun)
	}

	plan, err := fixture.pipelines.CreateReleasePlan(ctx, "admin", pipeline.ReleasePlanInput{
		Description: "验证发布计划选择历史制品",
		Groups: []pipeline.ReleaseGroupInput{{
			Name: "默认发布组", Applications: []pipeline.ReleaseApplicationInput{{ApplicationID: application.ID}},
		}},
	})
	if err != nil {
		t.Fatalf("创建测试发布计划失败: %v", err)
	}
	release := performJSONRequest(t, fixture.router, http.MethodPost, "/api/v1/release-plans/"+plan.ID+"/executions", map[string]any{
		"request_id":               "release-plan-http-existing-artifact",
		"expected_plan_updated_at": plan.UpdatedAt,
		"selections": []map[string]any{{
			"release_group_application_id": plan.Groups[0].Applications[0].ID,
			"workflow_id":                  workflowID,
			"expected_workflow_revision":   workflowRevision,
			"source_node_id":               sourceNodeID,
			"artifact_id":                  artifact.ID,
		}},
	}, cookie)
	var releasePayload struct {
		Execution struct {
			Items []struct {
				PipelineRunID string `json:"pipeline_run_id"`
			} `json:"items"`
		} `json:"release_plan_execution"`
	}
	if release.Code != http.StatusAccepted || json.Unmarshal(release.Body.Bytes(), &releasePayload) != nil || len(releasePayload.Execution.Items) != 1 {
		t.Fatalf("发布计划使用历史制品失败: status=%d body=%s", release.Code, release.Body.String())
	}
	var releaseRun model.PipelineRun
	if err := fixture.db.First(&releaseRun, "id = ?", releasePayload.Execution.Items[0].PipelineRunID).Error; err != nil {
		t.Fatalf("读取发布计划流水线失败: %v", err)
	}
	if releaseRun.ArtifactID != artifact.ID || releaseRun.CurrentNodeID != "approval" || releaseRun.Status != model.PipelineRunAwaitingApproval {
		t.Fatalf("发布计划没有使用历史制品跳过构建和构建后 Shell: %+v", releaseRun)
	}
	located := performJSONRequest(t, fixture.router, http.MethodGet,
		"/api/v1/pipeline-runs/"+releasePayload.Execution.Items[0].PipelineRunID, nil, cookie)
	var locatedPayload struct {
		PipelineRun model.PipelineRun `json:"pipeline_run"`
	}
	if located.Code != http.StatusOK || json.Unmarshal(located.Body.Bytes(), &locatedPayload) != nil ||
		locatedPayload.PipelineRun.ID != releaseRun.ID || locatedPayload.PipelineRun.ReleasePlanID != plan.ID ||
		locatedPayload.PipelineRun.ApplicationID != application.ID {
		t.Fatalf("无法通过发布计划执行项定位准确流水线运行: status=%d body=%s", located.Code, located.Body.String())
	}
}

func TestRetryRunHTTPCanSelectOriginalRunArtifactWithoutRefInput(t *testing.T) {
	const commitSHA = "789abcdef0123456789abcdef0123456789abcde"
	fixture := newReleasePlanExecutionHTTPFixture(t, commitSHA)
	defer fixture.close()

	ctx := context.Background()
	repositoryItem, _, err := fixture.repositories.Create(ctx, "admin", repository.Input{
		Name: "重试固定版本测试仓库", Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/retry-artifact.git", DefaultBranch: "main", AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatalf("创建测试仓库失败: %v", err)
	}
	application, workflowID, _, workflowRevision := createReleasePlanExecutionHTTPApplication(
		t, fixture.pipelines, fixture.db, repositoryItem.ID,
	)
	now := time.Now().UTC()
	failed := model.PipelineRun{
		ID: uuid.NewString(), ApplicationID: application.ID, WorkflowID: workflowID, WorkflowRevision: workflowRevision,
		Trigger: "manual", Ref: "refs/heads/main", CommitSHA: commitSHA, CommitMessage: "固定版本 HTTP 重试",
		Status: model.PipelineRunFailed, Stage: "deploy_failed", Message: "部署失败",
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.db.Create(&failed).Error; err != nil {
		t.Fatalf("创建失败运行失败: %v", err)
	}
	artifact := createReleasePlanExecutionHTTPArtifact(t, fixture.db, application, commitSHA, failed.ID)
	if err := fixture.db.Model(&model.PipelineRun{}).Where("id = ?", failed.ID).Update("artifact_id", artifact.ID).Error; err != nil {
		t.Fatalf("关联失败运行制品失败: %v", err)
	}
	login := performJSONRequest(t, fixture.router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("管理员登录失败: status=%d body=%s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	options := performJSONRequest(t, fixture.router, http.MethodGet, "/api/v1/pipeline-runs/"+failed.ID+"/retry-options", nil, cookie)
	if options.Code != http.StatusOK || !strings.Contains(options.Body.String(), artifact.ID) ||
		!strings.Contains(options.Body.String(), commitSHA) || strings.Contains(options.Body.String(), `"branches"`) {
		t.Fatalf("重试选项没有只返回固定版本和原运行制品: status=%d body=%s", options.Code, options.Body.String())
	}
	retried := performJSONRequest(t, fixture.router, http.MethodPost, "/api/v1/pipeline-runs/"+failed.ID+"/retry", map[string]any{
		"artifact_id": artifact.ID,
	}, cookie)
	var payload struct {
		PipelineRun model.PipelineRun `json:"pipeline_run"`
	}
	if retried.Code != http.StatusCreated || json.Unmarshal(retried.Body.Bytes(), &payload) != nil {
		t.Fatalf("使用原运行制品重试失败: status=%d body=%s", retried.Code, retried.Body.String())
	}
	if payload.PipelineRun.RetryOfID != failed.ID || payload.PipelineRun.Ref != failed.Ref ||
		payload.PipelineRun.CommitSHA != failed.CommitSHA || payload.PipelineRun.ArtifactID != artifact.ID ||
		payload.PipelineRun.CurrentNodeID != "approval" || payload.PipelineRun.Status != model.PipelineRunAwaitingApproval {
		t.Fatalf("HTTP 重试没有固定版本或跳过构建: %+v", payload.PipelineRun)
	}
}

type releasePlanExecutionRefLister struct {
	commitSHA string
}

func (l releasePlanExecutionRefLister) ListRefs(context.Context, model.GitRepository, string) (repository.RefResult, error) {
	return repository.RefResult{Branches: []repository.GitRef{{Name: "main", SHA: l.commitSHA}}}, nil
}

type releasePlanExecutionHTTPFixture struct {
	router       *gin.Engine
	db           *gorm.DB
	repositories *repository.Service
	pipelines    *pipeline.Service
	close        func()
}

func newReleasePlanExecutionHTTPFixture(t *testing.T, commitSHA string) releasePlanExecutionHTTPFixture {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:" + uuid.NewString() + "?mode=memory&cache=shared", MaxOpenConns: 1,
		MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	accounts := account.NewService(db)
	accessService, err := access.NewService(db)
	if err != nil {
		t.Fatalf("初始化 Casbin 权限服务失败: %v", err)
	}
	auditService := audit.NewService(db)
	secretManager, err := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatalf("初始化测试密钥管理器失败: %v", err)
	}
	credentialService := credential.NewService(db, secretManager)
	configurationService := configuration.NewService(db, secretManager)
	repositoryService := repository.NewService(
		db, secretManager, credentialService, releasePlanExecutionRefLister{commitSHA: commitSHA}, 4,
		repository.WithWebhookGate(configurationService),
	)
	pipelineService := pipeline.NewService(db, repositoryService, secretManager)
	dockerService := dockerengine.NewService(db, secretManager, config.Runtime{})
	pipelineService.ConfigureExecution(
		dockerService,
		deployment.NewService(db, dockerService, nil, nil, nil, "", logger),
		logger,
	)
	if _, err := accounts.CreateAdmin(context.Background(), "admin", "管理员", "correct horse battery staple"); err != nil {
		t.Fatalf("创建测试管理员失败: %v", err)
	}
	server := miniredis.RunT(t)
	redisClient, err := cache.Open(context.Background(), config.Redis{
		URL: "redis://" + server.Addr() + "/0", KeyPrefix: "edo:", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("打开测试 Redis 失败: %v", err)
	}
	authConfig := config.Auth{
		SessionTTL: time.Hour, CookieName: "edo_session", LoginMaxFailure: 3, LoginWindow: time.Minute,
	}
	sessions := auth.NewSessionStore(redisClient, authConfig.SessionTTL)
	limiter := auth.NewLoginRateLimiter(redisClient, authConfig.LoginMaxFailure, authConfig.LoginWindow, configurationService)
	login, err := account.NewLoginService(accounts, sessions, limiter, logger)
	if err != nil {
		t.Fatalf("初始化登录服务失败: %v", err)
	}
	sqlDB, _ := db.DB()
	_, runtimeLogs := logging.NewRuntime("info")
	router := NewRouter(Dependencies{
		Environment: "test", Database: sqlDB, Redis: healthyDependency{}, NATS: healthyDependency{},
		Logger: logger, RuntimeLogs: runtimeLogs, Version: "test", AuthConfig: authConfig,
		Accounts: accounts, Login: login, LoginLimiter: limiter, Sessions: sessions,
		Access: accessService, Audits: auditService,
		Credentials: credentialService, Repositories: repositoryService, Pipelines: pipelineService,
		Configurations: configurationService,
	})
	return releasePlanExecutionHTTPFixture{
		router: router, db: db, repositories: repositoryService, pipelines: pipelineService,
		close: func() {
			_ = redisClient.Close()
			_ = database.Close(db)
		},
	}
}

func createReleasePlanExecutionHTTPApplication(
	t *testing.T,
	service *pipeline.Service,
	db *gorm.DB,
	repositoryID string,
) (*model.Application, string, string, uint64) {
	t.Helper()
	ctx := context.Background()
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", pipeline.BuildPlanInput{
		Name: "发布计划 HTTP 构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile", ContextPath: ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	environment := model.Environment{ID: uuid.NewString(), Name: "发布计划 HTTP 环境", IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	host := model.Host{ID: uuid.NewString(), Name: "发布计划 HTTP 主机", Mode: model.HostModeSSH, Architecture: model.HostArchitectureAMD64, SSHPort: 22, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	endpoint := model.DockerEndpoint{
		ID: uuid.NewString(), Name: "发布计划 HTTP Docker", HostID: host.ID, Host: "unix:///var/run/docker.sock",
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	resources := []any{
		&environment, &host, &endpoint,
		&model.EnvironmentHost{EnvironmentID: environment.ID, HostID: host.ID, CreatedAt: now},
		&model.HostCapability{HostID: host.ID, Kind: model.HostCapabilityDocker, RuntimeID: endpoint.ID, Status: model.HostCapabilityReady, CreatedAt: now, UpdatedAt: now},
	}
	for _, resource := range resources {
		if err := db.Create(resource).Error; err != nil {
			t.Fatal(err)
		}
	}
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", pipeline.DeploymentPlanInput{
		Name: "发布计划 HTTP 部署", Kind: model.DeploymentPlanDocker, ServiceName: "api",
		DeploymentTarget: &deployment.TargetInput{
			Name: "发布计划 HTTP 部署位置", Platform: model.DeploymentDocker,
			EnvironmentID: environment.ID, HostID: host.ID,
			RuntimeID: endpoint.ID, WorkloadName: "api", RolloutTimeout: 300,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(ctx, "admin", pipeline.ApplicationInput{
		Name: "release_plan_http_app", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflowResult, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	workflow := workflowResult.Workflow
	triggerID := "source"
	workflow.SchemaVersion = model.WorkflowSchemaVersion
	workflow.Source = model.WorkflowNode{
		ID: triggerID, Type: model.WorkflowNodeTrigger, Name: "代码源",
		Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"manual", "push"}},
	}
	workflow.Stages = []model.WorkflowStage{
		{
			ID: "build-stage", Name: "构建",
			Tasks: []model.WorkflowNode{
				{ID: "build", Type: model.WorkflowNodeBuild, Name: "构建镜像", Config: model.WorkflowNodeConfig{BuildPlanID: buildPlan.ID}},
				{
					ID: "shell", Type: model.WorkflowNodeShell, Name: "冒烟检查",
					Config: model.WorkflowNodeConfig{Script: "echo ready", WorkingDirectory: ".", TimeoutSeconds: 60},
				},
			},
		},
		{
			ID: "release-stage", Name: "发布",
			Tasks: []model.WorkflowNode{
				{ID: "approval", Type: model.WorkflowNodeApproval, Name: "发布审核"},
				{ID: "manual", Type: model.WorkflowNodeManual, Name: "人工放行"},
				{
					ID: "deploy", Type: model.WorkflowNodeDeploy, Name: "部署",
					Config: model.WorkflowNodeConfig{DeploymentPlanID: deploymentPlan.ID},
				},
			},
		},
	}
	saved, err := service.SaveWorkflow(ctx, application.ID, "admin", pipeline.WorkflowInput{
		SchemaVersion: workflow.SchemaVersion, Name: workflow.Name, Revision: workflow.Revision, Activate: true,
		Source: workflow.Source, Stages: workflow.Stages,
	})
	if err != nil {
		t.Fatalf("启用测试流水线失败: %v", err)
	}
	return application, saved.Workflow.ID, triggerID, saved.Workflow.Revision
}

func createReleasePlanExecutionHTTPArtifact(t *testing.T, db *gorm.DB, application *model.Application, commitSHA string, pipelineRunIDs ...string) *model.Artifact {
	t.Helper()
	var buildPlan model.BuildPlan
	if err := db.First(&buildPlan, "name = ?", "发布计划 HTTP 构建").Error; err != nil {
		t.Fatalf("读取历史制品构建方案失败: %v", err)
	}
	now := time.Now().UTC()
	finishedAt := now
	pipelineRunID := uuid.NewString()
	if len(pipelineRunIDs) > 0 && strings.TrimSpace(pipelineRunIDs[0]) != "" {
		pipelineRunID = strings.TrimSpace(pipelineRunIDs[0])
	}
	build := model.BuildRun{
		ID: uuid.NewString(), ApplicationID: application.ID, PipelineRunID: pipelineRunID,
		RepositoryID: application.RepositoryID, WorkflowNodeID: "build", BuildPlanID: buildPlan.ID,
		ProducerKind: model.BuildRunProducerDockerfile, Ref: "refs/heads/main", CommitSHA: commitSHA,
		PlanSnapshot: "{}", Status: model.BuildRunStatusSucceeded, CreatedBy: "admin",
		CreatedAt: now, UpdatedAt: now, FinishedAt: &finishedAt,
	}
	digest := "sha256:" + strings.Repeat("b", 64)
	artifact := &model.Artifact{
		ID: uuid.NewString(), ApplicationID: application.ID, BuildRunID: build.ID, PipelineRunID: build.PipelineRunID,
		Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable,
		Name: "edo.local/" + application.Name + ":previous", Digest: digest,
		StorageKind: model.ArtifactStorageKindDockerDaemon, ImageRef: "edo.local/" + application.Name + ":previous",
		RuntimeID: "local", LocalImageID: digest, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&build).Error; err != nil {
		t.Fatalf("创建历史构建记录失败: %v", err)
	}
	if err := db.Create(artifact).Error; err != nil {
		t.Fatalf("创建历史制品失败: %v", err)
	}
	return artifact
}
