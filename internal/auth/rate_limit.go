package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"edo/internal/cache"
)

type LoginRateLimiter struct {
	redis      *cache.Redis
	maxFailure int64
	window     time.Duration
	gate       LoginLockoutGate
}

type LoginLockoutGate interface {
	LoginLockoutEnabled(context.Context) (bool, error)
}

func NewLoginRateLimiter(redisClient *cache.Redis, maxFailure int, window time.Duration, gate LoginLockoutGate) *LoginRateLimiter {
	return &LoginRateLimiter{redis: redisClient, maxFailure: int64(maxFailure), window: window, gate: gate}
}

func (l *LoginRateLimiter) Blocked(ctx context.Context, username, clientIP string) (bool, time.Duration, error) {
	enabled, err := l.enabled(ctx)
	if err != nil {
		return false, 0, err
	}
	if !enabled {
		return false, 0, nil
	}
	key := l.key(username, clientIP)
	count, err := l.redis.Client().Get(ctx, key).Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return false, 0, fmt.Errorf("读取登录限流状态失败: %w", err)
	}
	if count < l.maxFailure {
		return false, 0, nil
	}
	ttl, err := l.redis.Client().TTL(ctx, key).Result()
	if err != nil {
		return false, 0, fmt.Errorf("读取登录限流时间失败: %w", err)
	}
	return true, ttl, nil
}

func (l *LoginRateLimiter) RecordFailure(ctx context.Context, username, clientIP string) error {
	enabled, err := l.enabled(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	key := l.key(username, clientIP)
	_, err = l.redis.Client().TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Incr(ctx, key)
		pipe.Expire(ctx, key, l.window)
		return nil
	})
	if err != nil {
		return fmt.Errorf("记录登录失败次数失败: %w", err)
	}
	return nil
}

func (l *LoginRateLimiter) ClearAll(ctx context.Context) error {
	namespace := l.redis.Key("login", "failure") + ":"
	// MATCH 使用 glob，配置前缀中的特殊字符必须按字面匹配。
	pattern := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`).Replace(namespace) + "*"
	iterator := l.redis.Client().Scan(ctx, 0, pattern, 100).Iterator()
	keys := make([]string, 0, 100)
	for iterator.Next(ctx) {
		key := iterator.Val()
		// 枚举结果仍需核对准确命名空间及本模块生成的 SHA256 计数键格式。
		digest, owned := strings.CutPrefix(key, namespace)
		if !owned || len(digest) != sha256.Size*2 || strings.Trim(digest, "0123456789abcdef") != "" {
			continue
		}
		keys = append(keys, key)
		if len(keys) == 100 {
			if err := l.redis.Client().Del(ctx, keys...).Err(); err != nil {
				return fmt.Errorf("清理登录失败计数失败: %w", err)
			}
			keys = keys[:0]
		}
	}
	if err := iterator.Err(); err != nil {
		return fmt.Errorf("扫描登录失败计数失败: %w", err)
	}
	if len(keys) > 0 {
		if err := l.redis.Client().Del(ctx, keys...).Err(); err != nil {
			return fmt.Errorf("清理登录失败计数失败: %w", err)
		}
	}
	return nil
}

func (l *LoginRateLimiter) Reset(ctx context.Context, username, clientIP string) error {
	if err := l.redis.Client().Del(ctx, l.key(username, clientIP)).Err(); err != nil {
		return fmt.Errorf("清理登录限流状态失败: %w", err)
	}
	return nil
}

func (l *LoginRateLimiter) key(username, clientIP string) string {
	identity := strings.ToLower(strings.TrimSpace(username)) + "\x00" + clientIP
	digest := sha256.Sum256([]byte(identity))
	return l.redis.Key("login", "failure", hex.EncodeToString(digest[:]))
}

func (l *LoginRateLimiter) enabled(ctx context.Context) (bool, error) {
	if l.gate == nil {
		return false, nil
	}
	enabled, err := l.gate.LoginLockoutEnabled(ctx)
	if err != nil {
		return false, fmt.Errorf("读取登录锁定设置失败: %w", err)
	}
	return enabled, nil
}
