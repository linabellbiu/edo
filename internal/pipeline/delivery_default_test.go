package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
)

func deliveryTestContext() context.Context {
	return database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: database.DefaultProjectID(database.DefaultDepartmentID), DepartmentID: database.DefaultDepartmentID, UserID: "admin"})
}

func TestDeliveryDefaultsIsolationAndOptimisticUpdate(t *testing.T) {
	service, db, _, _ := newPipelineTestService(t)
	ctx := deliveryTestContext()
	rows, err := service.GetDeliveryDefaults(ctx)
	if err != nil || len(rows) != 4 {
		t.Fatalf("默认项不完整: %+v %v", rows, err)
	}
	var count int64
	db.Model(&model.DeliveryDefault{}).Count(&count)
	if count != 0 {
		t.Fatal("读取内置默认不应写数据库")
	}
	plan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{Name: "通用 Docker", Kind: model.DeploymentPlanDocker, Reusable: true, TimeoutSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if plan.DeploymentTargetID != "" {
		t.Fatal("可复用方案不应生成目标")
	}
	input := DeliveryDefaultInput{DeploymentPlanID: plan.ID}
	if err := service.SetDeliveryDefault(ctx, "admin", model.DeploymentPlanDocker, input); err != nil {
		t.Fatal(err)
	}
	if err := service.SetDeliveryDefault(ctx, "admin", model.DeploymentPlanDocker, input); !errors.Is(err, ErrDeliveryDefaultConflict) {
		t.Fatalf("过期版本应拒绝: %v", err)
	}
	if err := service.SetDeliveryDefault(ctx, "admin", model.DeploymentPlanKubernetes, input); !errors.Is(err, ErrInvalidDeliveryDefault) {
		t.Fatalf("错误部署类型应拒绝: %v", err)
	}
	otherProject := model.Project{ID: "another-delivery-project", DepartmentID: database.DefaultDepartmentID, Name: "另一项目", IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&otherProject).Error; err != nil {
		t.Fatal(err)
	}
	otherCtx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: otherProject.ID, DepartmentID: database.DefaultDepartmentID, UserID: "admin"})
	rows, err = service.GetDeliveryDefaults(otherCtx)
	if err != nil || rows[0].Version != 0 || rows[0].DeploymentPlanID != "" {
		t.Fatalf("默认项跨项目泄漏: %+v %v", rows, err)
	}
	if err := service.SetDeliveryDefault(otherCtx, "admin", model.DeploymentPlanDocker, input); !errors.Is(err, ErrDeliveryDefaultUnavailable) {
		t.Fatalf("不应引用其他项目方案: %v", err)
	}
	if err := service.SetDeploymentPlanActive(ctx, plan.ID, false); err != nil {
		t.Fatal(err)
	}
	rows, err = service.GetDeliveryDefaults(ctx)
	if err != nil || rows[0].DeploymentAvailable {
		t.Fatalf("停用的默认方案应明确不可用: %+v %v", rows, err)
	}
}

