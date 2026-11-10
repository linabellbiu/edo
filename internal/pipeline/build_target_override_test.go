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

func TestWorkflowBuildTargetOverridePrecedenceAndFrozenSnapshot(t *testing.T) {
	s, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "target_override", RepositoryID: repositoryID})
	if err != nil {
		t.Fatal(err)
	}
	registry := model.ImageRegistry{ID: "target-registry", Name: "阶段测试仓库", Endpoint: "https://registry.example.com", IsActive: true}
	if err := db.Create(&registry).Error; err != nil {
		t.Fatal(err)
	}
	plan, err := s.CreateBuildPlan(ctx, "admin", BuildPlanInput{Name: "默认构建阶段", Kind: model.BuildPlanDockerfile, TargetStage: "runtime", ImageRegistryID: registry.ID})
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.GetWorkflow(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := current.Workflow.Source
	pointer := func(value string) *string { return &value }
	for _, inline := range []bool{false, true} {
		for _, tc := range []struct {
			name, fallback, want                        string
			defaultPlatform, taskPlatform, wantPlatform string
			override                                    *string
		}{
			{"沿用模板", "runtime", "runtime", "", "", "linux/amd64", nil},
			{"覆盖模板", "runtime", "builder", "linux/arm64", "linux/amd64", "linux/amd64", pointer(" builder ")},
			{"清空覆盖", "runtime", "", "linux/amd64", "linux/arm64", "linux/arm64", pointer("")},
			{"模板为空", "", "", "linux/arm64", "", "linux/arm64", nil},
		} {
			config := model.WorkflowNodeConfig{BuildPlanID: plan.ID, TargetStage: tc.override, BuildPlatform: tc.taskPlatform}
			if _, err := s.UpdateBuildPlan(ctx, plan.ID, BuildPlanInput{Name: plan.Name, Kind: model.BuildPlanDockerfile, TargetStage: tc.fallback, Platform: tc.defaultPlatform, ImageRegistryID: registry.ID}); err != nil {
				t.Fatal(err)
			}
			if inline {
				config.BuildPlanID = ""
				config.InlineBuild = &model.BuildPlan{Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile", ContextPath: ".", TargetStage: tc.fallback, Platform: tc.defaultPlatform, ImageRegistryID: registry.ID, TimeoutSeconds: 1800}
			}
			saved, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{SchemaVersion: model.WorkflowSchemaVersion, Name: current.Workflow.Name, Revision: current.Workflow.Revision, Activate: true, Source: source, Stages: []model.WorkflowStage{{ID: "build", Name: "构建", Tasks: []model.WorkflowNode{{ID: "image", Name: "镜像构建", Type: model.WorkflowNodeBuild, Config: config}}}}})
			if err != nil {
				t.Fatalf("%s inline=%v 保存失败: %v", tc.name, inline, err)
			}
			current = saved
			reopened, err := s.GetWorkflow(ctx, app.ID)
			if err != nil {
				t.Fatal(err)
			}
			storedConfig := reopened.Workflow.Stages[0].Tasks[0].Config
			if (storedConfig.TargetStage == nil) != (tc.override == nil) || (tc.override != nil && *storedConfig.TargetStage != strings.TrimSpace(*tc.override)) {
				t.Fatalf("覆盖值或明确空值没有持久化: %+v", storedConfig)
			}
			if storedConfig.BuildPlatform != tc.taskPlatform {
				t.Fatal("任务平台没有持久化")
			}
			if inline && storedConfig.InlineBuild.TargetStage != tc.fallback {
				t.Fatal("任务覆盖修改了内联模板默认值")
			}
			run, err := s.newResolvedWorkflowRun(ctx, app, saved.Workflow, source, "manual", "main", strings.Repeat("a", 40), "admin", "", time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := parseWorkflowSnapshot(run)
			if err != nil || snapshot.BuildPlans["image"].TargetStage != tc.want {
				t.Fatalf("%s inline=%v 生效阶段错误: %+v %v", tc.name, inline, snapshot.BuildPlans["image"], err)
			}
			if snapshot.BuildPlans["image"].Platform != tc.wantPlatform {
				t.Fatalf("构建平台未按优先级冻结: %+v", snapshot.BuildPlans["image"])
			}
			platform, err := workflowBuildPlatform(*snapshot, "image", snapshot.BuildPlans["image"])
			if err != nil || platform != tc.wantPlatform {
				t.Fatalf("执行平台偏离已保存选择: %q %v", platform, err)
			}
			var stored model.BuildPlan
			if err := db.First(&stored, "uuid = ?", plan.ID).Error; err != nil || stored.TargetStage != tc.fallback {
				t.Fatal("运行覆盖污染了公共方案", err)
			}
			if _, err := s.UpdateBuildPlan(ctx, plan.ID, BuildPlanInput{Name: plan.Name, Kind: model.BuildPlanDockerfile, TargetStage: "changed"}); err != nil {
				t.Fatal(err)
			}
			frozen, err := parseWorkflowSnapshot(run)
			if err != nil || frozen.BuildPlans["image"].TargetStage != tc.want || frozen.BuildPlans["image"].Platform != tc.wantPlatform {
				t.Fatal("模板修改改变了已有运行快照", err)
			}
		}
	}
}

func TestWorkflowBuildTargetOverrideRejectsListsAndKeepsExplicitEmptyJSON(t *testing.T) {
	for _, value := range []string{"web,api", "web api", "web\napi", strings.Repeat("a", 129)} {
		node := model.WorkflowNode{ID: "image", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: "plan", TargetStage: &value}}
		if err := sanitizeWorkflowNode(&node); !errors.Is(err, ErrInvalidBuildTarget) {
			t.Fatalf("无效任务覆盖未拒绝: %q %v", value, err)
		}
	}
	empty := ""
	encoded, err := json.Marshal(model.WorkflowNodeConfig{BuildPlanID: "plan", TargetStage: &empty})
	if err != nil || !strings.Contains(string(encoded), `"target_stage":""`) {
		t.Fatal("明确清空被序列化省略", err)
	}
	var decoded model.WorkflowNodeConfig
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.TargetStage == nil || *decoded.TargetStage != "" {
		t.Fatal("明确清空未能回读", err)
	}
}

