package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"edo/internal/model"
	"edo/internal/repository"
)

func TestPendingRunCannotFreezeArchivedRepository(t *testing.T) {
	s, db, _, oldID := newPipelineTestService(t)
	ctx := context.Background()
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "stale_repository", RepositoryID: oldID})
	if err != nil {
		t.Fatal(err)
	}
	newRepo, err := s.repositories.Create(ctx, "admin", repository.Input{Name: "replacement", Provider: model.GitProviderGeneric, CloneURL: "https://git.example.com/team/new.git", AuthType: model.GitAuthNone})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateApplication(ctx, app.ID, ApplicationInput{Name: app.Name, RepositoryID: newRepo.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.repositories.Delete(ctx, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.createPendingRun(ctx, app, &app.Workflows[0], "admin", "请选择代码版本"); !errors.Is(err, ErrPipelineRepositoryUnavailable) {
		t.Fatalf("旧应用快照不能创建使用已删除仓库的运行: %v", err)
	}
	var runs int64
	if err := db.Model(&model.PipelineRun{}).Where("application_id = ?", app.ID).Count(&runs).Error; err != nil || runs != 0 {
		t.Fatalf("仓库不可用时应回滚整个运行: count=%d err=%v", runs, err)
	}
}

func TestArchivedRepositoryCheckDoesNotStopOtherWritebacks(t *testing.T) {
	s, db, _, oldID := newPipelineTestService(t)
	ctx := context.Background()
	newRepo, err := s.repositories.Create(ctx, "admin", repository.Input{Name: "active-repository", Provider: model.GitProviderGeneric, CloneURL: "https://git.example.com/team/new.git", AuthType: model.GitAuthNone})
	if err != nil {
		t.Fatal(err)
	}
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "active_repository_checks", RepositoryID: newRepo.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.repositories.Delete(ctx, oldID); err != nil {
		t.Fatal(err)
	}
	// 模拟旧数据中遗留的检查状态，验证单条失效仓库不会阻塞另一条回写。
	for _, id := range []string{oldID, newRepo.ID} {
		run := model.PipelineRun{ID: uuid.NewString(), ApplicationID: app.ID, WorkflowID: app.Workflows[0].ID, Status: model.PipelineRunSucceeded, StatusWriteback: true}
		if err := db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.PipelineRunRepository{ID: uuid.NewString(), PipelineRunID: run.ID, RepositoryID: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReconcileRepositoryChecks(ctx); err != nil {
		t.Fatal(err)
	}
	var deliveries []model.RepositoryCheckDelivery
	if err := db.Order("id ASC").Find(&deliveries).Error; err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 2 || deliveries[0].RepositoryID != oldID || deliveries[0].Status != "failed" || deliveries[1].RepositoryID != newRepo.ID || deliveries[1].Attempts != 1 {
		t.Fatalf("已删除仓库的失败结果应保留，其他记录继续处理: %+v", deliveries)
	}
}
