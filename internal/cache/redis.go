package cache

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"

	"edo/internal/config"
)

type Redis struct {
	client *redis.Client
	prefix string
}

func Open(ctx context.Context, cfg config.Redis) (*Redis, error) {
	options, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("Redis 连接配置无效: %w", err)
	}
	options.Protocol = 2
	options.DialTimeout = cfg.Timeout
	options.ReadTimeout = cfg.Timeout
	options.WriteTimeout = cfg.Timeout
	client := redis.NewClient(options)
	pingCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("连接 Redis 失败: %w", err)
	}
	return &Redis{client: client, prefix: cfg.KeyPrefix}, nil
}

func (r *Redis) Client() *redis.Client { return r.client }

func (r *Redis) Key(parts ...string) string {
	return r.prefix + strings.Join(parts, ":")
}

func (r *Redis) Ping(ctx context.Context) error { return r.client.Ping(ctx).Err() }

func (r *Redis) Close() error { return r.client.Close() }