func TestBuildPlatformOnlyAllowsSingleSupportedArchitecture(t *testing.T) {
	for _, value := range []string{"", "linux/amd64", "linux/arm64"} {
		plan, err := normalizeBuildPlanInput(BuildPlanInput{Name: "构建平台", Kind: model.BuildPlanDockerfile, Platform: value})
		if err != nil || plan.Platform == "" || (value != "" && plan.Platform != value) {
			t.Fatalf("有效平台未保存: %q %+v %v", value, plan, err)
		}
	}
	for _, value := range []string{"linux/386", "arm64", "linux/amd64,linux/arm64", "linux/arm/v7"} {
		if _, err := normalizeBuildPlanInput(BuildPlanInput{Name: "构建平台", Kind: model.BuildPlanDockerfile, Platform: value}); !errors.Is(err, ErrInvalidBuildPlatform) {
			t.Fatalf("不支持的平台未拒绝: %q %v", value, err)
		}
		node := model.WorkflowNode{ID: "image", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlatform: value}}
		if err := sanitizeWorkflowNode(&node); !errors.Is(err, ErrInvalidBuildPlatform) {
			t.Fatalf("无效任务平台未拒绝: %q %v", value, err)
		}
	}
	script, err := normalizeBuildPlanInput(BuildPlanInput{Name: "脚本构建", Kind: model.BuildPlanScript, Script: "echo ok", Platform: "invalid"})
	if err != nil || script.Platform != "" {
		t.Fatal("隐藏镜像平台影响脚本构建", err)
	}
}
