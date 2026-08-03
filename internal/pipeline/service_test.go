package pipeline

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/regclient/regclient"
	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/credential"
	"edo/internal/database"
	"edo/internal/deployment"
	"edo/internal/dockerengine"
	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/repository"
	"edo/internal/secret"
)

func TestApplicationOwnsIndependentWorkflows(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "multiple_workflows", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(application.Workflows) != 1 {
		t.Fatalf("新应用应带有一条可编辑草稿: %+v", application.Workflows)
	}
	first := application.Workflows[0]
	created, err := service.CreateApplicationWorkflow(ctx, application.ID, "admin", WorkflowCreateInput{Name: "生产发布"})
	if err != nil {
		t.Fatalf("新增第二条流水线失败: %v", err)
	}
	if created.Workflow.ID == first.ID {
		t.Fatal("新增流水线覆盖了原流水线")
	}
	hydrated, err := service.FindApplication(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hydrated.Workflow != nil {
		t.Fatal("多流水线应用不得隐式选择第一条流水线")
	}
	if _, err := service.GetWorkflow(ctx, application.ID); !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("缺少 workflow_id 的旧调用必须拒绝多流水线应用: %v", err)
	}
	saved, err := service.SaveApplicationWorkflow(ctx, application.ID, created.Workflow.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: "生产发布-自定义",
		Revision: created.Workflow.Revision, Source: created.Workflow.Source, Stages: created.Workflow.Stages,
	})
	if err != nil {
		t.Fatalf("修改第二条流水线失败: %v", err)
	}
	workflows, err := service.ListApplicationWorkflows(ctx, application.ID)
	if err != nil || len(workflows) != 2 {
		t.Fatalf("应用没有同时返回两条流水线: workflows=%+v err=%v", workflows, err)
	}
	if workflows[0].ID != first.ID || workflows[0].Revision != first.Revision || workflows[1].ID != saved.Workflow.ID {
		t.Fatalf("修改第二条流水线影响了第一条: %+v", workflows)
	}
	if err := service.DeleteApplicationWorkflow(ctx, application.ID, saved.Workflow.ID); err != nil {
		t.Fatalf("删除第二条流水线失败: %v", err)
	}
	workflows, err = service.ListApplicationWorkflows(ctx, application.ID)
	if err != nil || len(workflows) != 1 || workflows[0].ID != first.ID {
		t.Fatalf("删除单条流水线影响了应用内其他流水线: workflows=%+v err=%v", workflows, err)
	}
}

func TestApplicationWorkflowDefaultNamesRemainUnique(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "default_named_app", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateApplicationWorkflow(ctx, application.ID, "admin", WorkflowCreateInput{})
	if err != nil {
		t.Fatalf("第二条流水线应自动生成不重复的名称: %v", err)
	}
	third, err := service.CreateApplicationWorkflow(ctx, application.ID, "admin", WorkflowCreateInput{})
	if err != nil {
		t.Fatalf("第三条流水线应自动生成不重复的名称: %v", err)
	}
	if second.Workflow.Name != "default_named_app流水线 2" || third.Workflow.Name != "default_named_app流水线 3" {
		t.Fatalf("自动名称不符合预期: second=%q third=%q", second.Workflow.Name, third.Workflow.Name)
	}
}

