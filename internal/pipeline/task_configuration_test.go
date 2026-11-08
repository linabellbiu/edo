package pipeline

import (
	"context"
	"edo/internal/artifact"
	"edo/internal/cicd"
	"edo/internal/database"
	"edo/internal/model"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInlineImageBuildRequiresRegistryAndRepositoryPaths(t *testing.T) {
	makeNode := func() model.WorkflowNode {
		return model.WorkflowNode{ID: "image", Name: "镜像构建", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{InlineBuild: &model.BuildPlan{Kind: model.BuildPlanDockerfile, DockerfilePath: "docker/Dockerfile", ContextPath: ".", ImageRegistryID: "registry", TimeoutSeconds: 1800, CacheEnabled: true}}}
	}
	valid := makeNode()
	if err := sanitizeWorkflowNode(&valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*model.WorkflowNode){func(n *model.WorkflowNode) { n.Config.InlineBuild.ContextPath = "../outside" }, func(n *model.WorkflowNode) { n.Config.InlineBuild.DockerfilePath = "/etc/passwd" }, func(n *model.WorkflowNode) { n.Config.BuildPlanID = "another-plan" }} {
		node := makeNode()
		mutate(&node)
		if err := sanitizeWorkflowNode(&node); err == nil {
			t.Fatal("非法镜像构建被接受")
		}
	}
	if inlineBuildIdentity("workflow-a", "node") == inlineBuildIdentity("workflow-b", "node") {
		t.Fatal("复制流水线后不能共用构建身份")
	}
}
func TestNamedBuildOutputsRejectCollisionsAndUnsafeNames(t *testing.T) {
	makeNode := func() model.WorkflowNode {
		return model.WorkflowNode{ID: "build", Name: "自定义构建", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{InlineBuild: &model.BuildPlan{Kind: model.BuildPlanScript, Script: "printf built", RuntimeImage: model.DefaultRuntimeImage, TimeoutSeconds: 120}, BuildOutputs: []model.WorkflowBuildOutput{{Name: "BUILD_ARTIFACT", Paths: []string{"./output", "settings/app.yaml"}}}}}
	}
	good := makeNode()
	if err := sanitizeWorkflowNode(&good); err != nil {
		t.Fatal(err)
	}
	for _, outputs := range [][]model.WorkflowBuildOutput{{{Name: "IMAGE_TAG", Paths: []string{"dist"}}}, {{Name: "EDO_APPLICATION_ID", Paths: []string{"dist"}}}, {{Name: "BUILD_ARTIFACT", Paths: []string{"dist", "dist/child"}}}, {{Name: "BUILD_ARTIFACT", Paths: []string{"../../secret"}}}, {{Name: "A", Paths: []string{"dist"}}, {Name: "A", Paths: []string{"out"}}}} {
		node := makeNode()
		node.Config.BuildOutputs = outputs
		if err := sanitizeWorkflowNode(&node); err == nil {
			t.Fatalf("非法产物定义被接受: %+v", outputs)
		}
	}
}
func TestComposeSourceFilesAndManualBodyShareServiceParser(t *testing.T) {
	content := "services:\n  api:\n    image: registry.example/app:${IMAGE_TAG}\n  worker:\n    image: alpine:3.22\n"
	node := model.WorkflowNode{ID: "read-compose", Type: model.WorkflowNodeShell, Name: "检查", Config: model.WorkflowNodeConfig{Script: "true", RuntimeImage: model.DefaultRuntimeImage, TimeoutSeconds: 120}}
	s, _, _, run := newBuildExecutorTestRun(t, node, workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion}, map[string]string{"deploy/docker-compose.yaml": content})
	var app model.Application
	if err := s.db.First(&app, "uuid = ?", run.ApplicationID).Error; err != nil {
		t.Fatal(err)
	}
	text, err := s.readComposeSource(context.Background(), app, "", run.Ref, run.CommitSHA, &model.WorkflowComposeSource{Kind: "repository", Path: "deploy/docker-compose.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{content, text} {
		names, err := composeServiceNames(body)
		if err != nil || strings.Join(names, ",") != "api,worker" {
			t.Fatalf("服务解析错误: %v %v", names, err)
		}
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, UserID: "admin"})
	configuration, err := s.Delivery().SaveVariable(ctx, "admin", "", cicd.VariableInput{Name: "app-compose.yml", ApplicationID: app.ID, Kind: "config_yaml", Usage: "deploy", Value: &content, IsActive: true})
	if err != nil {
		t.Fatal("创建 Compose 配置失败", err)
	}
	configured, err := s.readComposeSource(ctx, app, "", run.Ref, run.CommitSHA, &model.WorkflowComposeSource{Kind: "configuration", ConfigurationID: configuration.ID})
	if err != nil {
		t.Fatal("读取 Compose 配置失败", err)
	}
	if names, err := composeServiceNames(configured); err != nil || strings.Join(names, ",") != "api,worker" {
		t.Fatal("配置来源未统一解析服务", names, err)
	}
	otherApplication := app
	otherApplication.ID = "another-application"
	if _, err := s.readComposeSource(ctx, otherApplication, "", run.Ref, run.CommitSHA, &model.WorkflowComposeSource{Kind: "configuration", ConfigurationID: configuration.ID}); err == nil {
		t.Fatal("不能读取其他应用的 Compose 配置")
	}
	for _, source := range []*model.WorkflowComposeSource{{Kind: "repository", Path: "../../outside"}, {Kind: "configuration", ConfigurationID: "config", Path: "ignored.yaml"}, {Kind: "upstream", ArtifactNodeID: "build", Path: "/etc/passwd"}} {
		if validComposeSource(source) {
			t.Fatal("不安全来源被接受")
		}
	}
	if _, err := composeServiceNames("services: [broken"); err == nil {
		t.Fatal("无效 Compose 不应保留旧服务列表")
	}
}
func TestNamedBuildOutputsAreReadableAsIndependentArchives(t *testing.T) {
	root := t.TempDir()
	groups := filepath.Join(root, "groups")
	for name, body := range map[string]string{"APP": "application", "CONFIG": "services:\n  api:\n    image: alpine:3.22\n"} {
		dir := filepath.Join(groups, name, "deploy")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outputs := []model.WorkflowBuildOutput{{Name: "APP", Paths: []string{"deploy"}}, {Name: "CONFIG", Paths: []string{"deploy"}}}
	packaged, err := packageNamedOutputs(context.Background(), groups, root, outputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"APP", "CONFIG"} {
		info, err := os.Stat(filepath.Join(packaged, name+".tar.gz"))
		if err != nil || info.Size() == 0 {
			t.Fatal("命名归档没有生成", err)
		}
	}
	encoded, err := json.Marshal(workflowBuildPlanSnapshot{Outputs: outputs})
	if err != nil || !strings.Contains(string(encoded), "CONFIG") {
		t.Fatal("产物标识必须进入不可变构建快照")
	}
}
func TestCustomDeploymentBindingIsOptionalAndSyntaxChecked(t *testing.T) {
	node := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{InlineDeploymentPlan: &model.WorkflowDeploymentPlan{Kind: model.DeploymentPlanScript}, RuntimeServices: []model.WorkflowRuntimeService{{Name: "my_app", RestartScript: "docker restart my_app", RecreateScript: "docker compose up -d --force-recreate api"}}}}
	if err := normalizeTaskRuntimeServices(&node); err != nil {
		t.Fatal(err)
	}
	node.Config.RuntimeServices[0].RestartScript = "if then"
	if err := normalizeTaskRuntimeServices(&node); err == nil {
		t.Fatal("无效服务命令不应保存")
	}
	node.Config.RuntimeServices = nil
	if err := normalizeTaskRuntimeServices(&node); err != nil {
		t.Fatal("不关联服务应允许纯脚本部署")
	}
}

