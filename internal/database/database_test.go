package database

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"zrt/internal/config"
	"zrt/internal/model"
)

func TestSQLiteMigrationIsIdempotent(t *testing.T) {
	cfg := config.Database{
		Driver:          "sqlite",
		DSN:             "file::memory:?cache=shared",
		MaxOpenConns:    1,
		MaxIdleConns:    1,
		ConnMaxLifetime: time.Minute,
	}
	db, err := Open(context.Background(), cfg, slog.Default())
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	defer Close(db)

	for range 2 {
		if err := Migrate(context.Background(), db); err != nil {
			t.Fatalf("执行迁移失败: %v", err)
		}
	}
	if !db.Migrator().HasTable(&model.Job{}) || !db.Migrator().HasTable(&model.OutboxEvent{}) ||
		!db.Migrator().HasTable(&model.User{}) || !db.Migrator().HasTable(&model.Role{}) ||
		!db.Migrator().HasTable(&model.AuditLog{}) || !db.Migrator().HasTable(&model.GitRepository{}) ||
		!db.Migrator().HasTable(&model.DockerEndpoint{}) || !db.Migrator().HasTable(&model.KubernetesCluster{}) ||
		!db.Migrator().HasTable(&model.DeploymentTarget{}) || !db.Migrator().HasTable(&model.DeploymentRecord{}) ||
		!db.Migrator().HasTable(&model.Configuration{}) || !db.Migrator().HasTable(&model.ConfigurationRevision{}) ||
		!db.Migrator().HasTable(&model.NotificationChannel{}) || !db.Migrator().HasTable(&model.Notification{}) ||
		!db.Migrator().HasTable(&model.ScheduledTask{}) || !db.Migrator().HasTable(&model.MonitorRule{}) ||
		!db.Migrator().HasTable(&model.MonitorCheck{}) || !db.Migrator().HasTable(&model.Application{}) ||
		!db.Migrator().HasTable(&model.BuildPlan{}) || !db.Migrator().HasTable(&model.ImageRegistry{}) ||
		!db.Migrator().HasTable(&model.ReleasePlan{}) || !db.Migrator().HasTable(&model.PipelineRun{}) {
		t.Fatal("核心任务表未创建")
	}
}

func TestExternalDatabaseMigration(t *testing.T) {
	tests := []struct {
		name   string
		driver string
		env    string
	}{
		{name: "PostgreSQL", driver: "postgres", env: "ZRT_TEST_POSTGRES_DSN"},
		{name: "MySQL", driver: "mysql", env: "ZRT_TEST_MYSQL_DSN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.env)
			if dsn == "" {
				t.Skipf("未设置 %s", test.env)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			db, err := Open(ctx, config.Database{
				Driver: test.driver, DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
			}, slog.Default())
			if err != nil {
				t.Fatalf("打开 %s 失败: %v", test.name, err)
			}
			defer func() { _ = Close(db) }()
			for range 2 {
				if err := Migrate(ctx, db); err != nil {
					t.Fatalf("执行 %s 迁移失败: %v", test.name, err)
				}
			}
			if err := VerifyMigrations(ctx, db); err != nil {
				t.Fatalf("验证 %s 迁移失败: %v", test.name, err)
			}
			if !db.Migrator().HasTable(&model.User{}) || !db.Migrator().HasTable(&model.DeploymentRecord{}) ||
				!db.Migrator().HasTable(&model.MonitorCheck{}) || !db.Migrator().HasTable(&model.Application{}) ||
				!db.Migrator().HasTable(&model.PipelineRun{}) {
				t.Fatalf("%s 核心表不完整", test.name)
			}
		})
	}
}
