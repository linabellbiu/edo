package database

import (
	"database/sql"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func mysqlDialector(dsn string) (gorm.Dialector, *sql.DB, error) {
	parsed, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return nil, nil, err
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, nil, err
	}
	parsed.Loc = location
	parsed.ParseTime = true
	if parsed.Params == nil {
		parsed.Params = make(map[string]string)
	}
	for key := range parsed.Params {
		if strings.EqualFold(key, "time_zone") {
			delete(parsed.Params, key)
		}
	}
	// loc只决定Go时间解析；官方驱动的系统变量参数会为每个新连接设置会话时区。
	parsed.Params["time_zone"] = "'Asia/Shanghai'"
	return mysql.New(mysql.Config{DSNConfig: parsed}), nil, nil
}
