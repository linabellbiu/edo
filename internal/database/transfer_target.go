package database

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	mysqldriver "github.com/go-sql-driver/mysql"
)

const (
	DefaultMySQLPort    = 3306
	DefaultPostgresPort = 5432
	TransferTimeZone    = "Asia/Shanghai"
)

var transferLocation = time.FixedZone(TransferTimeZone, 8*60*60)

type TransferConnection struct {
	Driver   string
	Host     string
	Port     int
	Username string
	Password string
	Database string
}

// BuildTransferTarget 使用数据库驱动支持的格式生成连接串，避免特殊字符破坏连接参数边界。
func BuildTransferTarget(input TransferConnection) (TransferTarget, error) {
	driver := strings.ToLower(strings.TrimSpace(input.Driver))
	host, ok := normalizeTransferHost(input.Host)
	username := strings.TrimSpace(input.Username)
	databaseName := strings.TrimSpace(input.Database)
	port := input.Port
	if port == 0 {
		port = DefaultTransferPort(driver)
	}
	if !ok || username == "" || input.Password == "" || databaseName == "" ||
		len(username) > 256 || len(input.Password) > 1024 || len(databaseName) > 256 ||
		port < 1 || port > 65535 {
		return TransferTarget{}, ErrInvalidTarget
	}
	if _, err := quoteTransferDatabaseIdentifier(driver, databaseName); err != nil {
		return TransferTarget{}, ErrInvalidTarget
	}

	address := net.JoinHostPort(host, strconv.Itoa(port))
	switch driver {
	case "mysql":
		configuration := mysqldriver.NewConfig()
		configuration.User = username
		configuration.Passwd = input.Password
		configuration.Net = "tcp"
		configuration.Addr = address
		configuration.DBName = databaseName
		configuration.ParseTime = true
		configuration.Loc = transferLocation
		configuration.Params = map[string]string{"charset": "utf8mb4"}
		targetDSN := configuration.FormatDSN()
		configuration.DBName = ""
		return TransferTarget{Driver: driver, DSN: targetDSN, databaseName: databaseName, serverDSN: configuration.FormatDSN()}, nil
	case "postgres":
		targetDSN := postgresTransferDSN(address, username, input.Password, databaseName)
		serverDSN := postgresTransferDSN(address, username, input.Password, "postgres")
		return TransferTarget{Driver: driver, DSN: targetDSN, databaseName: databaseName, serverDSN: serverDSN}, nil
	default:
		return TransferTarget{}, ErrInvalidTarget
	}
}

func postgresTransferDSN(address, username, password, databaseName string) string {
	connectionURL := &url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(username, password),
		Host:     address,
		Path:     "/" + databaseName,
		RawPath:  "/" + url.PathEscape(databaseName),
		RawQuery: "sslmode=disable&TimeZone=" + TransferTimeZone,
	}
	return connectionURL.String()
}

func quoteTransferDatabaseIdentifier(driver, databaseName string) (string, error) {
	databaseName = strings.TrimSpace(databaseName)
	if databaseName == "" {
		return "", ErrInvalidTarget
	}
	for _, character := range databaseName {
		if character == 0 || unicode.IsControl(character) {
			return "", ErrInvalidTarget
		}
	}
	switch driver {
	case "mysql":
		if len([]byte(databaseName)) > 64 {
			return "", ErrInvalidTarget
		}
		return "`" + strings.ReplaceAll(databaseName, "`", "``") + "`", nil
	case "postgres":
		if len([]byte(databaseName)) > 63 {
			return "", ErrInvalidTarget
		}
		return `"` + strings.ReplaceAll(databaseName, `"`, `""`) + `"`, nil
	default:
		return "", ErrInvalidTarget
	}
}

func DefaultTransferPort(driver string) int {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "mysql":
		return DefaultMySQLPort
	case "postgres":
		return DefaultPostgresPort
	default:
		return 0
	}
}

func normalizeTransferHost(value string) (string, bool) {
	host := strings.TrimSpace(value)
	if len(host) < 1 || len(host) > 255 {
		return "", false
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		if net.ParseIP(host) == nil {
			return "", false
		}
	}
	for _, character := range host {
		if unicode.IsSpace(character) || unicode.IsControl(character) || strings.ContainsRune("/?#@[]", character) {
			return "", false
		}
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return "", false
	}
	return host, true
}
