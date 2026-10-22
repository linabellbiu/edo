package deployment

import (
	"context"
	"errors"
	"testing"
	"time"

	"edo/internal/model"
)

func TestCurrentDeploymentRejectsReplacedAndDeletedInstances(t *testing.T) {
	s, db, _ := newDeploymentTestService(t)
	now := time.Now().UTC()
	old := model.DeploymentRecord{
		ID: "current-a", ApplicationID: "app", PipelineRunID: "run", DeploymentPlanID: "plan",
		TargetID: "target", Platform: model.DeploymentSSH, DeploymentPlanKind: model.DeploymentPlanScript,
		Status: model.DeploymentSucceeded, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.RuntimeState(context.Background(), old.ID); err != nil {
		t.Fatalf("当前记录应可读取: %v", err)
	}
	latest := old
	latest.ID = "current-b"
	// 完全相同创建时间也必须有稳定的选择顺序。
	if err := db.Create(&latest).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.RuntimeState(context.Background(), old.ID); !errors.Is(err, ErrDeploymentNotCurrent) {
		t.Fatalf("旧记录不应继续读取现有资源状态: %v", err)
	}
	if _, err := s.RuntimeState(context.Background(), latest.ID); err != nil {
		t.Fatalf("最新记录应可读取: %v", err)
	}
	if err := db.Model(&latest).Update("runtime_deleted_at", now).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.RuntimeState(context.Background(), latest.ID); !errors.Is(err, ErrRuntimeInstanceRemoved) {
		t.Fatalf("已删除实例应退出运行监控: %v", err)
	}
	if _, err := s.RuntimeState(context.Background(), old.ID); !errors.Is(err, ErrDeploymentNotCurrent) {
		t.Fatalf("删除最新实例后不能重新启用旧记录: %v", err)
	}
}

func TestCurrentDeploymentOrdersLegacyAndModernRecordsTogether(t *testing.T) {
	s, db, _ := newDeploymentTestService(t)
	now := time.Now().UTC()
	if err := db.Create(&model.GitRepository{
		ID: "repo-current", Name: "current", CloneURL: "https://example.invalid/current.git",
		Provider: model.GitProviderGeneric, AuthType: model.GitAuthNone, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Application{
		ID: "app", Name: "current_app", RepositoryID: "repo-current", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	run := model.PipelineRun{ID: "legacy-run", ApplicationID: "app", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	modern := model.DeploymentRecord{
		ID: "modern", ApplicationID: "app", PipelineRunID: run.ID, DeploymentPlanID: "plan",
		TargetID: "target", Platform: model.DeploymentSSH, DeploymentPlanKind: model.DeploymentPlanScript,
		Status: model.DeploymentSucceeded, CreatedAt: now, UpdatedAt: now,
	}
	legacy := modern
	legacy.ID, legacy.ApplicationID, legacy.CreatedAt = "legacy", "", now.Add(time.Second)
	if err := db.Create(&[]model.DeploymentRecord{modern, legacy}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.RuntimeState(context.Background(), modern.ID); !errors.Is(err, ErrDeploymentNotCurrent) {
		t.Fatalf("存在更晚旧格式记录时不可选中现代格式的旧记录: %v", err)
	}
	if _, err := s.RuntimeState(context.Background(), legacy.ID); err != nil {
		t.Fatalf("最新的旧格式记录应按运行外键识别: %v", err)
	}
}
