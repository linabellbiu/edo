package testutil_test

import (
	"context"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

func TestDatabaseTemplateIsCompleteAndIsolated(t *testing.T) {
	first, second := testutil.NewDatabase(t), testutil.NewDatabase(t)
	if err := database.VerifyMigrations(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	testutil.CreateAdmin(t, first)
	var count int64
	if err := second.Model(&model.User{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("测试数据库之间泄漏用户数据: count=%d err=%v", count, err)
	}
	if err := first.Exec("DROP TABLE users").Error; err != nil {
		t.Fatal(err)
	}
	if !second.Migrator().HasTable(&model.User{}) {
		t.Fatal("测试数据库之间泄漏结构变更")
	}
	third := testutil.NewDatabase(t)
	if err := database.VerifyMigrations(context.Background(), third); err != nil {
		t.Fatalf("用例修改了模板: %v", err)
	}
	var foreignKeys int
	query := "PRAGMA foreign_keys"
	if third.Dialector.Name() == "mysql" {
		query = "SELECT @@FOREIGN_KEY_CHECKS"
	}
	if err := third.Raw(query).Scan(&foreignKeys).Error; err != nil || foreignKeys != 1 {
		t.Fatalf("未保留真实连接的外键约束: foreign_keys=%d err=%v", foreignKeys, err)
	}
}

func TestDatabaseTemplateSupportsParallelFixtures(t *testing.T) {
	for range 4 {
		t.Run("独立数据库", func(t *testing.T) {
			t.Parallel()
			db := testutil.NewDatabase(t)
			testutil.CreateAdmin(t, db)
			var count int64
			if err := db.Model(&model.User{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("并行夹具相互污染: count=%d err=%v", count, err)
			}
		})
	}
}

func TestDatabaseSelectionRejectsMissingOrUnsafeConnection(t *testing.T) {
	for _, item := range []struct{ name, driver, dsn string }{
		{name: "未知驱动", driver: "invalid"},
		{name: "MySQL 缺少管理连接", driver: "mysql"},
		{name: "MySQL 连接指定已有数据库", driver: "mysql", dsn: "test:test@tcp(127.0.0.1:1)/business"},
	} {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv("EDO_TEST_BUSINESS_DRIVER", item.driver)
			t.Setenv("EDO_TEST_MYSQL_ADMIN_DSN", item.dsn)
			if db, err := testutil.OpenDatabase(t); err == nil || db != nil {
				t.Fatal("无效或指向已有数据库的测试配置未被拒绝")
			}
		})
	}
}
