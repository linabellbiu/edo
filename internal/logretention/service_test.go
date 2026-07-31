package logretention

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"testing"
	"time"

	"gorm.io/datatypes"

	"edo/internal/config"
	"edo/internal/configuration"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
)

func TestCleanupUsesIndependentRetentionWindows(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:log-retention?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	manager, err := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	settings := configuration.NewService(db, manager)
	if _, err := settings.UpdateLogRetentionSettings(ctx, "admin", true, 30, 180, 0); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	logs := []model.PipelineRunLog{
		{PipelineRunID: "old", Level: "info", Message: "old", CreatedAt: now.AddDate(0, 0, -31)},
		{PipelineRunID: "new", Level: "info", Message: "new", CreatedAt: now.AddDate(0, 0, -2)},
	}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	audits := []model.AuditLog{
		{ID: "old", Action: "test", ResourceType: "test", Result: model.AuditSucceeded, Metadata: datatypes.JSON(`{}`), CreatedAt: now.AddDate(0, 0, -181)},
		{ID: "new", Action: "test", ResourceType: "test", Result: model.AuditSucceeded, Metadata: datatypes.JSON(`{}`), CreatedAt: now.AddDate(0, 0, -2)},
	}
	if err := db.Create(&audits).Error; err != nil {
		t.Fatal(err)
	}

	report, err := NewService(db, settings, logger).Cleanup(ctx)
	if err != nil {
		t.Fatalf("清理日志失败: %v", err)
	}
	if report.PipelineLogsDeleted != 1 || report.AuditLogsDeleted != 1 {
		t.Fatalf("清理数量错误: %+v", report)
	}
	var pipelineCount, auditCount int64
	if err := db.Model(&model.PipelineRunLog{}).Count(&pipelineCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AuditLog{}).Count(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if pipelineCount != 1 || auditCount != 1 {
		t.Fatalf("保留窗口内的日志被误删: pipeline=%d audit=%d", pipelineCount, auditCount)
	}
}