func TestApplicationWorkflowPresetsCreateEditableDrafts(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "template_app", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		key          string
		name         string
		runtimeImage string
		scriptPart   string
	}{
		{key: workflowPresetGo, name: "Go 流水线", runtimeImage: "golang:1.26-alpine", scriptPart: "go test ./..."},
		{key: workflowPresetNodeJS, name: "Node.js 流水线", runtimeImage: "node:24-alpine", scriptPart: "npm ci"},
		{key: workflowPresetPython, name: "Python 流水线", runtimeImage: "python:3.14-alpine", scriptPart: "python -m pytest"},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			created, err := service.CreateApplicationWorkflow(ctx, application.ID, "admin", WorkflowCreateInput{PresetKey: test.key})
			if err != nil {
				t.Fatalf("从 %s 模板创建流水线失败: %v", test.key, err)
			}
			workflow := created.Workflow
			if workflow.Name != test.name || workflow.IsActive {
				t.Fatalf("模板应创建未启用草稿: name=%q active=%t", workflow.Name, workflow.IsActive)
			}
			if len(workflow.Stages) != 3 || len(workflow.Stages[0].Tasks) != 1 {
				t.Fatalf("模板阶段结构不完整: %+v", workflow.Stages)
			}
			testTask := workflow.Stages[0].Tasks[0]
			if testTask.Type != model.WorkflowNodeShell || testTask.Config.RuntimeImage != test.runtimeImage ||
				!strings.Contains(testTask.Config.Script, test.scriptPart) {
				t.Fatalf("语言测试任务不符合模板: %+v", testTask)
			}
			buildTask := workflow.Stages[1].Tasks[0]
			deployTask := workflow.Stages[2].Tasks[0]
			if buildTask.Type != model.WorkflowNodeBuild || buildTask.Config.BuildPlanID != "" ||
				deployTask.Type != model.WorkflowNodeDeploy || deployTask.Config.DeploymentPlanID != "" {
				t.Fatalf("模板不得绑定具体构建或部署方案: build=%+v deploy=%+v", buildTask, deployTask)
			}
			if created.Valid || !hasWorkflowIssue(created.Issues, "missing_build_plan") || !hasWorkflowIssue(created.Issues, "missing_deployment_plan") {
				t.Fatalf("未选择方案的模板草稿应提示补充配置: valid=%t issues=%+v", created.Valid, created.Issues)
			}
		})
	}

	blank, err := service.CreateApplicationWorkflow(ctx, application.ID, "admin", WorkflowCreateInput{PresetKey: workflowPresetBlank})
	if err != nil {
		t.Fatalf("创建空白流水线失败: %v", err)
	}
	if blank.Workflow.Name != "空白流水线" || len(blank.Workflow.Stages) != 0 {
		t.Fatalf("空白模板不应预设阶段: %+v", blank.Workflow)
	}

	before, err := service.ListApplicationWorkflows(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateApplicationWorkflow(ctx, application.ID, "admin", WorkflowCreateInput{PresetKey: "java"}); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("未知模板应被拒绝: %v", err)
	}
	after, err := service.ListApplicationWorkflows(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("未知模板不得留下流水线: before=%d after=%d", len(before), len(after))
	}
}

func TestResourcesCanBeConfiguredAndPipelinePrepared(t *testing.T) {
	service, db, secretManager, repositoryID := newPipelineTestService(t)
	ctx := context.Background()

	credential := "registry-password"
	registry, err := service.CreateRegistry(ctx, "admin", RegistryInput{
		Name: "团队镜像仓库", Provider: model.RegistryHarbor,
		Endpoint: "https://harbor.example.com", Namespace: "edo", Username: "robot",
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
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "镜像构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile", ImageRegistryID: registry.ID,
	})
	if err != nil {
		t.Fatalf("创建构建方案失败: %v", err)
	}
	target := model.DeploymentTarget{
		ID: "target-1", Name: "测试环境", Platform: model.DeploymentKubernetes,
		RuntimeID: "cluster-1", Namespace: "default",
		WorkloadName: "edo-api", ContainerName: "api", RolloutTimeout: 300,
		IsActive: true, CreatedBy: "admin", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "Kubernetes 发布", Kind: model.DeploymentPlanKubernetes,
		DeploymentTarget: deploymentPlanTargetInput(t, service, target),
	})
	if err != nil {
		t.Fatalf("创建部署方案失败: %v", err)
	}

	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "order_service", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatalf("创建应用失败: %v", err)
	}
	if application.Repository.ID != repositoryID || len(application.Repositories) != 1 {
		t.Fatalf("应用不应绑定流水线任务资源: %+v", application)
	}
	workflowResult, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatalf("读取应用流水线失败: %v", err)
	}
	source, stages := testStageWorkflowGraph(buildPlan.ID, deploymentPlan.ID)
	workflowResult, err = service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflowResult.Workflow.Name,
		Revision: workflowResult.Workflow.Revision, Activate: true, Source: source, Stages: stages,
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
	if len(blocked.Repositories) != 1 || blocked.Repositories[0].BuildPlanID != "" ||
		blocked.Repositories[0].DeploymentPlanID != "" {
		t.Fatalf("代码仓库快照不应复制流水线任务方案: %+v", blocked.Repositories)
	}
	if err := db.Model(&model.ApplicationRepository{}).Where("application_id = ?", application.ID).Updates(map[string]any{
		"last_observed_ref": "refs/heads/main", "last_observed_commit": "0123456789012345678901234567890123456789",
	}).Error; err != nil {
		t.Fatalf("设置测试代码版本失败: %v", err)
	}
	ready, err := service.PrepareRun(ctx, application.ID, "admin")
	if err != nil || ready.Status != model.PipelineRunBlocked || ready.Ref != "" || ready.CommitSHA != "" {
		t.Fatalf("手动执行不应静默复用后台最近观察到的代码版本: run=%+v err=%v", ready, err)
	}
}

