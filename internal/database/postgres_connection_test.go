package database

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"edo/internal/config"
)

func TestPostgresParsedPoolSkipsRawDSNTimezoneMatcher(t *testing.T) {
	isolatePostgresParameters(t)
	dialector, owned, err := makeDialector(config.Database{Driver: "postgres", DSN: "postgresql://fixture:fixture@127.0.0.1:5432/fixture?sslmode=disable&TimeZone=Asia%2FShanghai"})
	if err != nil {
		t.Fatal("合法的已编码时区不能被拒绝")
	}
	postgresDialect, ok := dialector.(*postgres.Dialector)
	if !ok {
		t.Fatal("PostgreSQL 方言丢失")
	}
	pool, ok := postgresDialect.Conn.(*sql.DB)
	if !ok || pool == nil || pool != owned {
		t.Fatal("未使用已解析的连接池，仍会触发 GORM 原始 DSN 时区匹配")
	}
	t.Cleanup(func() { _ = pool.Close() })
}

func isolatePostgresParameters(t *testing.T) {
	t.Helper()
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGSERVICEFILE", t.TempDir()+"/no-service")
	t.Setenv("PGPASSFILE", t.TempDir()+"/no-passfile")
	t.Setenv("PGTZ", "")
}

func TestPostgresConnectionConfigUsesDecodedParametersAndShanghai(t *testing.T) {
	isolatePostgresParameters(t)
	for _, dsn := range []string{
		"postgresql://fixture:fixture@127.0.0.1:5432/fixture?TimeZone=Asia%2FShanghai",
		"postgresql://fixture:fixture@127.0.0.1:5432/fixture?timezone=Asia/Shanghai",
		"postgres://fixture:fixture@127.0.0.1:5432/fixture?TIMEZONE=UTC",
		"host=127.0.0.1 user=fixture password=fixture dbname=fixture TimeZone='Asia/Shanghai'",
		"host=127.0.0.1 user=fixture password=fixture dbname=fixture timezone=UTC",
		"host=127.0.0.1 user=fixture password=fixture dbname=fixture time_zone=Asia/Shanghai",
		"postgres://fixture:fixture@127.0.0.1:5432/fixture?TimeZone=UTC&timezone=Asia%2FShanghai",
		"postgresql://fixture:fixture@127.0.0.1:5432/fixture",
	} {
		parsed, location, err := postgresConnectionConfig(dsn)
		if err != nil || location.String() != "Asia/Shanghai" || parsed.RuntimeParams["timezone"] != "Asia/Shanghai" {
			t.Fatal("URL 或关键字格式没有统一使用上海时区")
		}
		for key := range parsed.RuntimeParams {
			if (strings.EqualFold(key, "timezone") || strings.EqualFold(key, "time_zone")) && key != "timezone" {
				t.Fatal("保留了可覆盖上海时区的重复参数")
			}
		}
	}
}

func TestPostgresConnectionConfigPreservesOtherPGXOptions(t *testing.T) {
	isolatePostgresParameters(t)
	parsed, _, err := postgresConnectionConfig("postgresql://fixture:p%40ss%26word@127.0.0.1:5432/fixture?sslmode=require&connect_timeout=3&default_query_exec_mode=simple_protocol&statement_cache_capacity=0&description_cache_capacity=7&application_name=edo_fixture&TimeZone=Asia%2FShanghai")
	if err != nil {
		t.Fatal("合法 PGX 参数被拒绝")
	}
	if parsed.DefaultQueryExecMode != pgx.QueryExecModeSimpleProtocol || parsed.StatementCacheCapacity != 0 || parsed.DescriptionCacheCapacity != 7 || parsed.TLSConfig == nil || parsed.ConnectTimeout != 3*time.Second || parsed.RuntimeParams["application_name"] != "edo_fixture" || parsed.Password != "p@ss&word" {
		t.Fatal("时区处理修改了协议、缓存、TLS、超时或已解码凭据")
	}
}

func TestPostgresConnectionConfigRejectsInvalidTimezoneAndDSN(t *testing.T) {
	isolatePostgresParameters(t)
	for _, dsn := range []string{
		"postgresql://fixture:fixture@127.0.0.1:not-a-port/fixture",
		"host=127.0.0.1 user=fixture password='unterminated",
		"postgresql://fixture:fixture@127.0.0.1:5432/fixture?TimeZone=Not_A_Real_Zone",
		"host=127.0.0.1 user=fixture password=fixture dbname=fixture timezone='Not_A_Real_Zone'",
		"postgresql://fixture:fixture@127.0.0.1:5432/fixture?TimeZone=UTC&timezone=Not_A_Real_Zone",
	} {
		if parsed, location, err := postgresConnectionConfig(dsn); err == nil || parsed != nil || location != nil {
			t.Fatal("无效参数被静默替换成上海时区")
		}
	}
}

