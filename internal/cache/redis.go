package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
		// ParseURL 的原错误可能包含带口令的输入，诊断只保留稳定分类和类型。
		slog.ErrorContext(ctx, "Redis 连接配置无效", "operation", "redis_config_parse", "error_kind", "invalid_url", "error_type", fmt.Sprintf("%T", err), "err", errors.New("连接 URL 格式无效"))
		return nil, errors.New("Redis 连接配置无效")
	}
	options.Protocol = 2
	// 命令遵守调用方的取消及总期限，避免读写超时分别累加突破业务预算。
	options.ContextTimeoutEnabled = true
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
