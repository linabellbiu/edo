package database

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/model"
)

type narrowPipelineStatus struct {
	Status string `gorm:"type:varchar(16);not null;index"`
}

func (narrowPipelineStatus) TableName() string { return "pipeline_runs" }

func TestSQLitePipelineStatusUpgrade(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "status.db"), MaxOpenConns: 1, MaxIdleConns: 1,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close(db) })
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	exercisePipelineStatusUpgrade(t, db)
}

func exercisePipelineStatusUpgrade(t *testing.T, db *gorm.DB) {
	t.Helper()
	repo := model.GitRepository{ID: uuid.NewString(), Name: "状态字段验收仓库", IsActive: true}
	app := model.Application{ID: uuid.NewString(), Name: "status-upgrade", RepositoryID: repo.ID, IsActive: true}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app).Error; err != nil {
		t.Fatal(err)
	}
	run := model.PipelineRun{ID: uuid.NewString(), ApplicationID: app.ID, Status: model.PipelineRunSucceeded,
		CommitSHA: "frozen-commit", WorkflowSnapshot: `{"frozen":true}`}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Delete(&repo)
	defer db.Delete(&app)
	defer db.Delete(&run)
	// 复现已部署版本的窄字段，然后通过正式迁移升级，不借用全新建表结果。
	if err := db.Migrator().AlterColumn(&narrowPipelineStatus{}, "Status"); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version = ?", "202610240072").Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	statuses := []model.PipelineRunStatus{model.PipelineRunDetected, model.PipelineRunReady,
		model.PipelineRunBlocked, model.PipelineRunAwaitingApproval, model.PipelineRunRunning,
		model.PipelineRunSucceeded, model.PipelineRunFailed, model.PipelineRunSkipped, model.PipelineRunCanceled}
	for _, status := range statuses {
		if err := db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("status", status).Error; err != nil {
			t.Fatalf("状态 %s 无法写入升级后的字段: %v", status, err)
		}
		var stored model.PipelineRun
		if err := db.First(&stored, "uuid = ?", run.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Status != status || stored.ID != run.ID || stored.RowID != run.RowID ||
			stored.ApplicationID != app.ID || stored.CommitSHA != run.CommitSHA || stored.WorkflowSnapshot != run.WorkflowSnapshot {
			t.Fatalf("状态字段升级改变运行数据: status=%s run=%+v", status, stored)
		}
	}
}
