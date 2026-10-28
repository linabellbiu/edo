package database

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func postgresConnectionConfig(dsn string) (*pgx.ConnConfig, *time.Location, error) {
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, nil, err
	}
	// pgx 已处理 URL 解码和关键字引用；不再从原 DSN 提取或改写参数。
	for key, zone := range parsed.RuntimeParams {
		if !strings.EqualFold(key, "timezone") && !strings.EqualFold(key, "time_zone") {
			continue
		}
		if _, err := time.LoadLocation(zone); err != nil {
			return nil, nil, err
		}
		delete(parsed.RuntimeParams, key)
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, nil, err
	}
	parsed.RuntimeParams["timezone"] = location.String()
	return parsed, location, nil
}

func registerPostgresTimeCodecs(types *pgtype.Map, location *time.Location) {
	types.RegisterType(&pgtype.Type{Name: "timestamp", OID: pgtype.TimestampOID, Codec: &pgtype.TimestampCodec{ScanLocation: location}})
	types.RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: location}})
}

func postgresDialector(dsn string) (gorm.Dialector, *sql.DB, error) {
	parsed, location, err := postgresConnectionConfig(dsn)
	if err != nil {
		return nil, nil, err
	}
	// hook 不做可能失败的配置解析，避免连接建立后再因时区无效泄露连接。
	pool := stdlib.OpenDB(*parsed, stdlib.OptionAfterConnect(func(_ context.Context, connection *pgx.Conn) error {
		registerPostgresTimeCodecs(connection.TypeMap(), location)
		return nil
	}))
	return postgres.New(postgres.Config{Conn: pool}), pool, nil
}