func TestPostgresTimeCodecsKeepShanghaiAndDoNotMutateDefaults(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	types := pgtype.NewMap()
	registerPostgresTimeCodecs(types, location)
	for _, target := range []struct {
		oid  uint32
		text string
		hour int
	}{
		{pgtype.TimestampOID, "2026-10-28 09:02:03", 9},
		{pgtype.TimestamptzOID, "2026-10-28 09:02:03+00", 17},
	} {
		valueType, ok := types.TypeForOID(target.oid)
		if !ok {
			t.Fatal("时间类型未登记")
		}
		value, err := valueType.Codec.DecodeDatabaseSQLValue(types, target.oid, pgtype.TextFormatCode, []byte(target.text))
		if err != nil {
			t.Fatal(err)
		}
		decoded, ok := value.(time.Time)
		if !ok || decoded.Location().String() != "Asia/Shanghai" || decoded.Hour() != target.hour {
			t.Fatal("timestamp 与 timestamptz 的上海时间解释不正确")
		}
	}
	defaults := pgtype.NewMap()
	plain, _ := defaults.TypeForOID(pgtype.TimestampOID)
	zoned, _ := defaults.TypeForOID(pgtype.TimestamptzOID)
	if plain.Codec.(*pgtype.TimestampCodec).ScanLocation != nil || zoned.Codec.(*pgtype.TimestamptzCodec).ScanLocation != nil {
		t.Fatal("修改了其他连接的全局时间类型")
	}
}

func TestOpenPostgresConfigFailureDoesNotExposeCredentials(t *testing.T) {
	isolatePostgresParameters(t)
	for _, dsn := range []string{
		"postgresql://fixture:fixture_private_945@127.0.0.1:bad/fixture?password=fixture_query_468&sslpassword=fixture_ssl_379",
		"host=127.0.0.1 user=fixture password='fixture_private_945",
	} {
		var output bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&output, nil))
		db, err := Open(context.Background(), config.Database{Driver: "postgres", DSN: dsn}, logger)
		if db != nil || err == nil || err.Error() != "数据库连接配置无效，请检查数据库设置" {
			t.Fatal("配置错误未返回稳定中文提示")
		}
		for _, secret := range []string{"fixture_private_945", "fixture_query_468", "fixture_ssl_379", dsn} {
			if strings.Contains(output.String(), secret) || strings.Contains(err.Error(), secret) {
				t.Fatal("数据库解析错误泄露了连接配置")
			}
		}
		if !strings.Contains(output.String(), "database_dialector") || !strings.Contains(output.String(), "error_type") {
			t.Fatal("配置失败缺少安全诊断信息")
		}
	}
}

func mockConnectionDialect(t *testing.T, driver string) (*sql.DB, sqlmock.Sqlmock, gorm.Dialector) {
	t.Helper()
	pool, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	switch driver {
	case "postgres":
		return pool, mock, postgres.New(postgres.Config{Conn: pool})
	case "mysql":
		return pool, mock, mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true})
	default:
		mock.ExpectQuery("select sqlite_version").WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow("3.45.1"))
		return pool, mock, pureGoSQLiteDialector{Dialector: &gormsqlite.Dialector{DriverName: "sqlite", Conn: pool}}
	}
}

