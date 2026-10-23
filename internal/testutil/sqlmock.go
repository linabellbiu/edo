package testutil

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// NewSQLMock 仅模拟 SQL 交互，不验证方言、迁移或权限回调；这些由真实数据库测试覆盖。
func NewSQLMock(t testing.TB) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("创建 SQL Mock 失败: %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("SQL Mock 预期未满足: %v", err)
		}
		mock.ExpectClose()
		if err := conn.Close(); err != nil {
			t.Errorf("关闭 SQL Mock 失败: %v", err)
		}
	})
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: conn}), &gorm.Config{
		DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("初始化 Mock GORM 失败: %v", err)
	}
	return db, mock
}
