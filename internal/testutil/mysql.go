package testutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/database"
)

type mysqlTemplateTable struct {
	name, ddl string
	columns   []string
	rows      [][]any
}

// 模板迁移后立即关闭并删除，缓存中仅保留只读结构与初始行，夹具不共享可变数据库。
var migratedMySQL = sync.OnceValues(func() (tables []mysqlTemplateTable, resultErr error) {
	db, cleanup, err := createMySQLDatabase()
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, database.Close(db), cleanup()) }()
	if err := database.Migrate(context.Background(), db); err != nil {
		return nil, fmt.Errorf("初始化 MySQL 测试模板失败: %w", err)
	}
	connection, err := db.DB()
	if err != nil {
		return nil, err
	}
	names, err := db.Migrator().GetTables()
	if err != nil {
		return nil, err
	}
	tables = make([]mysqlTemplateTable, 0, len(names))
	for _, name := range names {
		table := mysqlTemplateTable{name: name}
		var ignored string
		if err := connection.QueryRow("SHOW CREATE TABLE "+mysqlIdentifier(name)).Scan(&ignored, &table.ddl); err != nil {
			return nil, err
		}
		rows, err := connection.Query("SELECT * FROM " + mysqlIdentifier(name))
		if err != nil {
			return nil, err
		}
		table.columns, err = rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		for rows.Next() {
			values, destinations := make([]any, len(table.columns)), make([]any, len(table.columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				return nil, err
			}
			for i, value := range values {
				if bytes, ok := value.([]byte); ok {
					values[i] = append([]byte{}, bytes...)
				}
			}
			table.rows = append(table.rows, values)
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	return tables, nil
})

func openMySQLFixture(t testing.TB) (*gorm.DB, error) {
	t.Helper()
	tables, err := migratedMySQL()
	if err != nil {
		return nil, err
	}
	db, cleanup, err := createMySQLDatabase()
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		if err := database.Close(db); err != nil {
			t.Errorf("关闭 MySQL 测试数据库失败: %v", err)
		}
		if err := cleanup(); err != nil {
			t.Errorf("清理 MySQL 测试数据库失败: %v", err)
		}
	})
	pool, err := db.DB()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	connection, err := pool.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0"); err != nil {
		return nil, err
	}
	defer func() {
		reset, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := connection.ExecContext(reset, "SET FOREIGN_KEY_CHECKS=1"); err != nil {
			t.Errorf("恢复 MySQL 测试连接约束失败: %v", err)
		}
	}()
	for _, table := range tables {
		if _, err := connection.ExecContext(ctx, table.ddl); err != nil {
			return nil, fmt.Errorf("复制 MySQL 测试表结构失败: %w", err)
		}
	}
	for _, table := range tables {
		columns := make([]string, len(table.columns))
		for i, column := range table.columns {
			columns[i] = mysqlIdentifier(column)
		}
		statement := "INSERT INTO " + mysqlIdentifier(table.name) + " (" + strings.Join(columns, ",") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ")"
		for _, row := range table.rows {
			if _, err := connection.ExecContext(ctx, statement, row...); err != nil {
				return nil, fmt.Errorf("复制 MySQL 测试初始记录失败: %w", err)
			}
		}
	}
	return db, nil
}

func createMySQLDatabase() (*gorm.DB, func() error, error) {
	value := os.Getenv("EDO_TEST_MYSQL_ADMIN_DSN")
	if value == "" {
		return nil, nil, errors.New("MySQL 业务测试缺少 EDO_TEST_MYSQL_ADMIN_DSN")
	}
	settings, err := mysql.ParseDSN(value)
	if err != nil || settings.DBName != "" {
		return nil, nil, errors.New("MySQL 测试管理连接必须有效且不指定数据库")
	}
	settings.ParseTime, settings.Loc = true, time.FixedZone("Asia/Shanghai", 8*60*60)
	settings.Timeout, settings.ReadTimeout, settings.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
	if settings.Params == nil {
		settings.Params = map[string]string{}
	}
	settings.Params["time_zone"] = "'+08:00'"
	admin, err := sql.Open("mysql", settings.FormatDSN())
	if err != nil {
		return nil, nil, errors.New("打开 MySQL 测试管理连接失败")
	}
	name := "edo_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+mysqlIdentifier(name)+" CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		admin.Close()
		return nil, nil, fmt.Errorf("创建独立 MySQL 测试数据库失败: %w", err)
	}
	cleanup := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, err := admin.ExecContext(ctx, "DROP DATABASE "+mysqlIdentifier(name))
		return errors.Join(err, admin.Close())
	}
	settings.DBName = name
	db, err := database.Open(ctx, config.Database{Driver: "mysql", DSN: settings.FormatDSN(), MaxOpenConns: 4, MaxIdleConns: 2}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		_ = cleanup()
		return nil, nil, err
	}
	return db, cleanup, nil
}

func mysqlIdentifier(value string) string { return "`" + strings.ReplaceAll(value, "`", "``") + "`" }