func TestOpenConnectionPoolLifecycle(t *testing.T) {
	for _, driver := range []string{"postgres", "mysql", "sqlite"} {
		for _, scenario := range []string{"success", "ping-failure", "canceled", "deadline"} {
			t.Run(driver+"/"+scenario, func(t *testing.T) {
				pool, mock, dialect := mockConnectionDialect(t, driver)
				ctx := context.Background()
				var cancel context.CancelFunc
				switch scenario {
				case "canceled":
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				case "deadline":
					ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
					defer cancel()
					mock.ExpectPing().WillDelayFor(time.Second)
				case "ping-failure":
					mock.ExpectPing().WillReturnError(errors.New("fixture_private_connection_error"))
				default:
					mock.ExpectPing()
				}
				if scenario != "success" {
					mock.ExpectClose()
				}
				var output bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&output, nil))
				db, err := openWithDialector(ctx, config.Database{Driver: driver, MaxOpenConns: 3, MaxIdleConns: 2, ConnMaxLifetime: time.Minute}, logger, dialect, pool)
				if scenario == "success" {
					if err != nil || db == nil || pool.Stats().MaxOpenConnections != 3 || pool.Stats().OpenConnections != 1 {
						t.Fatal("连接池配置或唯一一次 Ping 不正确")
					}
					mock.ExpectClose()
					if err := Close(db); err != nil {
						t.Fatal("成功连接未交由调用者释放")
					}
				} else {
					if db != nil || err == nil || err.Error() != "数据库连接检查失败，请检查数据库配置和服务状态" || pool.PingContext(context.Background()) == nil {
						t.Fatal("失败或取消后仍返回连接，或连接池没有关闭")
					}
					if strings.Contains(err.Error()+output.String(), "fixture_private_connection_error") || !strings.Contains(output.String(), "database_ping") {
						t.Fatal("连接失败泄密或缺少安全日志")
					}
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

type failingConnectionDialect struct{ gorm.Dialector }

func (d failingConnectionDialect) Initialize(db *gorm.DB) error {
	db.ConnPool = d.Dialector.(*postgres.Dialector).Conn
	return errors.New("fixture_private_initialize_error")
}

func TestOpenInitializationFailureClosesOwnedPool(t *testing.T) {
	pool, mock, dialect := mockConnectionDialect(t, "postgres")
	mock.ExpectClose()
	var output bytes.Buffer
	db, err := openWithDialector(context.Background(), config.Database{Driver: "postgres"}, slog.New(slog.NewJSONHandler(&output, nil)), failingConnectionDialect{dialect}, pool)
	if db != nil || err == nil || err.Error() != "连接数据库失败，请检查数据库配置和服务状态" || pool.PingContext(context.Background()) == nil {
		t.Fatal("初始化失败后没有关闭已分配连接池")
	}
	if strings.Contains(output.String()+err.Error(), "fixture_private_initialize_error") || !strings.Contains(output.String(), "database_open") {
		t.Fatal("初始化失败泄密或缺少诊断")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresConnectionDiagnosticOnlyRecordsSafeState(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logConnectionFailure(context.Background(), logger, "database_ping", "postgres", &pgconn.PgError{Code: "28P01", Message: "fixture_private_server_message", Detail: "fixture_private_detail"})
	if !strings.Contains(output.String(), "28P01") || strings.Contains(output.String(), "fixture_private") {
		t.Fatal("服务端连接诊断没有安全保留 SQLSTATE")
	}
	output.Reset()
	logConnectionFailure(context.Background(), logger, "database_ping", "postgres", &pgconn.PgError{Code: "bad_private_code", Message: "fixture_private_server_message"})
	if strings.Contains(output.String(), "bad_private_code") || strings.Contains(output.String(), "fixture_private") {
		t.Fatal("无效 SQLSTATE 被当作安全字段记录")
	}
}

func TestExternalPostgresSessionTimezoneAndScanning(t *testing.T) {
	if driver := os.Getenv("EDO_TEST_DATABASE_DRIVER"); driver != "" && driver != "postgres" {
		t.Skip("当前任务验证其他数据库")
	}
	dsn := os.Getenv("EDO_TEST_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("EDO_TEST_DATABASE_DRIVER") == "postgres" {
			t.Fatal("已选择 PostgreSQL，但连接配置缺失")
		}
		t.Skip("未提供独立 PostgreSQL 连接，仅跳过真实时区读取验证")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := Open(ctx, config.Database{Driver: "postgres", DSN: dsn, MaxOpenConns: 2, MaxIdleConns: 1}, slog.Default())
	if err != nil {
		t.Fatal("打开 PostgreSQL 时区测试连接失败")
	}
	t.Cleanup(func() {
		if err := Close(db); err != nil {
			t.Error("关闭 PostgreSQL 时区测试连接失败")
		}
	})
	pool, err := db.DB()
	if err != nil {
		t.Fatal("读取 PostgreSQL 测试连接池失败")
	}
	var timezone string
	if err := pool.QueryRowContext(ctx, "SHOW TimeZone").Scan(&timezone); err != nil || timezone != "Asia/Shanghai" {
		t.Fatal("PostgreSQL 会话没有使用上海时区")
	}
	var plain, zoned time.Time
	if err := pool.QueryRowContext(ctx, "SELECT TIMESTAMP '2026-10-28 09:02:03', TIMESTAMPTZ '2026-10-28 09:02:03+00'").Scan(&plain, &zoned); err != nil {
		t.Fatal("读取 PostgreSQL 时间类型失败")
	}
	if plain.Location().String() != "Asia/Shanghai" || plain.Hour() != 9 || zoned.Location().String() != "Asia/Shanghai" || zoned.Hour() != 17 {
		t.Fatal("PostgreSQL 时间读取没有保留上海墙上时间及真实时间点")
	}
}