func TestWorkflowDraftRejectsDeploymentPlanTargetMismatch(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "错配测试构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile",
	})
	if err != nil {
		t.Fatal(err)
	}
	sshTarget := model.DeploymentTarget{
		ID: "workflow-ssh-target", Name: "SSH 流水线目标", Platform: model.DeploymentSSH,
		EnvironmentID: "workflow-environment", HostID: "workflow-host", WorkingDirectory: "/srv/app",
		RolloutTimeout: 120,
	}
	plan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "SSH 流水线部署", Kind: model.DeploymentPlanScript, Script: "echo deploy\n", TimeoutSeconds: 120,
		DeploymentTarget: deploymentPlanTargetInput(t, service, sshTarget),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	dockerTarget := model.DeploymentTarget{
		ID: "workflow-docker-target", Name: "Docker 目标", Platform: model.DeploymentDocker,
		RuntimeID: "docker-1", WorkloadName: "api",
		RolloutTimeout: 120, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&dockerTarget).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.DeploymentPlan{}).Where("id = ?", plan.ID).
		Update("deployment_target_id", dockerTarget.ID).Error; err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "ssh_pipeline_app", RepositoryID: repositoryID,
		PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, stages := testStageWorkflowGraph(buildPlan.ID, plan.ID)
	result, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name,
		Revision: workflow.Workflow.Revision, Activate: false, Source: source, Stages: stages,
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
		{model.DeploymentPlanKubernetes, model.DeploymentKubernetes, true},
		{model.DeploymentPlanKubernetes, model.DeploymentDocker, false},
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
	targetInput := deploymentPlanTargetInput(t, service, model.DeploymentTarget{
		Name: "保留字节部署目标", Platform: model.DeploymentSSH,
		EnvironmentID: "script-bytes-environment", HostID: "script-bytes-host", WorkingDirectory: "/srv/app",
		RolloutTimeout: 120,
	})
	original := "\n  printf 'deploy'  \n\n"
	plan, err := service.CreateDeploymentPlan(context.Background(), "admin", DeploymentPlanInput{
		Name: "保留脚本字节", Kind: model.DeploymentPlanScript, Script: original, TimeoutSeconds: 120,
		DeploymentTarget: targetInput,
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
		DeploymentTarget: targetInput,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Script != updatedScript {
		t.Fatalf("更新部署方案时修改了脚本原始字节: got=%q want=%q", updated.Script, updatedScript)
	}
}

func TestPipelineIncompleteMessageListsEveryMissingItem(t *testing.T) {
	application := &model.Application{}
	message := pipelineIncompleteMessage(application)
	if message != "缺少：代码仓库、已启用的流水线、代码版本" {
		t.Fatalf("阻塞原因没有列出全部缺失项: %q", message)
	}
}

func TestCreateRegistryAcceptsRepositoryStyleDisplayName(t *testing.T) {
	service, _, secretManager, _ := newPipelineTestService(t)
	credential := " ucloud-registry-token "
	registry, err := service.CreateRegistry(context.Background(), "admin", RegistryInput{
		Name:       "uhub.service.ucloud.cn/edo-application",
		Provider:   model.RegistryGeneric,
		Endpoint:   "https://uhub.service.ucloud.cn",
		Namespace:  "edo-application",
		Username:   "852519822@qq.com",
		Credential: &credential,
	})
	if err != nil {
		t.Fatalf("合法的 UCloud 镜像仓库配置不应被拒绝: %v", err)
	}
	if registry.Name != "uhub.service.ucloud.cn/edo-application" ||
		registry.Endpoint != "https://uhub.service.ucloud.cn" || registry.Namespace != "edo-application" {
		t.Fatalf("镜像仓库配置保存错误: %+v", registry)
	}
	plaintext, err := secretManager.Decrypt(registry.CredentialCiphertext, []byte("image_registry:"+registry.Name+":credential"))
	if err != nil || plaintext != credential {
		t.Fatalf("镜像仓库凭据不应被静默修改: plaintext=%q err=%v", plaintext, err)
	}
}

func TestDockerHubRegistryUsesFixedEndpoint(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	credential := "docker-hub-token"
	registry, err := service.CreateRegistry(context.Background(), "admin", RegistryInput{
		Name: "Docker Hub", Provider: model.RegistryDockerHub, Namespace: "edo-team",
		Username: "edo-team", Credential: &credential, AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatalf("未填写地址的 Docker Hub 配置被拒绝: %v", err)
	}
	if registry.Endpoint != model.DockerHubEndpoint || registry.AllowInsecureHTTP {
		t.Fatalf("Docker Hub 没有使用固定安全地址: %+v", registry)
	}
	auth, err := service.registryAuth(*registry)
	if err != nil {
		t.Fatalf("读取 Docker Hub 认证失败: %v", err)
	}
	if auth.Host != "docker.io" || auth.ServerAddress != regclient.DockerRegistryAuth {
		t.Fatalf("Docker Hub 镜像主机或认证键错误: %+v", auth)
	}
}

func TestDockerHubRegistryRejectsThirdPartyEndpoint(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	_, err := service.CreateRegistry(context.Background(), "admin", RegistryInput{
		Name: "错误类型", Provider: model.RegistryDockerHub,
		Endpoint: "https://registry.cn-shenzhen.aliyuncs.com", Namespace: "edo-team",
	})
	if !errors.Is(err, ErrRegistryProviderEndpoint) {
		t.Fatalf("Docker Hub 类型接受了第三方仓库地址: %v", err)
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
	const username = "robot$edo"
	const password = "registry-token"
	registryServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/" {
			http.NotFound(response, request)
			return
		}
		actualUser, actualPassword, ok := request.BasicAuth()
		if !ok || actualUser != username || actualPassword != password {
			response.Header().Set("WWW-Authenticate", `Basic realm="EDO test registry"`)
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
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "Webhook 构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "webhook-target", Name: "Webhook 部署目标", Platform: model.DeploymentDocker,
		RuntimeID: "local", WorkloadName: "payment", RolloutTimeout: 120,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "Webhook 部署", Kind: model.DeploymentPlanDocker, ServiceName: "payment",
		TimeoutSeconds: 120, DeploymentTarget: deploymentPlanTargetInput(t, service, target),
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "payment_service", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatalf("创建应用失败: %v", err)
	}
	workflow, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, stages := testStageWorkflowGraph(buildPlan.ID, deploymentPlan.ID)
	source.Config.Events = []string{"manual", "push", "pr", "tag"}
	source.Config.TagPattern = "v*"
	if _, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name,
		Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: stages,
	}); err != nil {
		t.Fatalf("启用 Webhook 阶段式流水线失败: %v", err)
	}

	events := []repository.WebhookTaskPayload{
		{RepositoryID: repositoryID, EventType: "branch_push", Ref: "refs/heads/feature", CommitSHA: "skip-feature"},
		{RepositoryID: repositoryID, EventType: "branch_push", Ref: "refs/heads/main", CommitSHA: "commit-main", Message: "修复支付回调\n\n补充异常分支测试"},
		{RepositoryID: repositoryID, EventType: "branch_push", Ref: "refs/heads/main", CommitSHA: "commit-main"},
		{RepositoryID: repositoryID, EventType: "tag_push", Ref: "refs/tags/nightly", CommitSHA: "skip-tag"},
		{RepositoryID: repositoryID, EventType: "tag_push", Ref: "refs/tags/v1.0.0", CommitSHA: "commit-tag"},
		{
			RepositoryID: repositoryID, EventType: "pull_request", Ref: "refs/pull/17/head", CommitSHA: "commit-pr",
			SourceBranch: "feature/payment", TargetBranch: "main", Action: "synchronize",
		},
		{
			RepositoryID: repositoryID, EventType: "pull_request", Ref: "refs/pull/18/head", CommitSHA: "commit-merge",
			SourceBranch: "feature/refund", TargetBranch: "main", Action: "merged",
		},
		// 同一次合并通常还会发送目标分支 Push；两种事件只允许创建一条运行。
		{RepositoryID: repositoryID, EventType: "branch_push", Ref: "refs/heads/main", CommitSHA: "commit-merge"},
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
	if count != 4 {
		t.Fatalf("触发规则或合并事件去重生成了错误的流水线记录数: got=%d want=4", count)
	}
	var pullRequestRun model.PipelineRun
	if err := db.First(&pullRequestRun, "application_id = ? AND commit_sha = ?", application.ID, "commit-pr").Error; err != nil ||
		pullRequestRun.Ref != "refs/pull/17/head" || pullRequestRun.TriggerAction != "updated" ||
		pullRequestRun.SourceBranch != "feature/payment" || pullRequestRun.TargetBranch != "main" {
		t.Fatalf("PR 规则应按目标分支匹配并保留可检出的公开 Ref: run=%+v err=%v", pullRequestRun, err)
	}
	var mergeRuns int64
	if err := db.Model(&model.PipelineRun{}).
		Where("application_id = ? AND commit_sha = ?", application.ID, "commit-merge").
		Count(&mergeRuns).Error; err != nil || mergeRuns != 1 {
		t.Fatalf("PR 合并与目标分支 Push 未幂等去重: count=%d err=%v", mergeRuns, err)
	}
	var mainRun model.PipelineRun
	if err := db.First(&mainRun, "application_id = ? AND commit_sha = ?", application.ID, "commit-main").Error; err != nil || mainRun.CommitMessage != "修复支付回调" {
		t.Fatalf("Webhook 提交说明没有保存到流水线运行: run=%+v err=%v", mainRun, err)
	}
	var stored model.ApplicationRepository
	if err := db.First(&stored, "application_id = ?", application.ID).Error; err != nil {
		t.Fatalf("查询应用仓库关联失败: %v", err)
	}
	if stored.LastObservedCommit != "commit-merge" {
		t.Fatalf("应用仓库没有保存最后一次代码变化: %+v", stored)
	}
}

func TestRepositoryEventTriggersEveryMatchingApplicationWorkflow(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	application := createManualRunTestApplication(t, service, db, repositoryID, "multi_workflow_event")
	first := application.Workflow
	if first == nil {
		t.Fatal("新应用缺少默认流水线")
	}
	firstSource, firstStages := first.Source, cloneWorkflowStages(first.Stages)
	firstSource.Config.Events = []string{"push"}
	if _, err := service.SaveApplicationWorkflow(ctx, application.ID, first.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: "主分支构建",
		Revision: first.Revision, Activate: true, Source: firstSource, Stages: firstStages,
	}); err != nil {
		t.Fatalf("启用第一条流水线失败: %v", err)
	}
	second, err := service.CreateApplicationWorkflow(ctx, application.ID, "admin", WorkflowCreateInput{Name: "主分支安全检查"})
	if err != nil {
		t.Fatalf("创建第二条流水线失败: %v", err)
	}
	secondSource := firstSource
	if _, err := service.SaveApplicationWorkflow(ctx, application.ID, second.Workflow.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: second.Workflow.Name,
		Revision: second.Workflow.Revision, Activate: true, Source: secondSource, Stages: cloneWorkflowStages(firstStages),
	}); err != nil {
		t.Fatalf("启用第二条流水线失败: %v", err)
	}

	if err := service.HandleRepositoryEvent(ctx, repository.WebhookTaskPayload{
		RepositoryID: repositoryID, EventType: "branch_push",
		Ref: "refs/heads/main", CommitSHA: "shared-workflow-commit",
	}); err != nil {
		t.Fatalf("同一事件触发多条流水线失败: %v", err)
	}
	var runs []model.PipelineRun
	if err := db.Where("application_id = ? AND commit_sha = ?", application.ID, "shared-workflow-commit").Find(&runs).Error; err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("同一应用的两条匹配流水线应各创建一次运行: %+v", runs)
	}
	workflowIDs, dedupKeys := map[string]bool{}, map[string]bool{}
	for i := range runs {
		workflowIDs[runs[i].WorkflowID] = true
		if runs[i].EventDedupKey != nil {
			dedupKeys[*runs[i].EventDedupKey] = true
		}
	}
	if len(workflowIDs) != 2 || len(dedupKeys) != 2 {
		t.Fatalf("运行和幂等键没有按流水线隔离: runs=%+v", runs)
	}
	var observations []model.ApplicationRepositoryObservation
	if err := db.Where("application_repository_id IN (?)",
		db.Model(&model.ApplicationRepository{}).Select("id").Where("application_id = ?", application.ID),
	).Find(&observations).Error; err != nil {
		t.Fatal(err)
	}
	if len(observations) != 2 || observations[0].WorkflowID == observations[1].WorkflowID {
		t.Fatalf("仓库监听游标没有按流水线隔离: %+v", observations)
	}
}