func TestInlineCustomBuildSavesAndFreezesWithoutLibraryRecords(t *testing.T) {
	s, db, _, repositoryID := newPipelineTestService(t)
	app, err := s.CreateApplication(context.Background(), "admin", ApplicationInput{Name: "inline_build_app", RepositoryID: repositoryID, PollIntervalSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := s.GetWorkflow(context.Background(), app.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源", Config: model.WorkflowNodeConfig{Events: []string{"manual"}}}
	node := model.WorkflowNode{ID: "build-task", Type: model.WorkflowNodeBuild, Name: "自定义构建", Config: model.WorkflowNodeConfig{InlineBuild: &model.BuildPlan{Kind: model.BuildPlanScript, Script: "mkdir -p output", RuntimeImage: model.DefaultRuntimeImage, WorkingDirectory: ".", TimeoutSeconds: 120}, BuildOutputs: []model.WorkflowBuildOutput{{Name: "BUILD_ARTIFACT", Paths: []string{"output"}}}}}
	var before int64
	db.Model(&model.BuildPlan{}).Count(&before)
	saved, err := s.SaveApplicationWorkflow(context.Background(), app.ID, workflow.Workflow.ID, "admin", WorkflowInput{SchemaVersion: model.WorkflowSchemaVersion, Name: "内置构建", Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: []model.WorkflowStage{{ID: "build-stage", Name: "构建", Tasks: []model.WorkflowNode{node}}}})
	if err != nil || !saved.Valid {
		t.Fatal("任务配置不能直接保存", err, saved)
	}
	run, err := s.newResolvedWorkflowRun(context.Background(), app, saved.Workflow, source, "manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", time.Now())
	if err != nil {
		t.Fatal("内置构建无法生成运行快照", err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	plan := snapshot.BuildPlans[node.ID]
	if plan.ID != inlineBuildIdentity(saved.Workflow.ID, node.ID) || len(plan.Outputs) != 1 || plan.Outputs[0].Name != "BUILD_ARTIFACT" {
		t.Fatal("构建身份与产物定义未冻结", plan)
	}
	var after int64
	db.Model(&model.BuildPlan{}).Count(&after)
	if before != after {
		t.Fatal("流水线构建不应创建独立方案记录")
	}
}

func TestInlineScriptRuntimeUsesTaskConfigurationInsteadOfTemplateMetadata(t *testing.T) {
	config := model.WorkflowNodeConfig{InlineBuild: &model.BuildPlan{}, ToolchainLanguage: "nodejs", RuntimeImage: "node:24-alpine"}
	if got := effectiveBuildRuntimeImage(config, workflowBuildPlanSnapshot{RuntimeImage: "node:26-alpine"}); got != "node:26-alpine" {
		t.Fatal("模板元数据覆盖了任务运行环境", got)
	}
}

func TestNamedUpstreamComposeIsParsedAndFrozenBeforeDeployment(t *testing.T) {
	content := "services:\n  api:\n    image: alpine:3.22\n  worker:\n    image: alpine:3.22\n"
	build := model.WorkflowNode{ID: "build", Name: "自定义构建", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: "build-plan"}}
	deploy := model.WorkflowNode{ID: "deploy", Name: "Compose 部署", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: "deploy-plan", ComposeSource: &model.WorkflowComposeSource{Kind: "upstream", ArtifactNodeID: build.ID, ArtifactName: "CONFIG", Path: "deploy/docker-compose.yaml"}, Compose: &model.ComposeDeploymentSettings{ProjectName: "test-project", TagVariable: "${IMAGE_TAG}"}}}
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "build-stage", Tasks: []model.WorkflowNode{build}}, {ID: "deploy-stage", Tasks: []model.WorkflowNode{deploy}}}, DeploymentPlans: map[string]workflowDeploymentPlanSnapshot{deploy.ID: {ID: "deploy-plan", Kind: model.DeploymentPlanComposeStack}}, AuthorityID: "admin"}
	s, db, store, run := newBuildExecutorTestRun(t, deploy, snapshot, nil)
	root := t.TempDir()
	groups := filepath.Join(root, "groups")
	folder := filepath.Join(groups, "CONFIG", "deploy")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "docker-compose.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	bundles, err := packageNamedOutputs(context.Background(), groups, root, []model.WorkflowBuildOutput{{Name: "CONFIG", Paths: []string{"deploy"}}})
	if err != nil {
		t.Fatal(err)
	}
	var component model.PipelineRunRepository
	if err = db.Where("pipeline_run_id = ?", run.ID).First(&component).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := store.CreateFileFromPath(context.Background(), artifact.BuildOutputInput{BuildMetadata: artifact.BuildMetadata{ApplicationID: run.ApplicationID, PipelineRunID: run.ID, RepositoryID: component.RepositoryID, WorkflowNodeID: build.ID, BuildPlanID: "build-plan", Ref: run.Ref, CommitSHA: run.CommitSHA, PlanSnapshot: "{}", PlanDigest: sha256Digest([]byte("{}")), CreatedBy: "admin"}, SourcePath: bundles, Name: "outputs.tar.gz"})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&model.PipelineNodeRun{ID: "upstream-output", PipelineRunID: run.ID, ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, NodeID: build.ID, StageID: "build-stage", Type: model.WorkflowNodeBuild, Name: build.Name, Status: model.PipelineRunSucceeded, ArtifactID: stored.ID}).Error; err != nil {
		t.Fatal(err)
	}
	var app model.Application
	if err = db.Where("uuid = ?", run.ApplicationID).First(&app).Error; err != nil {
		t.Fatal(err)
	}
	prepared := &executionContext{run: run, node: deploy, snapshot: snapshot, application: app, deploymentPlan: model.DeploymentPlan{ID: "deploy-plan", Kind: model.DeploymentPlanComposeStack}}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, UserID: "admin"})
	if err = s.resolveProducedCompose(ctx, prepared); err != nil {
		t.Fatal("上游命名归档未解析", err)
	}
	names, err := composeServiceNames(prepared.deploymentPlan.ComposeYAML)
	if err != nil || strings.Join(names, ",") != "api,worker" {
		t.Fatal("上游 Compose 服务不完整", names, err)
	}
	var persisted model.PipelineRun
	if err = db.Where("uuid = ?", run.ID).First(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	frozen, err := parseWorkflowSnapshot(&persisted)
	if err != nil || frozen.DeploymentPlans[deploy.ID].ComposeYAML != content {
		t.Fatal("实际 Compose 正文没有固定到运行快照", err)
	}
}

