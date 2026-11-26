package projecttransfer

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

func TestApplicationPreviewDetectsFullBundleAndSharedConsumers(t *testing.T) {
	db := testutil.NewDatabase(t)
	svc := New(db, nil)
	source, target := database.DefaultProjectID(database.DefaultDepartmentID), uuid.NewString()
	repo := model.GitRepository{ID: uuid.NewString(), ProjectID: source, Name: "应用仓库", IsActive: true}
	registry := model.ImageRegistry{ID: uuid.NewString(), ProjectID: source, Name: "构建镜像仓库"}
	build := model.BuildPlan{ID: uuid.NewString(), ProjectID: source, Name: "构建方案", ImageRegistryID: registry.ID}
	app := model.Application{ID: uuid.NewString(), ProjectID: source, Name: "移动应用", RepositoryID: repo.ID}
	wf := model.ReleaseWorkflow{ID: uuid.NewString(), ProjectID: source, Name: "发布流程", ApplicationID: app.ID,
		Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: build.ID}}}
	variable := model.DeliveryVariable{ID: uuid.NewString(), ProjectID: source, ApplicationID: app.ID, Name: "CONFIG", Kind: "env", Usage: "build"}
	variable.ScopeKey = model.DeliveryVariableScopeKey(source, app.ID, "", variable.Usage, variable.Name)
	plan := model.ReleasePlan{ID: uuid.NewString(), ProjectID: source, Version: "one", Name: "单应用计划"}
	group := model.ReleaseGroup{ID: uuid.NewString(), ReleasePlanID: plan.ID, Name: "发布"}
	groupApp := model.ReleaseGroupApplication{ID: uuid.NewString(), ReleaseGroupID: group.ID, ApplicationID: app.ID}
	for _, row := range []any{
		&model.Project{ID: target, DepartmentID: database.DefaultDepartmentID, Name: "目标", IsActive: true},
		&repo, &registry, &build, &app, &wf, &variable, &plan, &group, &groupApp,
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: source, DepartmentID: database.DefaultDepartmentID, UserID: uuid.NewString()})
	input := Input{TargetProjectID: target, Resources: []Resource{{Kind: "application", ID: app.ID}}, IncludeDependencies: true,
		Authorize: func(*gorm.DB) error { return nil }, Audit: func(*gorm.DB, Plan) error { return nil }}
	preview, err := svc.Preview(ctx, source, input)
	if err != nil || len(preview.Blockers) != 0 || len(preview.Resources) != 7 || preview.Confirmation == "" {
		t.Fatalf("预览应自动列出完整应用关联: %+v err=%v", preview, err)
	}
	for _, item := range preview.Resources {
		if item.Automatic != (item.Kind != "application") {
			t.Fatalf("关联资源应明确标注自动检测: %+v", item)
		}
	}
	var stored model.Application
	if err := db.First(&stored, "uuid = ?", app.ID).Error; err != nil || stored.ProjectID != source {
		t.Fatalf("预览不得移动应用: %v", err)
	}
	input.Confirmation = preview.Confirmation
	other := model.Application{ID: uuid.NewString(), ProjectID: source, Name: "共享仓库的其他应用", RepositoryID: repo.ID}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Move(ctx, source, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("确认前新增共享引用不能让其他应用断开仓库: %v", err)
	}
	blocked, err := svc.Preview(ctx, source, input)
	if err != nil || blocked.Confirmation != "" {
		t.Fatalf("存在共享引用不能生成执行确认: %v", err)
	}
	found := false
	for _, blocker := range blocked.Blockers {
		if blocker.ID == other.ID && blocker.Name == other.Name {
			found = true
		}
	}
	if !found {
		t.Fatalf("应列出具体的共享应用: %+v", blocked.Blockers)
	}
	if err := db.Unscoped().Delete(&other).Error; err != nil {
		t.Fatal(err)
	}
	execution := model.ReleasePlanExecution{ID: uuid.NewString(), ProjectID: source, ReleasePlanID: plan.ID, RequestID: "mixed-history", Status: model.ReleasePlanExecutionSucceeded}
	item := model.ReleasePlanExecutionItem{ID: uuid.NewString(), ReleasePlanExecutionID: execution.ID, ReleaseGroupApplicationID: uuid.NewString(), ApplicationID: uuid.NewString(), PipelineRunID: uuid.NewString()}
	// 模拟原计划曾经包含其他应用；历史 ID 不能因当前计划只有一个应用就被隐式转移。
	if err := db.Create(&execution).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	blocked, err = svc.Preview(ctx, source, input)
	if err != nil || blocked.Confirmation != "" {
		t.Fatalf("混合应用历史不能直接迁移: %v", err)
	}
	found = false
	for _, blocker := range blocked.Blockers {
		if blocker.ID == plan.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("应列出混合历史的发布计划: %+v", blocked.Blockers)
	}
}
