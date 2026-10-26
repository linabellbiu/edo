package department

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
)

func TestDepartmentQueriesExternalDatabases(t *testing.T) {
	selected := os.Getenv("EDO_TEST_DATABASE_DRIVER")
	if selected != "" && selected != "sqlite" && selected != "mysql" && selected != "postgres" {
		t.Fatal("数据库测试驱动无效")
	}
	for _, target := range []struct{ driver, variable string }{
		{"postgres", "EDO_TEST_POSTGRES_DSN"}, {"mysql", "EDO_TEST_MYSQL_DSN"},
	} {
		t.Run(target.driver, func(t *testing.T) {
			if selected != "" && selected != target.driver {
				t.Skip("当前任务验证其他数据库")
			}
			dsn := os.Getenv(target.variable)
			if dsn == "" {
				if selected == target.driver {
					t.Fatal("已选择真实数据库测试，但连接配置缺失")
				}
				t.Skip("未配置独立的真实数据库测试连接")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			db, err := database.Open(ctx, config.Database{Driver: target.driver, DSN: dsn, MaxOpenConns: 2, MaxIdleConns: 2}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal("打开真实测试数据库失败")
			}
			t.Cleanup(func() {
				if err := database.Close(db); err != nil {
					t.Errorf("关闭测试数据库失败: %v", err)
				}
			})
			for _, table := range []any{&model.Department{}, &model.User{}, &model.Organization{}, &model.Project{}} {
				if !db.Migrator().HasTable(table) {
					t.Fatal("请先为可丢弃的测试数据库执行真实迁移测试")
				}
			}
			assertDepartmentMemberCountsAndScope(t, db)
		})
	}
}
