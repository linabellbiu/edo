package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"edo/internal/dockerengine"
	"edo/internal/model"
)

type workflowRuntimeManagerStub struct {
	mu        sync.Mutex
	installed map[string]bool
}

func (s *workflowRuntimeManagerStub) InspectScriptRuntimeImage(_ context.Context, image string) (dockerengine.ScriptRuntimeImageStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return dockerengine.ScriptRuntimeImageStatus{Image: image, Installed: s.installed[image]}, nil
}

func (s *workflowRuntimeManagerStub) PrepareScriptRuntimeImage(_ context.Context, image string) (dockerengine.ScriptRuntimeImageStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installed[image] = true
	return dockerengine.ScriptRuntimeImageStatus{Image: image, ImageID: "sha256:runtime", Installed: true}, nil
}

func presetTestService(t *testing.T) (*Service, *workflowRuntimeManagerStub) {
	t.Helper()
	service, _, _, _ := newPipelineTestService(t)
	runtimes := &workflowRuntimeManagerStub{installed: map[string]bool{
		"golang:1.26-alpine": true,
		"node:24-alpine":     true,
		"python:3.14-alpine": true,
	}}
	service.ConfigureWorkflowRuntimeManager(runtimes)
	return service, runtimes
}

func TestWorkflowPresetCatalogContainsDockerGoNodeJSPythonFlows(t *testing.T) {
	presets := ListWorkflowPresets()
	if len(presets) != 13 {
		t.Fatalf("流水线模板数量为 %d，期望空白模板、三份 Docker 模板和三种语言各三份模板", len(presets))
	}
	wanted := map[string]string{
		"blank": "quickstart", "docker-container": "docker", "docker-compose": "docker", "docker-kubernetes": "docker",
		"go-host": "go", "go-artifact-host": "go", "go-kubernetes": "go",
		"nodejs-host": "nodejs", "nodejs-artifact-host": "nodejs", "nodejs-kubernetes": "nodejs",
		"python-host": "python", "python-artifact-host": "python", "python-kubernetes": "python",
	}
	for _, preset := range presets {
		category, ok := wanted[preset.Key]
		if !ok {
			t.Fatalf("模板目录包含未知模板: %+v", preset)
		}
		if preset.Category != category || preset.Name == "" || preset.Description == "" {
			t.Fatalf("模板目录信息不完整: %+v", preset)
		}
		delete(wanted, preset.Key)
	}
	if len(wanted) != 0 {
		t.Fatalf("模板目录缺少项目: %+v", wanted)
	}
}

func TestCreateDockerWorkflowTemplateDoesNotRequireLanguageRuntime(t *testing.T) {
	service, _ := presetTestService(t)
	result, err := service.CreateWorkflowTemplateFromPreset(context.Background(), "admin", "docker-container", "")
	if err != nil {
		t.Fatalf("创建 Docker 容器流水线模板失败: %v", err)
	}
	stages := result.WorkflowTemplate.Stages
	if len(stages) != 2 || stages[0].Tasks[0].Name != "镜像构建" || stages[1].Tasks[0].Name != "Docker 容器部署" {
		t.Fatalf("Docker 容器模板任务不正确: %+v", stages)
	}
	build := stages[0].Tasks[0].Config
	if build.RuntimeImage != "" || build.ToolchainLanguage != "" || build.ToolchainVersion != "" {
		t.Fatalf("通用 Docker 模板不应绑定语言工具链: %+v", build)
	}
}

