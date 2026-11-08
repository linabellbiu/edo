package database

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"edo/internal/config"
	"edo/internal/model"
	"github.com/google/uuid"
)

func TestDeploymentExecutionMigration(t *testing.T) {
	for _, target := range []struct{ driver, env string }{{"sqlite", ""}, {"postgres", "EDO_TEST_POSTGRES_DSN"}, {"mysql", "EDO_TEST_MYSQL_DSN"}} {
		t.Run(target.driver, func(t *testing.T) {
			selected := os.Getenv("EDO_TEST_DATABASE_DRIVER")
			if selected != "" && selected != target.driver {
				t.Skip("当前任务验证其他数据库")
			}
			dsn := os.Getenv(target.env)
			if target.driver == "sqlite" {
				dsn = filepath.Join(t.TempDir(), "execution.db")
			} else if dsn == "" {
				if selected == target.driver {
					t.Fatal("已选择真实数据库测试，但连接配置缺失")
				}
				t.Skip("未配置独立迁移测试数据库")
			}
			ctx := context.Background()
			db, err := Open(ctx, config.Database{Driver: target.driver, DSN: dsn, MaxOpenConns: 2, MaxIdleConns: 1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = Close(db) })
			if err = Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			old := model.DeploymentPlan{ID: uuid.NewString(), Name: "执行配置升级-" + uuid.NewString(), Kind: model.DeploymentPlanDocker, TimeoutSeconds: 600, CreatedBy: "admin"}
			if err = db.Create(&old).Error; err != nil {
				t.Fatal(err)
			}
			if err = db.Migrator().DropColumn(&model.DeploymentPlan{}, "ExecutionConfig"); err != nil {
				t.Fatal(err)
			}
			if err = db.Where("version = ?", "202611080077").Delete(&model.SchemaMigration{}).Error; err != nil {
				t.Fatal(err)
			}
			if err = Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			var retained model.DeploymentPlan
			if err = db.First(&retained, "uuid = ?", old.ID).Error; err != nil || retained.RowID != old.RowID || retained.Name != old.Name || retained.ExecutionConfig != nil {
				t.Fatal("升级损坏旧方案或隐式接管任务参数", err)
			}
			retained.ExecutionConfig = &model.DeploymentExecutionConfig{DeploymentInputVersion: 1, DeploymentScript: "printf upgraded", VariableNames: []string{"SETTING"}}
			if err = db.Save(&retained).Error; err != nil {
				t.Fatal(err)
			}
			var saved model.DeploymentPlan
			if err = db.First(&saved, "uuid = ?", old.ID).Error; err != nil || saved.ExecutionConfig == nil || saved.ExecutionConfig.DeploymentScript != "printf upgraded" {
				t.Fatal("升级后不能继续保存执行配置", err)
			}
			next := model.DeploymentPlan{ID: uuid.NewString(), Name: "新方案-" + uuid.NewString(), Kind: model.DeploymentPlanDocker, TimeoutSeconds: 600, ExecutionConfig: &model.DeploymentExecutionConfig{}, CreatedBy: "admin"}
			if err = db.Create(&next).Error; err != nil || next.RowID <= old.RowID {
				t.Fatal("升级后不能继续创建方案", err)
			}
			var cleared model.DeploymentPlan
			if err = db.First(&cleared, "uuid = ?", next.ID).Error; err != nil || cleared.ExecutionConfig == nil {
				t.Fatal("空对象与未迁移方案不能区分", err)
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
