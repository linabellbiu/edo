package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"zrt/internal/cache"
	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/messaging"
)

type Resources struct {
	Database *gorm.DB
	SQL      *sql.DB
	Redis    *cache.Redis
	NATS     *messaging.NATS
	logger   *slog.Logger
}

func Open(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Resources, error) {
	db, err := database.Open(ctx, cfg.Database, logger)
	if err != nil {
		return nil, err
	}
	if err := database.VerifyMigrations(ctx, db); err != nil {
		_ = database.Close(db)
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		_ = database.Close(db)
		return nil, fmt.Errorf("获取数据库连接池失败: %w", err)
	}
	redisClient, err := cache.Open(ctx, cfg.Redis)
	if err != nil {
		_ = database.Close(db)
		return nil, err
	}
	natsClient, err := messaging.Open(ctx, cfg.NATS, logger)
	if err != nil {
		_ = redisClient.Close()
		_ = database.Close(db)
		return nil, err
	}
	if err := natsClient.EnsureStreams(ctx); err != nil {
		_ = natsClient.Close()
		_ = redisClient.Close()
		_ = database.Close(db)
		return nil, err
	}
	return &Resources{
		Database: db, SQL: sqlDB, Redis: redisClient, NATS: natsClient, logger: logger,
	}, nil
}

func (r *Resources) Close() {
	if err := r.NATS.Close(); err != nil {
		r.logger.Error("关闭 NATS 连接失败", "operation", "shutdown_nats", "err", err)
	}
	if err := r.Redis.Close(); err != nil {
		r.logger.Error("关闭 Redis 连接失败", "operation", "shutdown_redis", "err", err)
	}
	if err := database.Close(r.Database); err != nil {
		r.logger.Error("关闭数据库连接失败", "operation", "shutdown_database", "err", err)
	}
}
