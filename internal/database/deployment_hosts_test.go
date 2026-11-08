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

func TestDeploymentHostSelectionMigration(t *testing.T) {
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
			old := model.DeploymentTarget{ID: uuid.NewString(), Name: "旧部署目标-" + uuid.NewString(), Platform: model.DeploymentSSH, HostID: "old-host", EnvironmentID: "old-group", CreatedBy: "admin"}
			if err = db.Create(&old).Error; err != nil {
				t.Fatal(err)
			}
			if err = db.Migrator().DropColumn(&model.DeploymentTarget{}, "HostIDs"); err != nil {
				t.Fatal(err)
			}
			if err = db.Where("version = ?", "202611080079").Delete(&model.SchemaMigration{}).Error; err != nil {
				t.Fatal(err)
			}
			if err = Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			var retained model.DeploymentTarget
			if err = db.First(&retained, "uuid = ?", old.ID).Error; err != nil || retained.HostID != old.HostID || retained.RowID != old.RowID || retained.HostIDs != nil {
				t.Fatalf("升级不应改写旧单主机: %+v %v", retained, err)
			}
			retained.HostIDs = []string{"old-host", "another-host"}
			if err = db.Save(&retained).Error; err != nil {
				t.Fatal(err)
			}
			var updated model.DeploymentTarget
			if err = db.First(&updated, "uuid = ?", old.ID).Error; err != nil || len(updated.HostIDs) != 2 || updated.HostIDs[1] != "another-host" {
				t.Fatalf("升级后不能保存主机集合: %+v %v", updated, err)
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
