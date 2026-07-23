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

	"zrt/internal/cache"
)

type LoginRateLimiter struct {
	redis      *cache.Redis
	maxFailure int64
	window     time.Duration
}

func NewLoginRateLimiter(redisClient *cache.Redis, maxFailure int, window time.Duration) *LoginRateLimiter {
	return &LoginRateLimiter{redis: redisClient, maxFailure: int64(maxFailure), window: window}
}

func (l *LoginRateLimiter) Blocked(ctx context.Context, username, clientIP string) (bool, time.Duration, error) {
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
	key := l.key(username, clientIP)
	_, err := l.redis.Client().TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Incr(ctx, key)
		pipe.Expire(ctx, key, l.window)
		return nil
	})
	if err != nil {
		return fmt.Errorf("记录登录失败次数失败: %w", err)
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
