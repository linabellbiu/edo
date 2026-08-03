package database

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	moderncsqlite "modernc.org/sqlite"

	"edo/internal/config"
)

func Open(ctx context.Context, cfg config.Database, logger *slog.Logger) (*gorm.DB, error) {
	dialector, err := makeDialector(cfg)
	if err != nil {
		return nil, err
	}

	gormLog := gormlogger.New(
		log.New(&logWriter{logger: logger}, "", 0),
		gormlogger.Config{
			SlowThreshold:             500 * time.Millisecond,
			LogLevel:                  gormlogger.Error,
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      true,
			Colorful:                  false,
		},
	)
	db, err := gorm.Open(dialector, &gorm.Config{Logger: gormLog, TranslateError: true})
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	if err := registerDepartmentScope(db); err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取数据库连接池失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("数据库健康检查失败: %w", err)
	}
	return db, nil
}

func Close(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func makeDialector(cfg config.Database) (gorm.Dialector, error) {
	switch cfg.Driver {
	case "sqlite":
		dsn, err := sqliteDSN(cfg.DSN)
		if err != nil {
			return nil, err
		}
		return pureGoSQLiteDialector{
			Dialector: &gormsqlite.Dialector{DriverName: "sqlite", DSN: dsn},
		}, nil
	case "postgres":
		return postgres.Open(cfg.DSN), nil
	case "mysql":
		return mysql.Open(cfg.DSN), nil
	default:
		return nil, fmt.Errorf("不支持的数据库驱动 %q", cfg.Driver)
	}
}

type pureGoSQLiteDialector struct {
	*gormsqlite.Dialector
}

func (pureGoSQLiteDialector) Translate(err error) error {
	var sqliteErr *moderncsqlite.Error
	if !errors.As(err, &sqliteErr) {
		return err
	}
	switch sqliteErr.Code() {
	case 1555, 2067:
		return gorm.ErrDuplicatedKey
	case 787:
		return gorm.ErrForeignKeyViolated
	default:
		return err
	}
}

func sqliteDSN(dsn string) (string, error) {
	if dsn == ":memory:" {
		return dsn + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", nil
	}
	if strings.HasPrefix(dsn, "file:") {
		return appendSQLitePragmas(dsn, false), nil
	}
	dir := filepath.Dir(dsn)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("创建 SQLite 数据目录失败: %w", err)
	}
	return appendSQLitePragmas("file:"+dsn, true), nil
}

func appendSQLitePragmas(dsn string, wal bool) string {
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	pragmas := make([]string, 0, 3)
	if !strings.Contains(dsn, "_pragma=foreign_keys") {
		pragmas = append(pragmas, "_pragma=foreign_keys(1)")
	}
	if !strings.Contains(dsn, "_pragma=busy_timeout") {
		pragmas = append(pragmas, "_pragma=busy_timeout(5000)")
	}
	if wal && !strings.Contains(dsn, "_pragma=journal_mode") {
		pragmas = append(pragmas, "_pragma=journal_mode(WAL)")
	}
	if len(pragmas) == 0 {
		return dsn
	}
	return dsn + separator + strings.Join(pragmas, "&")
}

type logWriter struct {
	logger *slog.Logger
}

func (w *logWriter) Write(data []byte) (int, error) {
	w.logger.Error("数据库操作失败", "detail", strings.TrimSpace(string(data)))
	return len(data), nil
}

var _ io.Writer = (*logWriter)(nil)
