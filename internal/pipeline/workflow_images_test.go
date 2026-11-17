package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"edo/internal/model"
)

func workflowBuildProductTestStages(tasks ...model.WorkflowNode) []model.WorkflowStage {
	return []model.WorkflowStage{{ID: "build", Name: "构建", Tasks: tasks}}
}

func inlineDockerfileBuild(id, name, imageName, registry string) model.WorkflowNode {
	return model.WorkflowNode{ID: id, Type: model.WorkflowNodeBuild, Name: name, Config: model.WorkflowNodeConfig{
		ImageName:   imageName,
		InlineBuild: &model.BuildPlan{Kind: model.BuildPlanDockerfile, ImageRegistryID: registry},
	}}
}

func inlineScriptBuild(id, name string, outputs ...string) model.WorkflowNode {
	items := make([]model.WorkflowBuildOutput, 0, len(outputs))
	for _, output := range outputs {
		items = append(items, model.WorkflowBuildOutput{Name: output, Paths: []string{"dist"}})
	}
	return model.WorkflowNode{ID: id, Type: model.WorkflowNodeBuild, Name: name, Config: model.WorkflowNodeConfig{
		InlineBuild:  &model.BuildPlan{Kind: model.BuildPlanScript, ArtifactPath: "dist"},
		BuildOutputs: items,
	}}
}

func TestWorkflowBuildProductsListStaticNames(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	application := &model.Application{ID: uuid.NewString(), Name: "demo_app"}
	stages := workflowBuildProductTestStages(
		inlineDockerfileBuild("api", "构建 API", "demo-api", "registry-1"),
		inlineDockerfileBuild("web", "构建 Web", "registry.example.com/team/demo-web", "registry-1"),
		inlineScriptBuild("files", "打包", "APP_PACKAGE", "APP_CONFIG"),
	)
	images, artifacts, issues, err := service.workflowBuildProducts(context.Background(), application, stages)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("合法构建产物名称不应产生问题: %+v", issues)
	}
	if len(images) != 2 || images[0].ImageName != "demo-api" || images[1].ImageName != "registry.example.com/team/demo-web" {
		t.Fatalf("镜像产物名称解析不正确: %+v", images)
	}
	if len(artifacts) != 2 || artifacts[0].Name != "APP_PACKAGE" || artifacts[1].Name != "APP_CONFIG" {
		t.Fatalf("自定义构建产物名称解析不正确: %+v", artifacts)
	}
}

func TestWorkflowBuildProductsRejectInvalidAndDuplicateNames(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	application := &model.Application{ID: uuid.NewString(), Name: "demo_app"}
	stages := workflowBuildProductTestStages(
		inlineDockerfileBuild("api", "构建 API", "Demo_API", "registry-1"),
		inlineDockerfileBuild("web", "构建 Web", "demo-image", "registry-1"),
		inlineDockerfileBuild("worker", "构建 Worker", "demo-image", "registry-1"),
		inlineScriptBuild("files", "打包", "APP_PACKAGE", "APP_PACKAGE"),
		inlineScriptBuild("extra", "补充打包", "APP_PACKAGE"),
		inlineScriptBuild("bad", "错误打包", "1BAD"),
	)
	_, _, issues, err := service.workflowBuildProducts(context.Background(), application, stages)
	if err != nil {
		t.Fatal(err)
	}
	codes := make([]string, 0, len(issues))
	for _, issue := range issues {
		codes = append(codes, issue.Code)
	}
	joined := strings.Join(codes, ",")
	for _, want := range []string{"build_image_name_invalid", "build_image_name_duplicate", "build_artifact_name_duplicate", "build_artifact_name_invalid"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少静态检查问题 %s: %v", want, issues)
		}
	}
}

func TestWorkflowValidationSurfacesBuildProductIssues(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	application := &model.Application{ID: uuid.NewString(), Name: "demo_app"}
	source := model.WorkflowNode{ID: "trigger", Type: model.WorkflowNodeTrigger, Name: "代码源",
		Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"manual"}}}
	stages := workflowBuildProductTestStages(
		inlineDockerfileBuild("api", "构建 API", "demo-api", "registry-1"),
		inlineDockerfileBuild("web", "构建 Web", "demo-api", "registry-1"),
	)
	issues := service.validateWorkflow(context.Background(), application, model.WorkflowSchemaVersion, source, stages)
	if !hasWorkflowIssue(issues, "build_image_name_duplicate") {
		t.Fatalf("流水线校验必须报告重复的构建产物名称: %+v", issues)
	}
}