func TestApplicationRepositoryCheckInterval(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()

	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "default_poll_interval", RepositoryID: repositoryID,
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

func TestReleaseWorkflowCapturesExplicitApprovalNode(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	registry := createTestImageRegistry(t, service, "workflow-registry")
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "workflow-build", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile", ImageRegistryID: registry.ID,
	})
	if err != nil {
		t.Fatalf("创建构建方案失败: %v", err)
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "workflow-target", Name: "workflow-target", Platform: model.DeploymentKubernetes,
		RuntimeID: "cluster-1", Namespace: "default", WorkloadName: "edo-api", ContainerName: "api",
		RolloutTimeout: 300, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "workflow-kubernetes", Kind: model.DeploymentPlanKubernetes,
		DeploymentTarget: deploymentPlanTargetInput(t, service, target),
	})
	if err != nil {
		t.Fatalf("创建部署方案失败: %v", err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "staged_pipeline_app", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatalf("创建应用失败: %v", err)
	}
	workflowResult, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatalf("读取应用流水线失败: %v", err)
	}
	source, stages := testStageWorkflowGraph(buildPlan.ID, deploymentPlan.ID)
	workflowResult, err = service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflowResult.Workflow.Name,
		Revision: workflowResult.Workflow.Revision, Activate: true, Source: source, Stages: stages,
	})
	if err != nil || !workflowResult.Workflow.IsActive {
		t.Fatalf("启用阶段式流水线失败: result=%+v err=%v", workflowResult, err)
	}
	application, err = service.FindApplication(ctx, application.ID)
	if err != nil {
		t.Fatalf("重新读取应用失败: %v", err)
	}
	source = application.Workflow.Source
	run, err := service.newResolvedWorkflowRun(
		ctx, application, application.Workflow, source, "push", "refs/heads/main",
		"0123456789012345678901234567890123456789", "system", "检测到主分支更新", now,
	)
	if err != nil {
		t.Fatalf("创建代码事件流水线运行失败: %v", err)
	}
	components, err := pipelineRunRepositories(application, run.ID, run.Ref, run.CommitSHA, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		return tx.Create(&components).Error
	}); err != nil {
		t.Fatalf("保存代码事件流水线运行失败: %v", err)
	}
	if run.CurrentNodeID != "source" || !run.ApprovalRequired {
		t.Fatalf("流水线没有从代码源启动或没有记录显式审核节点: %+v", run)
	}
	listedRuns, err := service.ListRuns(ctx, 10)
	if err != nil || len(listedRuns) == 0 || !listedRuns[0].ApprovalRequired {
		t.Fatalf("流水线运行列表没有保留显式审核要求: runs=%+v err=%v", listedRuns, err)
	}
	if listedRuns[0].ExecutionGraph == nil || listedRuns[0].ExecutionGraph.Source.ID != source.ID ||
		len(listedRuns[0].ExecutionGraph.Stages) != len(stages) {
		t.Fatalf("流水线运行列表没有返回只读执行拓扑: graph=%+v", listedRuns[0].ExecutionGraph)
	}
	foundRun, err := service.FindRun(ctx, run.ID)
	if err != nil || foundRun.ID != run.ID || !foundRun.ApprovalRequired || foundRun.ExecutionGraph == nil {
		t.Fatalf("无法按标识定位准确的流水线运行: run=%+v err=%v", foundRun, err)
	}
	if _, err := service.FindRun(ctx, "missing-pipeline-run"); !errors.Is(err, ErrPipelineRunNotFound) {
		t.Fatalf("不存在的流水线运行没有返回稳定错误: %v", err)
	}
}

