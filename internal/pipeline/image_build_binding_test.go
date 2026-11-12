package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/pipelineimage"
)

func TestNewDeploymentUsesTagOnlyAndPreservesStoredLegacyBindings(t *testing.T) {
	s, db, _, repo := newPipelineTestService(t)
	ctx := deliveryTestContext()
	authorizeRegistryTestActors(t, s, "admin")
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "binding_snapshot", RepositoryID: repo})
	if err != nil {
		t.Fatal(err)
	}
	target := deploymentPlanTargetInput(t, s, model.DeploymentTarget{Name: "部署主机", Platform: model.DeploymentSSH, EnvironmentID: "env", HostID: "host", WorkingDirectory: "/srv/app"})
	if err = db.Create(&model.HostCapability{HostID: target.HostID, Kind: model.HostCapabilityDocker, Status: model.HostCapabilityReady}).Error; err != nil {
		t.Fatal(err)
	}
	execution := savedRegistryExecutionForTest(t, s, "")
	execution.Compose = &model.ComposeDeploymentSettings{TagVariable: "${EDO_IMAGE_TAG}"}
	plan, err := s.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{Name: "通用部署", Kind: model.DeploymentPlanComposeStack, ComposeYAML: "services:\n  api:\n    image: registry.example.com/team/api:${EDO_IMAGE_TAG}\n", ExecutionConfig: execution, DeploymentTarget: target})
	if err != nil {
		t.Fatal(err)
	}
	build, err := s.CreateBuildPlan(ctx, "admin", BuildPlanInput{Name: "镜像构建", Kind: model.BuildPlanDockerfile})
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.GetWorkflow(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := model.WorkflowNode{ID: "source", Name: "代码源", Type: model.WorkflowNodeTrigger, Config: model.WorkflowNodeConfig{Events: []string{"manual"}, Branch: "main"}}
	config := model.WorkflowNodeConfig{DeploymentPlanID: plan.ID, ImageBuildNodeID: "removed-build", ComposeBuildBindings: []model.ComposeBuildBinding{{Service: "removed-service", BuildNodeID: "removed-build"}}}
	saved, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{Name: current.Workflow.Name, Revision: current.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Activate: true, Source: source, Stages: []model.WorkflowStage{{ID: "build", Name: "构建", Tasks: []model.WorkflowNode{{ID: "api-build", Name: "API", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: build.ID, ImageName: "custom/api"}}}}, {ID: "deploy", Name: "部署", Tasks: []model.WorkflowNode{{ID: "deploy", Name: "部署", Type: model.WorkflowNodeDeploy, Config: config}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.Workflow.Stages[1].Tasks[0].Config; got.ImageBuildNodeID != "removed-build" || len(got.ComposeBuildBindings) != 1 {
		t.Fatal("保存时丢失已有历史关联数据")
	}
	run, err := s.newResolvedWorkflowRun(ctx, app, saved.Workflow, source, "manual", "refs/tags/v3", strings.Repeat("a", 40), "admin", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.PreserveComposeImageReferences {
		t.Fatal("新运行未保留用户镜像引用")
	}
	if snapshot.ImageTagIncludesName || snapshot.Stages[1].Tasks[0].Config.ImageBuildNodeID != "" || len(snapshot.Stages[1].Tasks[0].Config.ComposeBuildBindings) != 0 {
		t.Fatal("新运行仍使用完整镜像变量或要求构建关联")
	}
	var storedWorkflow model.ReleaseWorkflow
	if err := db.First(&storedWorkflow, "uuid = ?", saved.Workflow.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedWorkflow.Stages[1].Tasks[0].Config.ImageBuildNodeID != "removed-build" || storedWorkflow.Stages[1].Tasks[0].Config.ComposeBuildBindings[0].Service != "removed-service" {
		t.Fatal("创建新运行回写了已保存历史数据")
	}
	var stored model.DeploymentPlan
	if err = db.WithContext(context.Background()).First(&stored, "uuid = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ExecutionConfig == nil || stored.ExecutionConfig.DeploymentRegistryID != execution.DeploymentRegistryID {
		t.Fatal("通用方案执行配置被任务修改")
	}
}

func TestHistoricalSelectedBuildImagesUseActualNamesAndFrozenDigests(t *testing.T) {
	s, db, _, _ := newPipelineTestService(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer server.Close()
	project := database.DefaultProjectID(database.DefaultDepartmentID)
	registry := model.ImageRegistry{ID: "selected", Name: "部署仓库", ProjectID: project, Endpoint: server.URL, Namespace: "team", Provider: model.RegistryGeneric, AllowInsecureHTTP: true, IsActive: true}
	if err := db.Create(&registry).Error; err != nil {
		t.Fatal(err)
	}
	base := strings.TrimPrefix(server.URL, "http://") + "/team/"
	images := map[string]model.Artifact{}
	for index, name := range []string{"default-app", "custom/api"} {
		id := []string{"web-build", "api-build"}[index]
		image := model.Artifact{ID: id, ProjectID: project, ApplicationID: "app", PipelineRunID: "run", Name: "display:v1", Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable, StorageKind: model.ArtifactStorageKindRegistry, ImageRegistryID: registry.ID, ImageRef: base + name + "@sha256:" + strings.Repeat([]string{"a", "b"}[index], 64)}
		image.Digest = "sha256:" + strings.Repeat([]string{"a", "b"}[index], 64)
		if err := db.Create(&image).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.PipelineNodeRun{ID: id, ProjectID: project, PipelineRunID: "run", NodeID: id, ArtifactID: image.ID, Status: model.PipelineRunSucceeded}).Error; err != nil {
			t.Fatal(err)
		}
		images[id] = image
	}
	node := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentRegistryID: registry.ID, ImageBuildNodeID: "api-build", Compose: &model.ComposeDeploymentSettings{TagVariable: "${EDO_IMAGE_TAG}"}, ComposeBuildBindings: []model.ComposeBuildBinding{{Service: "web", BuildNodeID: "web-build"}, {Service: "api", BuildNodeID: "api-build"}}}}
	p := &executionContext{run: model.PipelineRun{ID: "run", ProjectID: project, ApplicationID: "app"}, node: node, deploymentPlan: model.DeploymentPlan{Kind: model.DeploymentPlanComposeStack}, variables: map[string]string{"EDO_IMAGE_TAG": "user-override"}, snapshot: workflowSnapshot{ImageTagIncludesName: true, IndependentDeploymentImages: true, BuildPlans: map[string]workflowBuildPlanSnapshot{"api-build": {Kind: model.BuildPlanDockerfile}, "web-build": {Kind: model.BuildPlanDockerfile}}, Stages: []model.WorkflowStage{{Tasks: []model.WorkflowNode{{ID: "api-build", Type: model.WorkflowNodeBuild}, {ID: "web-build", Type: model.WorkflowNodeBuild}}}, {Tasks: []model.WorkflowNode{node}}}}}
	if err := s.addDeploymentPipelineVariables(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if p.variables["EDO_IMAGE_TAG"] != base+"custom/api:v1" {
		t.Fatal("变量没有采用所选任务的真实名称", p.variables)
	}
	source := "services:\n  web:\n    image: ${EDO_IMAGE_TAG}\n  api:\n    image: ${EDO_IMAGE_TAG}\n"
	content, err := s.resolveConfiguredComposeImages(t.Context(), p, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range images {
		if !strings.Contains(content, image.ImageRef) {
			t.Fatal("服务没有固定到其所选构建的 Digest", content)
		}
	}
	if requests != 0 {
		t.Fatal("选择已构建镜像后仍重新追踪可变 Tag")
	}
	p.node.Config.Compose.Services = []string{"api"}
	scoped, err := s.resolveConfiguredComposeImages(t.Context(), p, source)
	if err != nil || strings.Contains(scoped, "${EDO_IMAGE_TAG}") || len(p.composeArtifacts) != 1 {
		t.Fatal("服务范围之外的已关联镜像仍残留未解析变量", scoped, err)
	}
	p.node.Config.Compose.Services = nil

	values, err := deploymentScriptEnvironment(p)
	if err != nil || values["EDO_IMAGE_TAG"] != base+"custom/api:v1" || values["IMAGE_TAG"] != "" {
		t.Fatal("脚本变量与所选构建不一致", values, err)
	}
	if _, err := s.resolveConfiguredComposeImages(t.Context(), p, strings.ReplaceAll(source, "image: ${EDO_IMAGE_TAG}", "image: wrong:${EDO_IMAGE_TAG}")); err == nil {
		t.Fatal("接受把完整镜像当成版本拼接")
	}
	p.node.Config.ImageBuildNodeID = ""
	p.node.Config.ComposeBuildBindings = nil
	if err := s.addDeploymentPipelineVariables(t.Context(), p); err != nil || p.variables["EDO_IMAGE_TAG"] != "" {
		t.Fatal("未选构建时猜测了镜像", err)
	}
	if _, err := s.resolveConfiguredComposeImages(t.Context(), p, source); err == nil {
		t.Fatal("未选构建却允许使用内置镜像引用")
	}
	p.node.Config.ImageBuildNodeID = "api-build"
	p.run.ProjectID = "foreign"
	if err := s.addDeploymentPipelineVariables(t.Context(), p); err == nil {
		t.Fatal("读取了其他项目的构建产物")
	}
}

func TestImageBuildBindingsRequirePriorImageTasks(t *testing.T) {
	build := func(id string) model.WorkflowNode { return model.WorkflowNode{ID: id, Type: model.WorkflowNodeBuild} }
	deploy := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy}
	kinds := map[string]model.BuildPlanKind{"prior": model.BuildPlanDockerfile, "parallel": model.BuildPlanDockerfile, "later": model.BuildPlanDockerfile, "file": model.BuildPlanScript}
	for _, id := range []string{"prior", "parallel", "later", "file", "foreign"} {
		deploy.Config.ImageBuildNodeID = id
		stages := []model.WorkflowStage{{Tasks: []model.WorkflowNode{build("prior"), build("file")}}, {Tasks: []model.WorkflowNode{deploy, build("parallel")}}, {Tasks: []model.WorkflowNode{build("later")}}}
		issues := validateImageBuildBindings(stages, kinds)
		if (len(issues) == 0) != (id == "prior") {
			t.Fatalf("构建来源阶段或类型校验错误: %s %+v", id, issues)
		}
	}
	deploy.Config.ImageBuildNodeID = "prior"
	deploy.Config.ComposeBuildBindings = []model.ComposeBuildBinding{{Service: "api", BuildNodeID: "prior"}, {Service: "api", BuildNodeID: "prior"}}
	if validImageBuildBindingShape(deploy) {
		t.Fatal("接受重复服务关联")
	}
}

func TestFullImageReferenceDoesNotUseDisplayNameAsRepository(t *testing.T) {
	for _, name := range []string{"app", "custom", "nested/api"} {
		image := model.Artifact{Name: "display:v2", ImageRef: "registry.example.com/team/" + name + "@sha256:" + strings.Repeat("a", 64)}
		value, err := pipelineimage.TaggedReference(image)
		if err != nil || value != "registry.example.com/team/"+name+":v2" {
			t.Fatal(value, err)
		}
		if expanded := expandPipelineBuildArgument("${EDO_IMAGE_TAG}|${EDO_GIT_TAG}", model.PipelineRun{Ref: "refs/tags/v2"}, value); expanded != value+"|v2" {
			t.Fatal("构建参数混淆完整镜像和版本", expanded)
		}

	}
}