func TestCreateWorkflowTemplateFromPresetCreatesEditableDraft(t *testing.T) {
	service, _ := presetTestService(t)
	ctx := context.Background()

	first, err := service.CreateWorkflowTemplateFromPreset(ctx, "admin", "go-kubernetes", "1.26")
	if err != nil {
		t.Fatalf("从 Go Kubernetes 模板创建方案失败: %v", err)
	}
	created := first.WorkflowTemplate
	if created.IsActive || created.Name != "Go · 测试、镜像构建并部署到 Kubernetes" {
		t.Fatalf("模板应创建同名未启用草稿: %+v", created)
	}
	if len(created.Stages) != 3 || len(created.Stages[0].Tasks) != 1 {
		t.Fatalf("模板没有生成完整阶段: %+v", created.Stages)
	}
	testTask := created.Stages[0].Tasks[0]
	if testTask.Type != model.WorkflowNodeShell || testTask.Config.RuntimeImage != "golang:1.26-alpine" ||
		testTask.Config.ToolchainLanguage != "go" || testTask.Config.ToolchainVersion != "1.26" ||
		!strings.Contains(testTask.Config.Script, "go test ./...") {
		t.Fatalf("Go 单元测试任务不正确: %+v", testTask)
	}
	buildTask := created.Stages[1].Tasks[0]
	deployTask := created.Stages[2].Tasks[0]
	if buildTask.Type != model.WorkflowNodeBuild || buildTask.Name != "镜像构建" || buildTask.Config.BuildPlanID != "" ||
		buildTask.Config.RuntimeImage != "golang:1.26-alpine" || buildTask.Config.ToolchainVersion != "1.26" ||
		deployTask.Type != model.WorkflowNodeDeploy || deployTask.Name != "Kubernetes 部署" || deployTask.Config.DeploymentPlanID != "" {
		t.Fatalf("模板不应绑定具体资源且任务名称应说明用途: build=%+v deploy=%+v", buildTask, deployTask)
	}
	if first.Valid || !hasWorkflowIssue(first.Issues, "missing_build_plan") || !hasWorkflowIssue(first.Issues, "missing_deployment_plan") {
		t.Fatalf("未完成资源绑定的模板草稿应返回待配置项: valid=%t issues=%+v", first.Valid, first.Issues)
	}

	second, err := service.CreateWorkflowTemplateFromPreset(ctx, "admin", "go-kubernetes", "1.26")
	if err != nil {
		t.Fatalf("重复选择模板应自动使用不重复名称: %v", err)
	}
	if second.WorkflowTemplate.Name != created.Name+" 2" {
		t.Fatalf("重复模板名称未自动递增: %q", second.WorkflowTemplate.Name)
	}

	if _, err := service.CreateWorkflowTemplateFromPreset(ctx, "admin", "java", "21"); err != ErrInvalidWorkflow {
		t.Fatalf("未知模板应被拒绝: %v", err)
	}
}

func TestPythonHostPresetDoesNotAddUnsupportedTestStep(t *testing.T) {
	service, _ := presetTestService(t)
	result, err := service.CreateWorkflowTemplateFromPreset(context.Background(), "admin", "python-artifact-host", "3.14")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.WorkflowTemplate.Stages) != 2 || result.WorkflowTemplate.Stages[0].Tasks[0].Type != model.WorkflowNodeBuild {
		t.Fatalf("Python 主机制品模板应从构建任务开始: %+v", result.WorkflowTemplate.Stages)
	}
}

func TestGoNodeJSPythonPresetTasksMatchCatalog(t *testing.T) {
	service, _ := presetTestService(t)
	tests := []struct {
		key          string
		stageCount   int
		runtimeImage string
		script       string
	}{
		{key: "go-artifact-host", stageCount: 3, runtimeImage: "golang:1.26-alpine", script: "go test ./..."},
		{key: "nodejs-artifact-host", stageCount: 3, runtimeImage: "node:24-alpine", script: "npm test"},
		{key: "python-artifact-host", stageCount: 2},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			version := "3.14"
			if strings.HasPrefix(test.key, "go-") {
				version = "1.26"
			} else if strings.HasPrefix(test.key, "nodejs-") {
				version = "24"
			}
			result, err := service.CreateWorkflowTemplateFromPreset(context.Background(), "admin", test.key, version)
			if err != nil {
				t.Fatal(err)
			}
			stages := result.WorkflowTemplate.Stages
			if len(stages) != test.stageCount {
				t.Fatalf("阶段数量为 %d，期望 %d: %+v", len(stages), test.stageCount, stages)
			}
			if test.runtimeImage != "" {
				task := stages[0].Tasks[0]
				if task.Type != model.WorkflowNodeShell || task.Config.RuntimeImage != test.runtimeImage || !strings.Contains(task.Config.Script, test.script) {
					t.Fatalf("默认测试任务不正确: %+v", task)
				}
			}
			build := stages[len(stages)-2].Tasks[0]
			deploy := stages[len(stages)-1].Tasks[0]
			if build.Type != model.WorkflowNodeBuild || deploy.Type != model.WorkflowNodeDeploy {
				t.Fatalf("模板必须生成真实构建和部署任务: build=%+v deploy=%+v", build, deploy)
			}
		})
	}
}