func TestDeliveryWorkflowFourKindsCreateIndependentDrafts(t *testing.T) {
	for _, kind := range deliveryKinds {
		t.Run(string(kind), func(t *testing.T) {
			service, db, _, repositoryID := newPipelineTestService(t)
			ctx := deliveryTestContext()
			buildInput := BuildPlanInput{Name: "构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile"}
			if kind == model.DeploymentPlanScript {
				buildInput.Kind = model.BuildPlanScript
				buildInput.Script = "printf 'app' > app.tar"
				buildInput.ArtifactPath = "app.tar"
			}
			if kind == model.DeploymentPlanKubernetes {
				registry := model.ImageRegistry{ID: "delivery-registry", Name: "发布镜像仓库", Provider: model.RegistryGeneric, Endpoint: "registry.example.com", Namespace: "apps", IsActive: true}
				if err := db.WithContext(ctx).Create(&registry).Error; err != nil {
					t.Fatal(err)
				}
				buildInput.ImageRegistryID = registry.ID
			}
			build, err := service.CreateBuildPlan(ctx, "admin", buildInput)
			if err != nil {
				t.Fatal(err)
			}
			target := model.DeploymentTarget{Platform: model.DeploymentDocker, RuntimeID: "local", Name: "测试位置", RolloutTimeout: 600}
			if kind == model.DeploymentPlanKubernetes {
				target.Platform = model.DeploymentKubernetes
				target.RuntimeID = "delivery-kube"
				target.Namespace = "test"
				target.WorkloadName = "api"
				target.ContainerName = "api"
			}
			if kind == model.DeploymentPlanScript {
				target.Platform = model.DeploymentSSH
				target.RuntimeID = ""
				target.EnvironmentID = "script-env"
				target.HostID = "script-host"
			}
			targetInput := deploymentPlanTargetInput(t, service, target)
			firstPlanID, firstTargetID := "", ""
			for _, name := range []string{"delivery_first", "delivery_second"} {
				app, err := service.CreateApplication(ctx, "admin", ApplicationInput{Name: name, RepositoryID: repositoryID, EmptyWorkflow: true})
				if err != nil {
					t.Fatal(err)
				}
				if len(app.Workflows) != 0 {
					t.Fatal("配置发布流程前不应生成含糊流水线")
				}
				result, err := service.CreateDeliveryWorkflow(ctx, app.ID, "admin", DeliveryWorkflowInput{Kind: kind, BuildPlanID: build.ID, Target: *targetInput})
				if err != nil {
					t.Fatal(err)
				}
				if result.Workflow.IsActive || result.Workflow.WorkflowTemplateID != "" || result.Workflow.ApplicationID != app.ID {
					t.Fatalf("应该创建独立草稿: %+v", result.Workflow)
				}
				if kind != model.DeploymentPlanScript && !result.Valid {
					t.Fatalf("创建的流程无效: %+v", result.Issues)
				}
				if kind == model.DeploymentPlanScript && result.Valid {
					t.Fatal("示例脚本不能直接启用")
				}
				tasks := workflowTasks(result.Workflow.Stages)
				if len(tasks) != 2 || tasks[0].Config.BuildPlanID != build.ID {
					t.Fatalf("构建未绑定: %+v", tasks)
				}
				plan, err := deliveryTestPlan(t, service, ctx, tasks[1].Config.DeploymentPlanID)
				if err != nil {
					t.Fatal(err)
				}
				if plan.Kind != kind || plan.DeploymentTargetID == "" || plan.DeploymentTargetID == firstTargetID || plan.ID == firstPlanID {
					t.Fatalf("应用应拥有独立方案和目标: %+v", plan)
				}
				firstPlanID, firstTargetID = plan.ID, plan.DeploymentTargetID
				if kind != model.DeploymentPlanScript {
					saved, err := service.SaveApplicationWorkflow(ctx, app.ID, result.Workflow.ID, "admin", WorkflowInput{Name: result.Workflow.Name, Revision: result.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: result.Workflow.Source, Stages: result.Workflow.Stages, Activate: true})
					if err != nil {
						t.Fatal(err)
					}
					app, err = service.FindApplication(ctx, app.ID)
					if err != nil {
						t.Fatal(err)
					}
					run, err := service.newResolvedWorkflowRun(ctx, app, saved.Workflow, saved.Workflow.Source, "manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", time.Now())
					if err != nil {
						t.Fatal(err)
					}
					snap, err := parseWorkflowSnapshot(run)
					if err != nil {
						t.Fatal(err)
					}
					if snap.DeploymentPlans["deploy"].ID != plan.ID || snap.DeploymentTargets["deploy"].ID != plan.DeploymentTargetID {
						t.Fatal("运行未冻结独立部署配置")
					}
				}
			}
			for _, value := range []any{&model.PipelineRun{}, &model.DeploymentRecord{}, &model.Job{}} {
				var count int64
				if err := db.Model(value).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("配置流程不应触发运行: %T %d %v", value, count, err)
				}
			}
		})
	}
}

