package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"

	"zrt/internal/cache"
	"zrt/internal/config"
)

type staticLoginLockoutGate bool

func (gate staticLoginLockoutGate) LoginLockoutEnabled(context.Context) (bool, error) {
	return bool(gate), nil
}

func TestSessionTokenIsHashedInRedis(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient, err := cache.Open(context.Background(), config.Redis{
		URL: "redis://" + server.Addr() + "/0", KeyPrefix: "zrt:", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("连接测试 Redis 失败: %v", err)
	}
	defer redisClient.Close()

	store := NewSessionStore(redisClient, time.Hour)
	token, _, err := store.Create(context.Background(), "user-1", 3)
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	keys := server.Keys()
	if len(keys) != 1 || strings.Contains(keys[0], token) {
		t.Fatalf("Redis Key 不得包含原始会话凭据: %v", keys)
	}
	session, err := store.Get(context.Background(), token)
	if err != nil || session.UserID != "user-1" || session.AuthVersion != 3 {
		t.Fatalf("读取会话失败: session=%+v err=%v", session, err)
	}
	if err := store.Delete(context.Background(), token); err != nil {
		t.Fatalf("删除会话失败: %v", err)
	}
	if _, err := store.Get(context.Background(), token); err != ErrSessionNotFound {
		t.Fatalf("删除后的会话应失效: %v", err)
	}
}

func TestLoginRateLimiterHasFiniteWindow(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient, err := cache.Open(context.Background(), config.Redis{
		URL: "redis://" + server.Addr() + "/0", KeyPrefix: "zrt:", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("连接测试 Redis 失败: %v", err)
	}
	defer redisClient.Close()

	limiter := NewLoginRateLimiter(redisClient, 2, time.Minute, staticLoginLockoutGate(true))
	for range 2 {
		if err := limiter.RecordFailure(context.Background(), "admin", "127.0.0.1"); err != nil {
			t.Fatalf("记录登录失败失败: %v", err)
		}
	}
	blocked, _, err := limiter.Blocked(context.Background(), "admin", "127.0.0.1")
	if err != nil || !blocked {
		t.Fatalf("达到阈值后应阻止登录: blocked=%v err=%v", blocked, err)
	}
	server.FastForward(time.Minute + time.Second)
	blocked, _, err = limiter.Blocked(context.Background(), "admin", "127.0.0.1")
	if err != nil || blocked {
		t.Fatalf("限流窗口结束后应恢复登录: blocked=%v err=%v", blocked, err)
	}
}

func TestLoginRateLimiterDefaultsDisabled(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient, err := cache.Open(context.Background(), config.Redis{
		URL: "redis://" + server.Addr() + "/0", KeyPrefix: "zrt:", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("连接测试 Redis 失败: %v", err)
	}
	defer redisClient.Close()

	limiter := NewLoginRateLimiter(redisClient, 1, time.Minute, nil)
	if err := limiter.RecordFailure(context.Background(), "admin", "127.0.0.1"); err != nil {
		t.Fatalf("关闭状态不应记录失败次数: %v", err)
	}
	blocked, _, err := limiter.Blocked(context.Background(), "admin", "127.0.0.1")
	if err != nil || blocked {
		t.Fatalf("默认关闭时不应阻止登录: blocked=%v err=%v", blocked, err)
	}
	if keys := server.Keys(); len(keys) != 0 {
		t.Fatalf("默认关闭时不应写入登录失败计数: %v", keys)
	}
}

func TestLoginRateLimiterClearsExistingFailures(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient, err := cache.Open(context.Background(), config.Redis{
		URL: "redis://" + server.Addr() + "/0", KeyPrefix: "zrt:", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("连接测试 Redis 失败: %v", err)
	}
	defer redisClient.Close()

	limiter := NewLoginRateLimiter(redisClient, 1, time.Minute, staticLoginLockoutGate(true))
	for _, username := range []string{"admin", "operator"} {
		if err := limiter.RecordFailure(context.Background(), username, "127.0.0.1"); err != nil {
			t.Fatalf("记录登录失败失败: %v", err)
		}
	}
	if err := limiter.ClearAll(context.Background()); err != nil {
		t.Fatalf("清理登录失败计数失败: %v", err)
	}
	if keys := server.Keys(); len(keys) != 0 {
		t.Fatalf("登录失败计数未清理: %v", keys)
	}
}