func TestWorkflowRuntimeMustBePreparedBeforeTemplateCreation(t *testing.T) {
	service, runtimes := presetTestService(t)
	ctx := context.Background()

	versions, err := service.ListWorkflowRuntimeVersions(ctx, "go")
	if err != nil || len(versions) != 5 || !versions[0].Recommended || !versions[0].Installed || versions[1].Installed || !versions[3].Legacy {
		t.Fatalf("构建版本目录状态不正确: versions=%+v err=%v", versions, err)
	}
	if _, err := service.CreateWorkflowTemplateFromPreset(ctx, "admin", "go-host", "1.25"); !errors.Is(err, ErrWorkflowRuntimeNotPrepared) {
		t.Fatalf("未下载的构建版本应阻止创建: %v", err)
	}
	prepared, err := service.PrepareWorkflowRuntimeVersion(ctx, "go", "1.25")
	if err != nil || !prepared.Installed || !runtimes.installed["golang:1.25-alpine"] {
		t.Fatalf("下载后版本未就绪: runtime=%+v err=%v", prepared, err)
	}
	created, err := service.CreateWorkflowTemplateFromPreset(ctx, "admin", "go-host", "1.25")
	if err != nil {
		t.Fatalf("版本就绪后仍无法创建: %v", err)
	}
	build := created.WorkflowTemplate.Stages[0].Tasks[0]
	if build.Config.ToolchainVersion != "1.25" || build.Config.RuntimeImage != "golang:1.25-alpine" {
		t.Fatalf("所选版本未写入构建任务: %+v", build.Config)
	}
}

type blockingWorkflowRuntimeManager struct {
	mu        sync.Mutex
	installed bool
	started   chan struct{}
	release   chan struct{}
}

func (m *blockingWorkflowRuntimeManager) InspectScriptRuntimeImage(_ context.Context, image string) (dockerengine.ScriptRuntimeImageStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return dockerengine.ScriptRuntimeImageStatus{Image: image, Installed: m.installed}, nil
}

func (m *blockingWorkflowRuntimeManager) PrepareScriptRuntimeImage(_ context.Context, image string) (dockerengine.ScriptRuntimeImageStatus, error) {
	select {
	case m.started <- struct{}{}:
	default:
	}
	<-m.release
	m.mu.Lock()
	m.installed = true
	m.mu.Unlock()
	return dockerengine.ScriptRuntimeImageStatus{Image: image, ImageID: "sha256:runtime", Installed: true}, nil
}

func TestStartPrepareWorkflowRuntimeReturnsBeforeImagePullCompletes(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	manager := &blockingWorkflowRuntimeManager{started: make(chan struct{}, 1), release: make(chan struct{})}
	service.ConfigureWorkflowRuntimeManager(manager)

	started, err := service.StartPrepareWorkflowRuntimeVersion(context.Background(), "nodejs", "20")
	if err != nil || started.Installed || started.PreparationStatus != workflowRuntimePreparing {
		t.Fatalf("启动异步下载返回不正确: runtime=%+v err=%v", started, err)
	}
	select {
	case <-manager.started:
	case <-time.After(time.Second):
		t.Fatal("后台镜像拉取没有启动")
	}
	versions, err := service.ListWorkflowRuntimeVersions(context.Background(), "nodejs")
	if err != nil || versions[3].PreparationStatus != workflowRuntimePreparing {
		t.Fatalf("下载中状态没有对外返回: versions=%+v err=%v", versions, err)
	}
	close(manager.release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		versions, err = service.ListWorkflowRuntimeVersions(context.Background(), "nodejs")
		if err == nil && versions[3].Installed && versions[3].PreparationStatus == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("后台镜像拉取完成后状态未更新: versions=%+v err=%v", versions, err)
}
