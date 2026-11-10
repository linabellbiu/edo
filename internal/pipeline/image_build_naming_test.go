package pipeline

import (
	"context"
	"encoding/json"
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

func TestNewBuiltinBuildRunIgnoresTargetWithoutChangingStoredHistory(t *testing.T) {
	s, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "image_name_test", RepositoryID: repositoryID})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.CreateBuildPlan(ctx, "admin", BuildPlanInput{Name: "通用镜像构建", Kind: model.BuildPlanDockerfile, TargetStage: "web,api"})
	if err != nil || plan.TargetStage != "" {
		t.Fatalf("新方案仍接受 target: %+v %v", plan, err)
	}
	// 模拟旧方案，证明新运行不展开任务，也不通过读取方案改写历史数据。
	if err := db.Model(&model.BuildPlan{}).Where("uuid = ?", plan.ID).Update("target_stage", "web,api").Error; err != nil {
		t.Fatal(err)
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
	if len(snapshot.Stages[0].Tasks) != 1 || snapshot.BuildPlans["image"].TargetStage != "" {
		t.Fatalf("新运行仍展开 target: %+v", snapshot)
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
