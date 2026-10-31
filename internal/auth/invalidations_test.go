package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"edo/internal/cache"
	"edo/internal/config"
)

func invalidationTestRedis(t *testing.T, server *miniredis.Miniredis, db int, prefix string) *cache.Redis {
	t.Helper()
	client, err := cache.Open(context.Background(), config.Redis{URL: fmt.Sprintf("redis://%s/%d", server.Addr(), db), KeyPrefix: prefix, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestInvalidationsAreScopedByPrefixDBUserAndSession(t *testing.T) {
	server := miniredis.RunT(t)
	bus := NewInvalidations(invalidationTestRedis(t, server, 0, "edo:"), nil)
	otherDB := NewInvalidations(invalidationTestRedis(t, server, 1, "edo:"), nil)
	otherPrefix := NewInvalidations(invalidationTestRedis(t, server, 0, "isolated:"), nil)
	const token = "public-synthetic-session-canary-not-a-real-credential"
	type listener struct {
		bus         *Invalidations
		user, token string
		stream      <-chan *redis.Message
	}
	listeners := []listener{{bus: bus, user: "user-a", token: token}, {bus: bus, user: "user-a", token: "other-token"}, {bus: bus, user: "user-b", token: "third-token"}, {bus: otherDB, user: "user-a", token: token}, {bus: otherPrefix, user: "user-a", token: token}}
	for n := range listeners {
		sub, err := listeners[n].bus.Subscribe(context.Background(), listeners[n].user, listeners[n].token)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sub.Close() })
		listeners[n].stream = sub.Channel()
	}
	for _, channel := range server.PubSubChannels("") {
		if strings.Contains(channel, token) {
			t.Fatal("频道不得含原始会话凭据")
		}
	}
	for _, candidate := range []struct {
		name    string
		publish func()
		want    []bool
	}{
		{"用户", func() { bus.UserChanged(context.Background(), "user-a") }, []bool{true, true, false, false, false}},
		{"会话", func() { bus.SessionChanged(context.Background(), token) }, []bool{true, false, false, false, false}},
		{"策略", func() { bus.PolicyChanged(context.Background()) }, []bool{true, true, true, false, false}},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			candidate.publish()
			for n, listener := range listeners {
				if candidate.want[n] {
					select {
					case message := <-listener.stream:
						if message == nil || message.Payload != invalidationPayload {
							t.Fatal("仅允许固定失效负载")
						}
					case <-time.After(time.Second):
						t.Fatal("缺少目标身份通知")
					}
				} else {
					select {
					case <-listener.stream:
						t.Fatal("通知跨越身份、Redis DB 或前缀范围")
					case <-time.After(20 * time.Millisecond):
					}
				}
			}
		})
	}
}

func TestInvalidationsPublishSurvivesRequestCancellation(t *testing.T) {
	server := miniredis.RunT(t)
	bus := NewInvalidations(invalidationTestRedis(t, server, 0, "edo:"), nil)
	sub, err := bus.Subscribe(context.Background(), "user", "synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bus.UserChanged(ctx, "user")
	message, err := sub.ReceiveMessage(context.Background())
	if err != nil || message.Payload != invalidationPayload {
		t.Fatal("已提交变更不得因请求取消丢失通知")
	}
}

func TestInvalidationsPubSubReconnectsAfterHandshakeDeadline(t *testing.T) {
	server := miniredis.RunT(t)
	bus := NewInvalidations(invalidationTestRedis(t, server, 0, "edo:"), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	sub, err := bus.Subscribe(ctx, "user", "synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	stream := sub.ChannelWithSubscriptions()
	// 越过局部订阅期限后仍需使用调用者生命周期，随后仅重启本测试的 miniredis。
	time.Sleep(invalidationTimeout + 20*time.Millisecond)
	server.Close()
	if err := server.Restart(); err != nil {
		t.Fatal(err)
	}
	acks := make(map[string]bool)
	for len(acks) < 3 {
		select {
		case event := <-stream:
			if subscription, ok := event.(*redis.Subscription); ok && subscription.Kind == "subscribe" {
				acks[subscription.Channel] = true
			}
		case <-ctx.Done():
			t.Fatal("握手上下文取消后无法完整重订阅")
		}
	}
	bus.SessionChanged(ctx, "synthetic-token")
	select {
	case event := <-stream:
		message, ok := event.(*redis.Message)
		if !ok || message.Payload != invalidationPayload {
			t.Fatal("重连后未收到固定失效信号")
		}
	case <-ctx.Done():
		t.Fatal("重连后通知丢失")
	}
}

func TestInvalidationsFailureDoesNotExposeSessionOrReturnPublishError(t *testing.T) {
	server := miniredis.RunT(t)
	client := invalidationTestRedis(t, server, 0, "edo:")
	var output bytes.Buffer
	bus := NewInvalidations(client, slog.New(slog.NewTextHandler(&output, nil)))
	_ = client.Close()
	const token = "synthetic-secret-token-canary"
	bus.SessionChanged(context.Background(), token)
	sub, err := bus.Subscribe(context.Background(), "user", token)
	if sub != nil || !errors.Is(err, ErrInvalidationsUnavailable) {
		t.Fatal("订阅失败必须返回稳定不可用结果")
	}
	if strings.Contains(output.String(), token) || !strings.Contains(output.String(), "auth_invalidation_publish") || !strings.Contains(output.String(), "auth_invalidation_subscribe") {
		t.Fatal("诊断须保留操作而不能记录原始会话")
	}
}

func TestSessionDeletionsPublishTargetedInvalidation(t *testing.T) {
	for _, candidate := range []string{"正常删除", "损坏记录", "内部过期"} {
		t.Run(candidate, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := invalidationTestRedis(t, server, 0, "edo:")
			store := NewSessionStore(client, time.Hour)
			token, session, err := store.Create(context.Background(), "user", 1)
			if err != nil {
				t.Fatal(err)
			}
			sub, err := store.Invalidations().Subscribe(context.Background(), "user", token)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			switch candidate {
			case "正常删除":
				err = store.Delete(context.Background(), token)
			case "损坏记录":
				server.Set(store.key(token), "{")
				_, err = store.Get(context.Background(), token)
			case "内部过期":
				session.ExpiresAt = time.Now().Add(-time.Second)
				value := fmt.Sprintf(`{"user_id":"user","auth_version":1,"expires_at":%q}`, session.ExpiresAt.Format(time.RFC3339Nano))
				server.Set(store.key(token), value)
				_, err = store.Get(context.Background(), token)
			}
			if candidate == "正常删除" && err != nil {
				t.Fatal(err)
			}
			if candidate != "正常删除" && err == nil {
				t.Fatal("无效会话必须拒绝")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			message, err := sub.ReceiveMessage(ctx)
			if err != nil || message.Channel != store.invalidations.sessionChannel(token) || message.Payload != invalidationPayload {
				t.Fatal("删除后未通知准确会话")
			}
		})
	}
}
