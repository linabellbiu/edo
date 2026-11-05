package database

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"edo/internal/config"
	"edo/internal/model"
)

func TestApplicationEnvironmentMigration(t *testing.T) {
	selected := os.Getenv("EDO_TEST_DATABASE_DRIVER")
	if selected != "" && selected != "sqlite" && selected != "mysql" && selected != "postgres" {
		t.Fatal("数据库测试驱动无效")
	}
	for _, target := range []struct{ driver, env string }{{"sqlite", ""}, {"postgres", "EDO_TEST_POSTGRES_DSN"}, {"mysql", "EDO_TEST_MYSQL_DSN"}} {
		t.Run(target.driver, func(t *testing.T) {
			if selected != "" && selected != target.driver {
				t.Skip("当前任务验证其他数据库")
			}
			dsn := os.Getenv(target.env)
			if target.driver == "sqlite" {
				dsn = filepath.Join(t.TempDir(), "environment.db")
			} else if dsn == "" {
				if os.Getenv("EDO_TEST_DATABASE_DRIVER") == target.driver {
					t.Fatal("已选择真实数据库测试，但连接配置缺失")
				}
				t.Skip("未配置独立迁移测试数据库")
			}
			db, err := Open(context.Background(), config.Database{Driver: target.driver, DSN: dsn, MaxOpenConns: 2, MaxIdleConns: 1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = Close(db) })
			ctx := context.Background()
			if err = Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			old := model.DeliveryVariable{ID: uuid.NewString(), ScopeKey: uuid.NewString(), Name: "OLD", Kind: "env", Usage: "deploy", Version: 1, IsActive: true, ProtectedRefs: []string{}}
			if err = db.Create(&old).Error; err != nil || old.AutoDeploy {
				t.Fatal("初建或默认值不正确", err)
			}
			if err = db.Migrator().DropColumn(&model.DeliveryVariable{}, "AutoDeploy"); err != nil {
				t.Fatal(err)
			}
			if err = db.Where("version = ?", "202611050076").Delete(&model.SchemaMigration{}).Error; err != nil {
				t.Fatal(err)
			}
			if err = Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			var retained model.DeliveryVariable
			if err = db.First(&retained, "uuid = ?", old.ID).Error; err != nil || retained.RowID != old.RowID || retained.Name != old.Name || retained.AutoDeploy {
				t.Fatal("升级改写旧变量或自动启用旧配置", err)
			}
			next := model.DeliveryVariable{ID: uuid.NewString(), ScopeKey: uuid.NewString(), Name: "NEW", Kind: "env_text", Usage: "deploy", Version: 1, AutoDeploy: true, ProtectedRefs: []string{}}
			if err = db.Create(&next).Error; err != nil || next.RowID <= old.RowID {
				t.Fatal("升级后不能继续写入", err)
			}
			if err = Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err = VerifyMigrations(ctx, db); err != nil {
				t.Fatal(err)
			}
		})
	}
}
