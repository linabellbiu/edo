package database

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestBuildTransferTargetUsesDatabaseDefaultsAndEscapesCredentials(t *testing.T) {
	tests := []struct {
		name       string
		input      TransferConnection
		wantDriver string
		wantHost   string
		wantPort   int
	}{
		{
			name: "mysql",
			input: TransferConnection{
				Driver: "mysql", Host: "mysql.internal", Username: "edo", Password: "p@ss/word",
				Database: "edo/database",
			},
			wantDriver: "mysql", wantHost: "mysql.internal", wantPort: DefaultMySQLPort,
		},
		{
			name: "postgres_ipv6",
			input: TransferConnection{
				Driver: "postgres", Host: "[2001:db8::10]", Username: "edo@example", Password: "p@ss/word",
				Database: "edo/database",
			},
			wantDriver: "postgres", wantHost: "2001:db8::10", wantPort: DefaultPostgresPort,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, err := BuildTransferTarget(test.input)
			if err != nil {
				t.Fatalf("生成目标数据库连接失败: %v", err)
			}
			if target.Driver != test.wantDriver {
				t.Fatalf("数据库驱动错误: got=%s want=%s", target.Driver, test.wantDriver)
			}
			if target.databaseName != test.input.Database || target.serverDSN == "" {
				t.Fatalf("缺少自动创建数据库所需的服务器连接信息")
			}
			switch target.Driver {
			case "mysql":
				configuration, parseErr := mysqldriver.ParseDSN(target.DSN)
				if parseErr != nil {
					t.Fatalf("解析 MySQL DSN 失败: %v", parseErr)
				}
				if configuration.Addr != test.wantHost+":"+strconv.Itoa(test.wantPort) || configuration.User != test.input.Username ||
					configuration.Passwd != test.input.Password || configuration.DBName != test.input.Database ||
					!configuration.ParseTime || configuration.Loc.String() != TransferTimeZone || configuration.Params["charset"] != "utf8mb4" {
					t.Fatalf("MySQL 连接配置不一致: %+v", configuration)
				}
			case "postgres":
				if strings.Contains(target.DSN, "TimeZone=Asia%2FShanghai") || !strings.Contains(target.DSN, "TimeZone=Asia/Shanghai") {
					t.Fatalf("PostgreSQL 时区参数必须保留未编码的斜杠: %s", target.DSN)
				}
				parsed, parseErr := url.Parse(target.DSN)
				if parseErr != nil {
					t.Fatalf("解析 PostgreSQL DSN 失败: %v", parseErr)
				}
				password, _ := parsed.User.Password()
				if parsed.Hostname() != test.wantHost || parsed.Port() != strconv.Itoa(test.wantPort) || parsed.User.Username() != test.input.Username ||
					password != test.input.Password || parsed.Path != "/"+test.input.Database ||
					parsed.Query().Get("sslmode") != "disable" || parsed.Query().Get("TimeZone") != TransferTimeZone {
					t.Fatalf("PostgreSQL 连接配置不一致: %s", target.DSN)
				}
			}
		})
	}
}

func TestQuoteTransferDatabaseIdentifier(t *testing.T) {
	tests := []struct {
		driver string
		name   string
		want   string
	}{
		{driver: "postgres", name: `edo"archive`, want: `"edo""archive"`},
		{driver: "mysql", name: "edo`archive", want: "`edo``archive`"},
	}
	for _, test := range tests {
		got, err := quoteTransferDatabaseIdentifier(test.driver, test.name)
		if err != nil || got != test.want {
			t.Fatalf("数据库标识符引用错误: driver=%s got=%q want=%q err=%v", test.driver, got, test.want, err)
		}
	}
}

func TestBuildTransferTargetRejectsInvalidFields(t *testing.T) {
	valid := TransferConnection{
		Driver: "postgres", Host: "database.internal", Port: 5432,
		Username: "edo", Password: "password", Database: "edo",
	}
	tests := []struct {
		name   string
		mutate func(*TransferConnection)
	}{
		{name: "unsupported_driver", mutate: func(input *TransferConnection) { input.Driver = "sqlite" }},
		{name: "host_with_port", mutate: func(input *TransferConnection) { input.Host = "database.internal:5432" }},
		{name: "empty_username", mutate: func(input *TransferConnection) { input.Username = "" }},
		{name: "empty_password", mutate: func(input *TransferConnection) { input.Password = "" }},
		{name: "empty_database", mutate: func(input *TransferConnection) { input.Database = "" }},
		{name: "invalid_port", mutate: func(input *TransferConnection) { input.Port = 70000 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if _, err := BuildTransferTarget(input); err != ErrInvalidTarget {
				t.Fatalf("无效连接参数未被拒绝: %v", err)
			}
		})
	}
}
