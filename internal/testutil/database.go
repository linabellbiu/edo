// Package testutil 提供测试夹具；生产服务不得依赖本包。
package testutil

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/database"
)

// 每个测试进程只迁移一次。缓存的是已关闭连接后的文件字节，不是共享数据库。
var migratedSQLite = sync.OnceValues(func() ([]byte, error) {
	dir, err := os.MkdirTemp("", "edo-test-schema-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "template.db")
	db, err := openSQLite(path)
	if err != nil {
		return nil, err
	}
	migrateErr := database.Migrate(context.Background(), db)
	// 最后一个连接关闭后 WAL 已合并，才能复制主文件。
	closeErr := database.Close(db)
	if migrateErr != nil {
		return nil, fmt.Errorf("初始化测试数据库模板失败: %w", migrateErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return os.ReadFile(path)
})

// NewDatabase 为每个用例创建独立的真实 SQLite，保留生产连接配置和权限回调。
// 仅用于业务集成测试；迁移测试必须继续从空库执行，不得使用此模板。
func NewDatabase(t testing.TB) *gorm.DB {
	t.Helper()
	data, err := migratedSQLite()
	if err != nil {
		t.Fatalf("读取测试数据库模板失败: %v", err)
	}
	path := filepath.Join(t.TempDir(), "edo.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("复制测试数据库失败: %v", err)
	}
	db, err := openSQLite(path)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(db); err != nil {
			t.Errorf("关闭测试数据库失败: %v", err)
		}
	})
	return db
}

func openSQLite(path string) (*gorm.DB, error) {
	// t.TempDir 的子测试路径可能包含 #、? 等 URI 字符，必须转义以免多个用例连接同一文件。
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=journal_mode(WAL)"}).String()
	return database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: dsn, MaxOpenConns: 1, MaxIdleConns: 1,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}
