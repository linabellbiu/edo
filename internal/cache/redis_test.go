package cache

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"

	"edo/internal/config"
)

func TestRedisCommandsHonorCallerContext(t *testing.T) {
	server := miniredis.RunT(t)
	client, err := Open(context.Background(), config.Redis{URL: "redis://" + server.Addr() + "/0", KeyPrefix: "edo:", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if !client.Client().Options().ContextTimeoutEnabled {
		t.Fatal("Redis 命令必须使用调用者总期限而非无期限后台上下文")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Client().Ping(ctx).Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的命令未被拒绝: %v", err)
	}
}

func TestRedisInvalidURLDoesNotExposeCredentials(t *testing.T) {
	const canary = "synthetic-url-password-canary"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	client, err := Open(context.Background(), config.Redis{URL: "unknown://user:" + canary + "@localhost", Timeout: time.Second})
	if client != nil || err == nil || strings.Contains(err.Error(), canary) || strings.Contains(logs.String(), canary) {
		t.Fatal("无效地址不得回显认证配置")
	}
	if !strings.Contains(logs.String(), "redis_config_parse") || !strings.Contains(logs.String(), "invalid_url") {
		t.Fatal("格式错误必须保留安全操作诊断")
	}
}