func TestInlineBuildCanReuseItsOwnArtifactWithoutLibraryPlan(t *testing.T) {
	build := model.WorkflowNode{ID: "build", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{InlineBuild: &model.BuildPlan{Kind: model.BuildPlanScript}, BuildOutputs: []model.WorkflowBuildOutput{{Name: "FILES", Paths: []string{"output"}}}}}
	deploy := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy}
	workflow := &model.ReleaseWorkflow{ID: "workflow", Stages: []model.WorkflowStage{{ID: "build-stage", Tasks: []model.WorkflowNode{build}}, {ID: "deploy-stage", Tasks: []model.WorkflowNode{deploy}}}}
	id := inlineBuildIdentity(workflow.ID, build.ID)
	plan := inlineBuildPlansForWorkflow(workflow)[id]
	producer := model.BuildRun{BuildPlanID: id, WorkflowNodeID: build.ID}
	_, resume, ok := manualArtifactBuildNodes(workflow, producer, plan, model.Artifact{Kind: model.ArtifactKindFileBundle})
	if !ok || resume.ID != build.ID {
		t.Fatal("当前内联构建无法选择自己的历史产物")
	}
	other := *workflow
	other.ID = "another-workflow"
	if _, _, ok = manualArtifactBuildNodes(&other, producer, plan, model.Artifact{Kind: model.ArtifactKindFileBundle}); ok {
		t.Fatal("另一条流水线不能复用不同构建身份的产物")
	}
}
