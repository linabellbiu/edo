package k3s

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

// 三库验证真实 JSON 路径等值和过滤后分页；Mock 不能替代数据库方言验证。
func TestUninstallHistoryHostFilterExternalDatabases(t *testing.T) {
	selected := os.Getenv("EDO_TEST_DATABASE_DRIVER")
	if selected != "" && selected != "sqlite" && selected != "postgres" && selected != "mysql" {
		t.Fatal("数据库测试驱动无效")
	}
	for _, target := range []struct{ driver, variable string }{{"sqlite", ""}, {"postgres", "EDO_TEST_POSTGRES_DSN"}, {"mysql", "EDO_TEST_MYSQL_DSN"}} {
		t.Run(target.driver, func(t *testing.T) {
			if selected != "" && selected != target.driver {
				t.Skip("当前任务验证其他数据库")
			}
			var db *gorm.DB
			if target.driver == "sqlite" {
				db = testutil.NewDatabase(t)
			} else {
				dsn := os.Getenv(target.variable)
				if dsn == "" {
					if selected == target.driver {
						t.Fatal("已选择真实数据库测试，但连接配置缺失")
					}
					t.Skip("未配置独立真实数据库连接")
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				var err error
				db, err = database.Open(ctx, config.Database{Driver: target.driver, DSN: dsn, MaxOpenConns: 2, MaxIdleConns: 2}, slog.New(slog.NewTextHandler(io.Discard, nil)))
				if err != nil {
					t.Fatal("打开真实测试数据库失败")
				}
				t.Cleanup(func() {
					if err := database.Close(db); err != nil {
						t.Error("关闭真实测试数据库失败")
					}
				})
				if !db.Migrator().HasTable(&model.K3sInstallation{}) {
					t.Fatal("请先对可丢弃数据库执行真实迁移测试")
				}
			}
			assertUninstallJSONHostFilter(t, db)
		})
	}
}

func assertUninstallJSONHostFilter(t *testing.T, db *gorm.DB) {
	t.Helper()
	project, actor, host := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: project, DepartmentID: database.DefaultDepartmentID, UserID: actor, AllDepartments: true})
	ids := []string{}
	t.Cleanup(func() {
		if err := db.WithContext(ctx).Where("uuid IN ? AND project_id = ?", ids, project).Delete(&model.K3sInstallation{}).Error; err != nil {
			t.Error("清理本用例卸载记录失败")
		}
	})
	want := ""
	for index := 0; index < 16; index++ {
		targetHost, owner, action, status := host, actor, "uninstall", "failed"
		switch {
		case index == 0:
		case index < 12:
			targetHost = uuid.NewString()
		case index == 12:
			owner = uuid.NewString()
		case index == 13:
			action = "create"
		case index == 14:
			status = "planned"
		case index == 15:
			targetHost = host + "-suffix"
		}
		preview, _ := json.Marshal(Plan{Uninstall: &UninstallSummary{HostID: targetHost}})
		id := uuid.NewString()
		ids = append(ids, id)
		if index == 0 {
			want = id
		}
		now := time.Now().UTC()
		record := model.K3sInstallation{ID: id, ProjectID: project, DepartmentID: database.DefaultDepartmentID, CreatedBy: owner, Action: action, Name: "卸载筛选夹具", Status: status, Preview: datatypes.JSON(preview), Hosts: datatypes.JSON("[]"), InputCiphertext: "fixture-only", ExpiresAt: now, CreatedAt: now, UpdatedAt: now}
		if err := db.WithContext(ctx).Create(&record).Error; err != nil {
			t.Fatal("写入卸载筛选夹具失败")
		}
	}
	var records []model.K3sInstallation
	err := db.WithContext(ctx).Where("project_id = ? AND created_by = ? AND action = ? AND status <> ?", project, actor, "uninstall", "planned").Where(datatypes.JSONQuery("preview").Equals(host, "uninstall", "host_id")).Order("id DESC").Limit(1).Find(&records).Error
	if err != nil || len(records) != 1 || records[0].ID != want {
		t.Fatalf("主机 JSON 筛选、操作者隔离或分页不符: count=%d err=%v", len(records), err)
	}
}
