package database

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"

	"edo/internal/config"
)

func TestMySQLConnectionConfigUsesShanghaiForEveryConnection(t *testing.T) {
	for _, dsn := range []string{
		"fixture:fixture@tcp(127.0.0.1:3306)/edo",
		"fixture:fixture@tcp(127.0.0.1:3306)/edo?parseTime=false",
		"fixture:fixture@tcp(127.0.0.1:3306)/edo?parseTime=true&loc=Asia%2FShanghai",
		"fixture:fixture@tcp(127.0.0.1:3306)/edo?parseTime=true&loc=UTC&time_zone=%27%2B00%3A00%27&TIME_ZONE=%27SYSTEM%27",
	} {
		dialect, owned, err := makeDialector(config.Database{Driver: "mysql", DSN: dsn})
		if err != nil {
			t.Fatal("合法MySQL连接参数被拒绝")
		}
		mysql, ok := dialect.(*gormmysql.Dialector)
		if !ok || owned != nil || mysql.DSNConfig == nil {
			t.Fatal("没有保留MySQL官方驱动连接配置")
		}
		parsed := mysql.DSNConfig
		if !parsed.ParseTime || parsed.Loc.String() != "Asia/Shanghai" || parsed.Params["time_zone"] != "'Asia/Shanghai'" {
			t.Error("Go时间解析与每个物理连接的会话时区未同时使用上海")
		}
		for key := range parsed.Params {
			if strings.EqualFold(key, "time_zone") && key != "time_zone" {
				t.Error("保留了可覆盖上海时区的重复参数")
			}
		}
	}
}

func TestMySQLConnectionTimezonePreservesOtherOptions(t *testing.T) {
	dsn := "fixture:fixture:p@ss@tcp([::1]:3306)/edo?charset=utf8mb4&parseTime=true&tls=true&timeout=3s&readTimeout=4s&writeTimeout=5s&interpolateParams=true&sql_mode=%27STRICT_TRANS_TABLES%27"
	dialect, _, err := makeDialector(config.Database{Driver: "mysql", DSN: dsn})
	if err != nil {
		t.Fatal("合法MySQL参数被拒绝")
	}
	parsed := dialect.(*gormmysql.Dialector).DSNConfig
	if parsed.User != "fixture" || parsed.Passwd != "fixture:p@ss" || parsed.Net != "tcp" || parsed.Addr != "[::1]:3306" || parsed.DBName != "edo" || !parsed.ParseTime || parsed.TLSConfig != "true" || parsed.Timeout != 3*time.Second || parsed.ReadTimeout != 4*time.Second || parsed.WriteTimeout != 5*time.Second || !parsed.InterpolateParams || parsed.Params["sql_mode"] != "'STRICT_TRANS_TABLES'" {
		t.Fatal("时区归一化修改了凭据、地址、TLS、超时或其他选项")
	}
	if parsed.Loc.String() != "Asia/Shanghai" || parsed.Params["time_zone"] != "'Asia/Shanghai'" {
		t.Fatal("其他合法选项阻止了上海时区配置")
	}
}

func TestMySQLConnectionConfigRejectsInvalidDSNBeforeOpen(t *testing.T) {
	for _, dsn := range []string{
		"fixture:fixture@tcp(127.0.0.1:3306)/edo?loc=Not_A_Real_Zone",
		"fixture:fixture@tcp(127.0.0.1:3306)/edo?timeout=not-a-duration",
	} {
		if dialect, owned, err := makeDialector(config.Database{Driver: "mysql", DSN: dsn}); err == nil || dialect != nil || owned != nil {
			t.Error("无效MySQL配置没有在创建连接前被拒绝")
		}
	}
}

func TestExternalMySQLSessionTimezoneAndScanning(t *testing.T) {
	if driver := os.Getenv("EDO_TEST_DATABASE_DRIVER"); driver != "" && driver != "mysql" {
		t.Skip("当前任务验证其他数据库")
	}
	dsn := os.Getenv("EDO_TEST_MYSQL_DSN")
	if dsn == "" {
		if os.Getenv("EDO_TEST_DATABASE_DRIVER") == "mysql" {
			t.Fatal("已选择MySQL，但连接配置缺失")
		}
		t.Skip("未提供独立MySQL连接，仅跳过真实会话时区验证")
	}
	input, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("MySQL测试连接格式无效")
	}
	input.Loc = time.UTC
	input.ParseTime = true
	if input.Params == nil {
		input.Params = make(map[string]string)
	}
	for key := range input.Params {
		if strings.EqualFold(key, "time_zone") {
			delete(input.Params, key)
		}
	}
	// 故意传入UTC，不能靠测试工具预补上海参数或服务器默认值让产品假绿。
	input.Params["time_zone"] = "'+00:00'"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := Open(ctx, config.Database{Driver: "mysql", DSN: input.FormatDSN(), MaxOpenConns: 2, MaxIdleConns: 2}, slog.Default())
	if err != nil {
		t.Fatal("打开MySQL时区测试连接失败")
	}
	t.Cleanup(func() {
		if err := Close(db); err != nil {
			t.Error("关闭MySQL时区测试连接失败")
		}
	})
	pool, err := db.DB()
	if err != nil {
		t.Fatal("读取MySQL测试连接池失败")
	}
	check := func(connection *sql.Conn) uint64 {
		t.Helper()
		var id uint64
		var zone string
		var offset int
		var wall time.Time
		if err := connection.QueryRowContext(ctx, "SELECT CONNECTION_ID(), @@session.time_zone, TIMESTAMPDIFF(SECOND, UTC_TIMESTAMP(), NOW()), CAST('2026-11-03 09:02:03' AS DATETIME)").Scan(&id, &zone, &offset, &wall); err != nil {
			t.Fatal("读取MySQL会话时区及时间类型失败")
		}
		if zone != "Asia/Shanghai" || offset != 8*60*60 || wall.Location().String() != "Asia/Shanghai" || wall.Hour() != 9 {
			t.Fatal("MySQL会话或时间读取没有使用上海时区")
		}
		return id
	}
	first, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal("打开首个MySQL测试连接失败")
	}
	defer first.Close()
	firstID := check(first)
	second, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal("打开第二个MySQL测试连接失败")
	}
	defer second.Close()
	secondID := check(second)
	if firstID == secondID {
		t.Fatal("没有验证两个独立物理连接")
	}
	pool.SetMaxIdleConns(0)
	_ = first.Close()
	_ = second.Close()
	reopened, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal("重建MySQL测试连接失败")
	}
	defer reopened.Close()
	if id := check(reopened); id == firstID || id == secondID {
		t.Fatal("没有验证重建后的新物理连接")
	}
}
