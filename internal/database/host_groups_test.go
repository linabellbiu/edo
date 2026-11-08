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

func TestHostGroupMigration(t *testing.T) {
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
			old := model.Environment{ID: uuid.NewString(), Name: "主机组升级-" + uuid.NewString(), CreatedBy: "admin", IsActive: true}
			if err = db.Create(&old).Error; err != nil {
				t.Fatal(err)
			}
			host := model.Host{ID: uuid.NewString(), Name: "升级主机-" + uuid.NewString(), Mode: model.HostModeLocal, CreatedBy: "admin"}
			if err = db.Create(&host).Error; err != nil {
				t.Fatal(err)
			}
			membership := model.EnvironmentHost{EnvironmentID: old.ID, HostID: host.ID}
			if err = db.Create(&membership).Error; err != nil {
				t.Fatal(err)
			}
			if err = db.Migrator().DropIndex(&model.Environment{}, "ux_host_groups_name"); err != nil {
				t.Fatal(err)
			}
			if err = db.Migrator().DropIndex(&model.Environment{}, "idx_environments_parent_id"); err != nil {
				t.Fatal(err)
			}
			if err = db.Migrator().DropColumn(&model.Environment{}, "ParentID"); err != nil {
				t.Fatal(err)
			}
			if err = db.Where("version = ?", "202611080078").Delete(&model.SchemaMigration{}).Error; err != nil {
				t.Fatal(err)
			}
			if err = Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			var retained model.Environment
			if err = db.First(&retained, "uuid = ?", old.ID).Error; err != nil || retained.RowID != old.RowID || retained.Name != old.Name || retained.ParentID != "" {
				t.Fatalf("升级损坏旧主机组: %+v %v", retained, err)
			}
			var link model.EnvironmentHost
			if err = db.First(&link, "id = ?", membership.RowID).Error; err != nil || link.EnvironmentID != old.ID || link.HostID != host.ID {
				t.Fatal("升级丢失主机关联", err)
			}
			next := model.Environment{ID: uuid.NewString(), Name: "下级-" + uuid.NewString(), ParentID: old.ID, CreatedBy: "admin"}
			if err = db.Create(&next).Error; err != nil || next.RowID <= old.RowID {
				t.Fatal("升级后不能创建下级", err)
			}
			next.Name += "-改名"
			if err = db.Save(&next).Error; err != nil {
				t.Fatal("升级后不能修改", err)
			}
			duplicate := model.Environment{ID: uuid.NewString(), Name: old.Name, DepartmentID: uuid.NewString(), ProjectID: uuid.NewString(), CreatedBy: "admin"}
			if err = db.Create(&duplicate).Error; err == nil {
				t.Fatal("名称未实现全局唯一")
			}
			if err = Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err = VerifyMigrations(ctx, db); err != nil {
				t.Fatal(err)
			}
			// 旧数据重名时不改名、不删除记录；修正准确名称后可以重新升级。
			if err = db.Migrator().DropIndex(&model.Environment{}, "ux_host_groups_name"); err != nil {
				t.Fatal(err)
			}
			duplicate.ID = uuid.NewString()
			if err = db.Create(&duplicate).Error; err != nil {
				t.Fatal(err)
			}
			if err = migrateHostGroups(db); err == nil {
				t.Fatal("重名旧数据应阻断迁移")
			}
			var unchanged model.Environment
			if err = db.First(&unchanged, "uuid = ?", duplicate.ID).Error; err != nil || unchanged.Name != old.Name {
				t.Fatal("迁移不能隐式改名或删记录", err)
			}
			if err = db.Model(&unchanged).Update("name", "调整后-"+uuid.NewString()).Error; err != nil {
				t.Fatal(err)
			}
			if err = migrateHostGroups(db); err != nil {
				t.Fatal("修正重名后应能恢复升级", err)
			}
		})
	}
}
