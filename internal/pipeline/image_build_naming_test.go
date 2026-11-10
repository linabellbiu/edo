package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"edo/internal/model"
)

func TestBuiltinImageNameDefaultsToApplicationAndAllowsOverride(t *testing.T) {
	for _, tc := range []struct{ configured, want string }{{"", "my_app"}, {"  ", "my_app"}, {"frontend", "frontend"}, {"backend", "backend"}} {
		prepared := &executionContext{application: model.Application{ID: "app", Name: "my_app"}, registry: model.ImageRegistry{Endpoint: "https://registry.example.com", Namespace: "team"}, node: model.WorkflowNode{Config: model.WorkflowNodeConfig{ImageName: tc.configured, ImageTag: "v1"}}}
		image, _, err := (&Service{}).executionImage(context.Background(), prepared)
		if err != nil || image != "registry.example.com/team/"+tc.want+":v1" {
			t.Fatalf("镜像名称规则不一致: 配置=%q 输出=%q err=%v", tc.configured, image, err)
		}
	}
}

func TestBuiltinBuildRunFreezesOneTargetWithoutChangingStoredHistory(t *testing.T) {
	s, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "image_name_test", RepositoryID: repositoryID})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.CreateBuildPlan(ctx, "admin", BuildPlanInput{Name: "通用镜像构建", Kind: model.BuildPlanDockerfile, TargetStage: " runtime "})
	if err != nil || plan.TargetStage != "runtime" {
		t.Fatalf("新方案没有保存单个 target: %+v %v", plan, err)
	}
	workflow, err := s.GetWorkflow(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := model.WorkflowNode{ID: "source", Name: "代码源", Type: model.WorkflowNodeTrigger, Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"manual"}}}
	saved, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name, Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: []model.WorkflowStage{{ID: "build", Name: "构建", Tasks: []model.WorkflowNode{{ID: "image", Name: "镜像构建", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: plan.ID}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.newResolvedWorkflowRun(ctx, app, saved.Workflow, source, "manual", "refs/tags/v1", strings.Repeat("a", 40), "admin", "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Stages[0].Tasks) != 1 || snapshot.BuildPlans["image"].TargetStage != "runtime" {
		t.Fatalf("新运行未冻结单个 target: %+v", snapshot)
	}
	if _, err := s.UpdateBuildPlan(ctx, plan.ID, BuildPlanInput{Name: plan.Name, Kind: model.BuildPlanDockerfile, TargetStage: "builder"}); err != nil {
		t.Fatal(err)
	}
	frozen, err := parseWorkflowSnapshot(run)
	if err != nil || frozen.BuildPlans["image"].TargetStage != "runtime" {
		t.Fatal("修改方案改变了已生成快照", err)
	}
	next, err := s.newResolvedWorkflowRun(ctx, app, saved.Workflow, source, "manual", "refs/tags/v2", strings.Repeat("b", 40), "admin", "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	nextSnapshot, err := parseWorkflowSnapshot(next)
	if err != nil || nextSnapshot.BuildPlans["image"].TargetStage != "builder" {
		t.Fatal("新运行没有采用修改后的阶段", err)
	}
	// 旧的无效列表明确阻止新运行，读取和验证不改写原记录。
	if err := db.Model(&model.BuildPlan{}).Where("uuid = ?", plan.ID).Update("target_stage", "web,api").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.newResolvedWorkflowRun(ctx, app, saved.Workflow, source, "manual", "main", strings.Repeat("a", 40), "admin", "", time.Now().UTC()); !errors.Is(err, ErrInvalidBuildTarget) {
		t.Fatalf("旧列表 target 未阻止新运行: %v", err)
	}
	var stored model.BuildPlan
	if err := db.First(&stored, "uuid = ?", plan.ID).Error; err != nil || stored.TargetStage != "web,api" {
		t.Fatalf("读取方案改写了存储: %+v %v", stored, err)
	}
	var historical workflowBuildPlanSnapshot
	if err := json.Unmarshal([]byte(`{"kind":"dockerfile","target_stage":"web"}`), &historical); err != nil || historical.TargetStage != "web" {
		t.Fatal("历史快照无法按原配置读取", err)
	}
}

func TestBuildTargetAcceptsOnlyOneStage(t *testing.T) {
	for _, value := range []string{"", "runtime", "build-stage_1.0", " runtime "} {
		normalized, err := normalizeBuildPlanInput(BuildPlanInput{Name: "目标阶段", Kind: model.BuildPlanDockerfile, TargetStage: value})
		if err != nil || normalized.TargetStage != strings.TrimSpace(value) {
			t.Fatalf("有效阶段被拒绝: %q %v", value, err)
		}
	}
	for _, value := range []string{"web,api", "web api", "web\napi", "web\tapi", "web，api", "--target=web", "${TARGET}", strings.Repeat("a", 129)} {
		if _, err := normalizeBuildPlanInput(BuildPlanInput{Name: "目标阶段", Kind: model.BuildPlanDockerfile, TargetStage: value}); !errors.Is(err, ErrInvalidBuildTarget) {
			t.Fatalf("无效阶段未拒绝: %q %v", value, err)
		}
		node := model.WorkflowNode{ID: "build", Name: "镜像构建", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{InlineBuild: &model.BuildPlan{Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile", ContextPath: ".", TargetStage: value}}}
		if err := normalizeInlineBuild(&node); !errors.Is(err, ErrInvalidWorkflow) || !strings.Contains(err.Error(), "Docker target") {
			t.Fatalf("内联构建未拒绝无效阶段: %q %v", value, err)
		}
	}
	node := model.WorkflowNode{ID: "build", Name: "镜像构建", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{InlineBuild: &model.BuildPlan{Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile", ContextPath: ".", TargetStage: " runtime "}}}
	if err := normalizeInlineBuild(&node); err != nil || node.Config.InlineBuild.TargetStage != "runtime" {
		t.Fatalf("内联阶段未保存: %+v %v", node, err)
	}
	script, err := normalizeBuildPlanInput(BuildPlanInput{Name: "脚本构建", Kind: model.BuildPlanScript, Script: "echo ok", TargetStage: "web,api"})
	if err != nil || script.TargetStage != "" {
		t.Fatal("隐藏的 Docker 阶段影响脚本构建", err)
	}
}
