package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/mysql"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	moderncsqlite "modernc.org/sqlite"

	"edo/internal/config"
)

func Open(ctx context.Context, cfg config.Database, logger *slog.Logger) (*gorm.DB, error) {
	if logger == nil {
		logger = slog.Default()
	}
	dialector, pool, err := makeDialector(cfg)
	if err != nil {
		logConnectionFailure(ctx, logger, "database_dialector", cfg.Driver, err)
		return nil, errors.New("数据库连接配置无效，请检查数据库设置")
	}
	return openWithDialector(ctx, cfg, logger, dialector, pool)
}

// PostgreSQL 的池由 EDO 创建；其他方言仍由 GORM 初始化，失败时统一释放。
func openWithDialector(ctx context.Context, cfg config.Database, logger *slog.Logger, dialector gorm.Dialector, pool *sql.DB) (db *gorm.DB, resultErr error) {
	defer func() {
		if resultErr == nil {
			return
		}
		if pool == nil && db != nil {
			pool, _ = db.DB()
		}
		if pool != nil {
			if err := pool.Close(); err != nil {
				logConnectionFailure(ctx, logger, "database_close_failed_open", cfg.Driver, err)
			}
		}
		db = nil
	}()
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
	var err error
	// 先设置连接池再做唯一一次有界 Ping，避免 GORM 使用 Background 连接。
	db, err = gorm.Open(dialector, &gorm.Config{Logger: gormLog, TranslateError: true, DisableAutomaticPing: true})
	if err != nil {
		logConnectionFailure(ctx, logger, "database_open", cfg.Driver, err)
		return db, errors.New("连接数据库失败，请检查数据库配置和服务状态")
	}
	if err := registerDepartmentScope(db); err != nil {
		logConnectionFailure(ctx, logger, "database_register_scope", cfg.Driver, err)
		return db, errors.New("数据库初始化失败，请检查服务日志")
	}

	sqlDB, err := db.DB()
	if err != nil {
		logConnectionFailure(ctx, logger, "database_pool", cfg.Driver, err)
		return db, errors.New("数据库连接池初始化失败，请检查服务日志")
	}
	pool = sqlDB
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		logConnectionFailure(ctx, logger, "database_ping", cfg.Driver, err)
		return db, errors.New("数据库连接检查失败，请检查数据库配置和服务状态")
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

func makeDialector(cfg config.Database) (gorm.Dialector, *sql.DB, error) {
	switch cfg.Driver {
	case "sqlite":
		dsn, err := sqliteDSN(cfg.DSN)
		if err != nil {
			return nil, nil, err
		}
		return pureGoSQLiteDialector{
			Dialector: &gormsqlite.Dialector{DriverName: "sqlite", DSN: dsn},
		}, nil, nil
	case "postgres":
		return postgresDialector(cfg.DSN)
	case "mysql":
		return mysql.Open(cfg.DSN), nil, nil
	default:
		return nil, nil, errors.New("不支持的数据库驱动")
	}
}

func logConnectionFailure(ctx context.Context, logger *slog.Logger, operation, driver string, err error) {
	if driver != "sqlite" && driver != "postgres" && driver != "mysql" {
		driver = "invalid"
	}
	// 驱动解析错误及其 cause 可能含完整 DSN；只记录原错误类型和安全分类。
	attributes := []any{"operation", operation, "driver", driver, "error_type", fmt.Sprintf("%T", err)}
	if cause := errors.Unwrap(err); cause != nil {
		attributes = append(attributes, "cause_type", fmt.Sprintf("%T", cause))
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && len(postgresError.Code) == 5 && strings.Trim(postgresError.Code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") == "" {
		attributes = append(attributes, "sql_state", postgresError.Code)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		attributes = append(attributes, "reason", "deadline")
	} else if errors.Is(err, context.Canceled) {
		attributes = append(attributes, "reason", "canceled")
	}
	logger.ErrorContext(ctx, "数据库连接操作失败", attributes...)
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
