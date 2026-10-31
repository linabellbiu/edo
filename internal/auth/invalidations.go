package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"edo/internal/cache"
)

const invalidationTimeout = 3 * time.Second
const invalidationPayload = "stale"

var ErrInvalidationsUnavailable = errors.New("身份变更通知暂不可用，请稍后重试")

// Invalidations 只通知身份快照需要重读；权限和身份仍由正常数据库校验决定。
// 复用已配置的 go-redis PubSub，不持有额外连接池或后台发送协程。
type Invalidations struct {
	redis  *cache.Redis
	client *redis.Client
	logger *slog.Logger
}

func NewInvalidations(redisClient *cache.Redis, logger *slog.Logger) *Invalidations {
	if logger == nil {
		logger = slog.Default()
	}
	result := &Invalidations{redis: redisClient, logger: logger}
	if redisClient != nil {
		result.client = redisClient.Client().WithTimeout(invalidationTimeout)
	}
	return result
}

func (i *Invalidations) channel(kind, identity string) string {
	return i.redis.Key("auth", "invalidations", strconv.Itoa(i.client.Options().DB), kind, identity)
}

func (i *Invalidations) policyChannel() string {
	return i.channel("policy", "all")
}

func (i *Invalidations) sessionChannel(token string) string {
	digest := sha256.Sum256([]byte(token))
	return i.channel("session", hex.EncodeToString(digest[:]))
}

func (i *Invalidations) PolicyChanged(ctx context.Context) {
	if i != nil && i.client != nil {
		i.publish(ctx, "policy", i.policyChannel())
	}
}

func (i *Invalidations) UserChanged(ctx context.Context, userID string) {
	if i != nil && i.client != nil && userID != "" {
		i.publish(ctx, "user", i.channel("user", userID))
	}
}

func (i *Invalidations) SessionChanged(ctx context.Context, token string) {
	if i != nil && i.client != nil && token != "" {
		i.publish(ctx, "session", i.sessionChannel(token))
	}
}

func (i *Invalidations) publish(ctx context.Context, kind, channel string) {
	// 数据库已提交后，即便原请求取消，仍同步尝试通知；总期限固定，不重放业务写入。
	notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), invalidationTimeout)
	defer cancel()
	if err := i.client.Publish(notifyCtx, channel, invalidationPayload).Err(); err != nil {
		i.logger.ErrorContext(notifyCtx, "发布身份失效通知失败", "operation", "auth_invalidation_publish", "kind", kind, "err", err)
	}
}

// Subscribe 确认三个频道全部订阅成功后才交给调用者，调用者负责关闭返回的 PubSub。
func (i *Invalidations) Subscribe(ctx context.Context, userID, token string) (*redis.PubSub, error) {
	if i == nil || i.client == nil || userID == "" || token == "" {
		return nil, ErrInvalidationsUnavailable
	}
	subCtx, cancel := context.WithTimeout(ctx, invalidationTimeout)
	defer cancel()
	channels := []string{i.policyChannel(), i.channel("user", userID), i.sessionChannel(token)}
	pubsub := i.client.Subscribe(subCtx)
	fail := func(err error) (*redis.PubSub, error) {
		if closeErr := pubsub.Close(); closeErr != nil {
			i.logger.WarnContext(ctx, "关闭未完成的身份通知订阅失败", "operation", "auth_invalidation_subscribe_close", "err", closeErr)
		}
		i.logger.ErrorContext(ctx, "订阅身份失效通知失败", "operation", "auth_invalidation_subscribe", "err", err)
		return nil, ErrInvalidationsUnavailable
	}
	if err := pubsub.Subscribe(subCtx, channels...); err != nil {
		return fail(err)
	}
	pending := make(map[string]bool, len(channels))
	for _, channel := range channels {
		pending[channel] = true
	}
	deadline, _ := subCtx.Deadline()
	for len(pending) != 0 {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fail(context.DeadlineExceeded)
		}
		// go-redis 会把首次 Receive 的上下文用于 Channel 重连，不能保存即将取消的握手上下文。
		event, err := pubsub.ReceiveTimeout(ctx, remaining)
		if err != nil {
			return fail(err)
		}
		if err := subCtx.Err(); err != nil {
			return fail(err)
		}
		if subscription, ok := event.(*redis.Subscription); ok && subscription.Kind == "subscribe" {
			delete(pending, subscription.Channel)
		}
	}
	return pubsub, nil
}