func TestDeliveryCustomDefaultCopiesRulesWithoutOriginalTarget(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := deliveryTestContext()
	build, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{Name: "构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile"})
	if err != nil {
		t.Fatal(err)
	}
	originalTarget := deploymentPlanTargetInput(t, service, model.DeploymentTarget{Name: "旧应用位置", Platform: model.DeploymentDocker, RuntimeID: "local", WorkloadName: "old_app", RolloutTimeout: 300})
	original, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{Name: "自定义 Docker 规则", Kind: model.DeploymentPlanDocker, DeploymentTarget: originalTarget, TimeoutSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := BuiltinDelivery(model.DeploymentPlanDocker)
	definition.Stages[0].Tasks[0].Config.BuildPlanID = build.ID
	definition.Stages[1].Tasks[0].Config.DeploymentPlanID = original.ID
	template, err := service.CreateWorkflowTemplate(ctx, "admin", WorkflowTemplateInput{WorkflowInput: WorkflowInput{Name: "自定义发布流程", SchemaVersion: model.WorkflowSchemaVersion, Source: definition.Source, Stages: definition.Stages, Activate: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetDeliveryDefault(ctx, "admin", model.DeploymentPlanDocker, DeliveryDefaultInput{WorkflowTemplateID: template.WorkflowTemplate.ID, DeploymentPlanID: original.ID}); err != nil {
		t.Fatal(err)
	}
	app, err := service.CreateApplication(ctx, "admin", ApplicationInput{Name: "default_copy_app", RepositoryID: repositoryID, EmptyWorkflow: true})
	if err != nil {
		t.Fatal(err)
	}
	nextTarget := *originalTarget
	nextTarget.WorkloadName = ""
	result, err := service.CreateDeliveryWorkflow(ctx, app.ID, "admin", DeliveryWorkflowInput{Kind: model.DeploymentPlanDocker, BuildPlanID: build.ID, Target: nextTarget, ExpectedDefaultVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	planID := workflowTasks(result.Workflow.Stages)[1].Config.DeploymentPlanID
	plan, err := deliveryTestPlan(t, service, ctx, planID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.DeploymentTargetID == original.DeploymentTargetID || plan.DeploymentTarget.WorkloadName != "" || plan.TimeoutSeconds != 300 || result.Workflow.Name != "自定义发布流程" {
		t.Fatalf("默认规则复制了旧目标或丢失自定义配置: %+v", plan)
	}
	if err := service.SetDeliveryDefault(ctx, "admin", model.DeploymentPlanDocker, DeliveryDefaultInput{ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := service.GetApplicationWorkflow(ctx, app.ID, result.Workflow.ID)
	if err != nil || workflowTasks(unchanged.Workflow.Stages)[1].Config.DeploymentPlanID != planID || unchanged.Workflow.Revision != 1 {
		t.Fatal("切换默认改写了已有流水线")
	}
	if _, err := service.CreateDeliveryWorkflow(ctx, app.ID, "admin", DeliveryWorkflowInput{Kind: model.DeploymentPlanDocker, BuildPlanID: build.ID, Target: nextTarget, ExpectedDefaultVersion: 1}); !errors.Is(err, ErrDeliveryDefaultConflict) {
		t.Fatalf("旧默认版本应拒绝: %v", err)
	}
	var before, after int64
	db.Model(&model.DeploymentPlan{}).Count(&before)
	nextTarget.EnvironmentID = "missing"
	if _, err := service.CreateDeliveryWorkflow(ctx, app.ID, "admin", DeliveryWorkflowInput{Kind: model.DeploymentPlanDocker, BuildPlanID: build.ID, Target: nextTarget, ExpectedDefaultVersion: 2}); err == nil {
		t.Fatal("无效目标应拒绝")
	}
	db.Model(&model.DeploymentPlan{}).Count(&after)
	if before != after {
		t.Fatal("失败创建残留部署方案")
	}
}

func deliveryTestPlan(t *testing.T, s *Service, ctx context.Context, id string) (*model.DeploymentPlan, error) {
	t.Helper()
	var plan model.DeploymentPlan
	err := s.db.WithContext(ctx).Preload("DeploymentTarget").First(&plan, "uuid = ?", id).Error
	return &plan, err
}

func TestReusableDeploymentPlanCanActivatePublicTemplateButNotApplication(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := deliveryTestContext()
	build, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{Name: "构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{Name: "通用规则", Kind: model.DeploymentPlanDocker, Reusable: true, TimeoutSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := BuiltinDelivery(model.DeploymentPlanDocker)
	definition.Stages[0].Tasks[0].Config.BuildPlanID = build.ID
	definition.Stages[1].Tasks[0].Config.DeploymentPlanID = plan.ID
	template, err := service.CreateWorkflowTemplate(ctx, "admin", WorkflowTemplateInput{WorkflowInput: WorkflowInput{Name: "通用发布编排", SchemaVersion: model.WorkflowSchemaVersion, Source: definition.Source, Stages: definition.Stages, Activate: true}})
	if err != nil || !template.Valid {
		t.Fatalf("公共方案应允许通用规则: %+v %v", template, err)
	}
	app, err := service.CreateApplication(ctx, "admin", ApplicationInput{Name: "unbound_target", RepositoryID: repositoryID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SaveApplicationWorkflow(ctx, app.ID, app.Workflows[0].ID, "admin", WorkflowInput{Name: app.Workflows[0].Name, Revision: app.Workflows[0].Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: definition.Source, Stages: definition.Stages, Activate: true})
	if !errors.Is(err, ErrInvalidWorkflow) || result == nil || result.Valid {
		t.Fatalf("应用缺少具体运行位置时不得启用: %+v %v", result, err)
	}
}
