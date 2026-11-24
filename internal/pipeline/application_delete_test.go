package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/model"
)

func TestDeleteApplicationProtectsReferencesAndRetainsHistory(t *testing.T) {
	s, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "archive_example", RepositoryID: repositoryID})
	if err != nil {
		t.Fatal(err)
	}
	run := model.PipelineRun{ID: uuid.NewString(), ApplicationID: app.ID, WorkflowID: app.Workflows[0].ID, Status: model.PipelineRunRunning}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteApplication(ctx, app.ID); !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("未结束的流水线必须阻止删除：%v", err)
	}
	if err := db.Model(&run).Update("status", model.PipelineRunFailed).Error; err != nil {
		t.Fatal(err)
	}
	plan, err := s.CreateReleasePlan(ctx, "admin", ReleasePlanInput{Description: "删除应用引用测试", Groups: defaultReleasePlanGroups([]ReleaseApplicationInput{{ApplicationID: app.ID}})})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteApplication(ctx, app.ID); !errors.Is(err, ErrApplicationInUse) {
		t.Fatalf("发布计划的引用必须阻止删除：%v", err)
	}
	if err := s.DeleteReleasePlan(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteApplication(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindApplication(ctx, app.ID); !errors.Is(err, ErrApplicationNotFound) {
		t.Fatalf("已删除应用仍可访问：%v", err)
	}
	var rows int64
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", app.ID).Count(&rows).Error; err != nil || rows != 0 {
		t.Fatalf("应用所属流水线仍可访问：count=%d err=%v", rows, err)
	}
	var workflow model.ReleaseWorkflow
	if err := db.Unscoped().First(&workflow, "uuid = ?", app.Workflows[0].ID).Error; err != nil || !workflow.DeletedAt.Valid {
		t.Fatalf("流水线应保留软删除记录：%v", err)
	}
	history, err := s.FindRun(ctx, run.ID)
	if err != nil || history.Application.Name != app.Name || history.RetryAllowed {
		t.Fatalf("历史运行应保留原应用名称并关闭重试入口：%+v, %v", history, err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return lockWorkflowApplication(tx, app.ID) }); !errors.Is(err, ErrApplicationNotFound) {
		t.Fatalf("迟到的流水线或发布计划写入必须拒绝已删除应用：%v", err)
	}
	if _, err := s.repositories.Find(ctx, repositoryID); err != nil {
		t.Fatalf("源代码仓库应继续保留：%v", err)
	}
}