func TestPublicWorkflowTemplateSyncsLinkedApplications(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	registry := createTestImageRegistry(t, service, "template-registry")
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "公共模板构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile", ImageRegistryID: registry.ID,
	})
	if err != nil {
		t.Fatalf("创建构建方案失败: %v", err)
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "template-target", Name: "模板部署目标", Platform: model.DeploymentKubernetes,
		RuntimeID: "cluster-1", Namespace: "default", WorkloadName: "edo-api", ContainerName: "api",
		RolloutTimeout: 300, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "公共模板 Kubernetes", Kind: model.DeploymentPlanKubernetes,
		DeploymentTarget: deploymentPlanTargetInput(t, service, target),
	})
	if err != nil {
		t.Fatalf("创建部署方案失败: %v", err)
	}
	source, stages := testStageWorkflowGraph(buildPlan.ID, deploymentPlan.ID)
	templateResult, err := service.CreateWorkflowTemplate(ctx, "admin", WorkflowTemplateInput{
		Description: "构建并发布",
		WorkflowInput: WorkflowInput{
			SchemaVersion: model.WorkflowSchemaVersion, Name: "持续交付", Activate: true,
			Source: source, Stages: stages,
		},
	})
	if err != nil || !templateResult.Valid || !templateResult.WorkflowTemplate.IsActive {
		t.Fatalf("公共流水线方案未能启用: result=%+v err=%v", templateResult, err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "public_pipeline_app", RepositoryID: repositoryID, PollIntervalSeconds: 60,
		WorkflowTemplateID: templateResult.WorkflowTemplate.ID,
	})
	if err != nil {
		t.Fatalf("使用公共流水线方案创建应用失败: %v", err)
	}
	if application.WorkflowTemplate == nil || application.Workflow == nil || !application.Workflow.IsActive ||
		application.Workflow.WorkflowTemplateRevision != 1 {
		t.Fatalf("应用没有完整关联阶段式流水线方案: %+v", application)
	}
	if application.Workflow.Name != templateResult.WorkflowTemplate.Name {
		t.Fatalf("关联流水线名称不应拼接应用名或方案类型: got=%q want=%q", application.Workflow.Name, templateResult.WorkflowTemplate.Name)
	}
	if application.RepositoryID != repositoryID {
		t.Fatalf("通用流水线方案不应改变应用代码仓库: got=%s want=%s", application.RepositoryID, repositoryID)
	}

	updatedSource, updatedStages := source, cloneWorkflowStages(stages)
	updatedSource.Name = "修改后的代码源"
	if _, err := service.SaveWorkflowTemplate(ctx, templateResult.WorkflowTemplate.ID, "admin", WorkflowTemplateInput{
		Description: "模板第二版",
		WorkflowInput: WorkflowInput{
			SchemaVersion: model.WorkflowSchemaVersion, Name: "持续交付新版", Revision: 1, Activate: true,
			Source: updatedSource, Stages: updatedStages,
		},
	}); err != nil {
		t.Fatalf("更新公共流水线方案失败: %v", err)
	}
	stored, err := service.GetWorkflow(ctx, application.ID)
	if err != nil || stored.Workflow.Name != "持续交付新版" || stored.Workflow.Source.Name != "修改后的代码源" || stored.Workflow.WorkflowTemplateRevision != 2 {
		t.Fatalf("启用方案的新版本没有同步到关联应用: workflow=%+v err=%v", stored, err)
	}
	var repositoryBoundApplication model.Application
	if err := db.First(&repositoryBoundApplication, "id = ?", application.ID).Error; err != nil || repositoryBoundApplication.RepositoryID != repositoryID {
		t.Fatalf("方案版本同步不应改写应用代码仓库: application=%+v err=%v", repositoryBoundApplication, err)
	}
	updatedApplication, err := service.UpdateApplication(ctx, application.ID, ApplicationInput{
		Name: application.Name, Description: "只修改应用说明", RepositoryID: repositoryID,
		PollIntervalSeconds: 60, WorkflowTemplateID: templateResult.WorkflowTemplate.ID,
	})
	if err != nil || updatedApplication.Workflow == nil || !updatedApplication.Workflow.IsActive {
		t.Fatalf("修改应用说明不应停用关联流水线: application=%+v err=%v", updatedApplication, err)
	}
	stored, err = service.GetWorkflow(ctx, application.ID)
	if err != nil || stored.Workflow.Source.Name != "修改后的代码源" || stored.Workflow.WorkflowTemplateRevision != 2 {
		t.Fatalf("修改应用说明不应回退已经同步的流水线: workflow=%+v err=%v", stored, err)
	}
	if err := service.DeleteWorkflowTemplate(ctx, templateResult.WorkflowTemplate.ID); !errors.Is(err, ErrWorkflowTemplateInUse) {
		t.Fatalf("仍被应用使用的流水线方案不应允许删除: %v", err)
	}

	customSource, customStages := stored.Workflow.Source, cloneWorkflowStages(stored.Workflow.Stages)
	customSource.Name = "应用自定义代码源"
	custom, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: stored.Workflow.Name,
		Revision: stored.Workflow.Revision, Activate: true, Source: customSource, Stages: customStages,
	})
	if err != nil || custom.Workflow.WorkflowTemplateID != "" || custom.Workflow.WorkflowTemplateRevision != 0 {
		t.Fatalf("单独修改应用流水线后没有解除方案关联: workflow=%+v err=%v", custom, err)
	}
	var detached model.Application
	if err := db.First(&detached, "id = ?", application.ID).Error; err != nil || detached.WorkflowTemplateID != "" {
		t.Fatalf("应用仍然引用已经解除关联的流水线方案: application=%+v err=%v", detached, err)
	}

	thirdSource, thirdStages := updatedSource, cloneWorkflowStages(updatedStages)
	thirdSource.Name = "方案第三版代码源"
	if _, err := service.SaveWorkflowTemplate(ctx, templateResult.WorkflowTemplate.ID, "admin", WorkflowTemplateInput{
		Description: "模板第三版",
		WorkflowInput: WorkflowInput{
			SchemaVersion: model.WorkflowSchemaVersion, Name: "持续交付", Revision: 2, Activate: true,
			Source: thirdSource, Stages: thirdStages,
		},
	}); err != nil {
		t.Fatalf("更新解除关联后的流水线方案失败: %v", err)
	}
	stored, err = service.GetWorkflow(ctx, application.ID)
	if err != nil || stored.Workflow.Source.Name != "应用自定义代码源" {
		t.Fatalf("方案更新覆盖了应用的自定义流水线: workflow=%+v err=%v", stored, err)
	}
	if err := service.DeleteWorkflowTemplate(ctx, templateResult.WorkflowTemplate.ID); err != nil {
		t.Fatalf("解除全部应用关联后仍不能删除流水线方案: %v", err)
	}
	unused, err := service.CreateWorkflowTemplate(ctx, "admin", WorkflowTemplateInput{
		Description: "可删除的未使用方案",
		WorkflowInput: WorkflowInput{
			SchemaVersion: model.WorkflowSchemaVersion, Name: "未使用流水线方案",
			Source: source, Stages: stages,
		},
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

func testStageWorkflowGraph(buildPlanID, deploymentPlanID string) (model.WorkflowNode, []model.WorkflowStage) {
	source := model.WorkflowNode{
		ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源",
		Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"manual", "push"}},
	}
	tasks := []model.WorkflowNode{
		{
			ID: "build", Type: model.WorkflowNodeBuild, Name: "构建",
			Config: model.WorkflowNodeConfig{BuildPlanID: buildPlanID},
		},
		{
			ID: "shell", Type: model.WorkflowNodeShell, Name: "脚本检查",
			Config: model.WorkflowNodeConfig{
				Script: "echo ready", RuntimeImage: model.DefaultRuntimeImage, WorkingDirectory: ".", TimeoutSeconds: 60,
			},
		},
		{
			ID: "approval", Type: model.WorkflowNodeApproval, Name: "审核",
		},
		{
			ID: "manual", Type: model.WorkflowNodeManual, Name: "人工放行",
		},
		{
			ID: "deploy", Type: model.WorkflowNodeDeploy, Name: "部署",
			Config: model.WorkflowNodeConfig{DeploymentPlanID: deploymentPlanID},
		},
	}
	stages := []model.WorkflowStage{
		{ID: "build", Name: "构建", Tasks: tasks[:1]},
		{ID: "verify", Name: "检查", Tasks: tasks[1:2]},
		{ID: "approval", Name: "审核", Tasks: tasks[2:3]},
		{ID: "manual", Name: "人工放行", Tasks: tasks[3:4]},
		{ID: "deploy", Name: "部署", Tasks: tasks[4:]},
	}
	return source, stages
}

func createTestImageRegistry(t *testing.T, service *Service, name string) *model.ImageRegistry {
	t.Helper()
	registry, err := service.CreateRegistry(context.Background(), "admin", RegistryInput{
		Name: name, Provider: model.RegistryGeneric, Endpoint: "https://registry.example.com", Namespace: "edo",
	})
	if err != nil {
		t.Fatalf("创建测试镜像仓库失败: %v", err)
	}
	return registry
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
	service := NewService(db, repositoryService, secretManager)
	dockerService := dockerengine.NewService(db, secretManager, config.Runtime{})
	kubeService := kube.NewService(db, secretManager, config.Runtime{})
	service.ConfigureExecution(dockerService, deployment.NewService(db, dockerService, kubeService, nil, nil, "", logger), logger)
	return service, db, secretManager, repo.ID
}
