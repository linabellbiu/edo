package messaging

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"edo/internal/config"
)

type NATS struct {
	conn   *nats.Conn
	js     jetstream.JetStream
	config config.NATS
	logger *slog.Logger
}

type QueueStats struct {
	Connected       bool   `json:"connected"`
	StoredMessages  uint64 `json:"stored_messages"`
	StoredBytes     uint64 `json:"stored_bytes"`
	Consumers       int    `json:"consumers"`
	PendingMessages uint64 `json:"pending_messages"`
	AckPending      int    `json:"ack_pending"`
	Redelivered     int    `json:"redelivered"`
	WaitingPulls    int    `json:"waiting_pulls"`
	DeadMessages    uint64 `json:"dead_messages"`
	DeadBytes       uint64 `json:"dead_bytes"`
}

func Open(ctx context.Context, cfg config.NATS, logger *slog.Logger) (*NATS, error) {
	conn, err := nats.Connect(
		cfg.URL,
		nats.Name("edo"),
		nats.Timeout(cfg.Timeout),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			logger.Error("NATS 异步操作失败", "operation", "nats_async", "err", err)
		}),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logger.Warn("NATS 连接断开", "operation", "nats_disconnect", "err", err)
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			logger.Info("NATS 已重新连接", "operation", "nats_reconnect")
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("连接 NATS 失败: %w", err)
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("初始化 JetStream 失败: %w", err)
	}
	client := &NATS{conn: conn, js: js, config: cfg, logger: logger}
	if err := client.Ping(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return client, nil
}

func (n *NATS) EnsureStreams(ctx context.Context) error {
	_, err := n.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        n.config.Stream,
		Description: "EDO 持久化任务队列",
		Subjects:    []string{n.config.SubjectPrefix + ".>"},
		Retention:   jetstream.WorkQueuePolicy,
		Storage:     jetstream.FileStorage,
		MaxAge:      n.config.MaxAge,
		MaxBytes:    n.config.MaxBytes,
		MaxMsgSize:  1024 * 1024,
		Replicas:    n.config.Replicas,
		Duplicates:  2 * time.Minute,
		Discard:     jetstream.DiscardOld,
	})
	if err != nil {
		return fmt.Errorf("创建或更新 NATS 任务 Stream 失败: %w", err)
	}
	_, err = n.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        n.config.DeadStream,
		Description: "EDO 任务死信",
		Subjects:    []string{n.config.DeadSubject},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		MaxAge:      30 * 24 * time.Hour,
		MaxBytes:    n.config.DeadMaxBytes,
		MaxMsgSize:  1024 * 1024,
		Replicas:    n.config.Replicas,
		Discard:     jetstream.DiscardOld,
	})
	if err != nil {
		return fmt.Errorf("创建或更新 NATS 死信 Stream 失败: %w", err)
	}
	return nil
}

func (n *NATS) EnsureConsumer(ctx context.Context, durable, filter string, maxAttempts int) (jetstream.Consumer, error) {
	if maxAttempts < 1 {
		return nil, fmt.Errorf("Consumer %s 的最大执行次数必须大于 0", durable)
	}
	consumer, err := n.js.CreateOrUpdateConsumer(ctx, n.config.Stream, jetstream.ConsumerConfig{
		Name:          durable,
		Durable:       durable,
		Description:   "EDO Worker " + durable,
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxDeliver:    maxAttempts,
		BackOff:       retryBackoff(maxAttempts),
		FilterSubject: filter,
		MaxAckPending: 100,
		ReplayPolicy:  jetstream.ReplayInstantPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("创建或更新 NATS Consumer %s 失败: %w", durable, err)
	}
	return consumer, nil
}

func (n *NATS) Publish(ctx context.Context, subject string, payload []byte, messageID string) error {
	publishCtx, cancel := context.WithTimeout(ctx, n.config.Timeout)
	defer cancel()
	if _, err := n.js.Publish(publishCtx, subject, payload, jetstream.WithMsgID(messageID)); err != nil {
		return fmt.Errorf("发布 NATS 消息失败: %w", err)
	}
	return nil
}

func (n *NATS) PublishDeadLetter(ctx context.Context, payload []byte, messageID string) error {
	return n.Publish(ctx, n.config.DeadSubject, payload, messageID)
}

func (n *NATS) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, n.config.Timeout)
	defer cancel()
	if _, err := n.js.AccountInfo(pingCtx); err != nil {
		return fmt.Errorf("JetStream 健康检查失败: %w", err)
	}
	return nil
}

func (n *NATS) QueueStats(ctx context.Context, consumerName string) (QueueStats, error) {
	stats := QueueStats{Connected: n.conn.IsConnected()}
	if !stats.Connected {
		return stats, fmt.Errorf("NATS 连接当前不可用")
	}

	stream, err := n.js.Stream(ctx, n.config.Stream)
	if err != nil {
		return stats, fmt.Errorf("读取 NATS 任务 Stream 失败: %w", err)
	}
	streamInfo, err := stream.Info(ctx)
	if err != nil {
		return stats, fmt.Errorf("读取 NATS 任务 Stream 状态失败: %w", err)
	}
	stats.StoredMessages = streamInfo.State.Msgs
	stats.StoredBytes = streamInfo.State.Bytes
	stats.Consumers = streamInfo.State.Consumers

	consumer, err := stream.Consumer(ctx, consumerName)
	if err != nil {
		return stats, fmt.Errorf("读取 NATS Consumer 失败: %w", err)
	}
	consumerInfo, err := consumer.Info(ctx)
	if err != nil {
		return stats, fmt.Errorf("读取 NATS Consumer 状态失败: %w", err)
	}
	stats.PendingMessages = consumerInfo.NumPending
	stats.AckPending = consumerInfo.NumAckPending
	stats.Redelivered = consumerInfo.NumRedelivered
	stats.WaitingPulls = consumerInfo.NumWaiting

	deadStream, err := n.js.Stream(ctx, n.config.DeadStream)
	if err != nil {
		return stats, fmt.Errorf("读取 NATS 死信 Stream 失败: %w", err)
	}
	deadInfo, err := deadStream.Info(ctx)
	if err != nil {
		return stats, fmt.Errorf("读取 NATS 死信 Stream 状态失败: %w", err)
	}
	stats.DeadMessages = deadInfo.State.Msgs
	stats.DeadBytes = deadInfo.State.Bytes
	return stats, nil
}

// PurgeDeadLetters 只清理配置的死信主题，不删除 Stream，也不会影响正常任务队列。
func (n *NATS) PurgeDeadLetters(ctx context.Context) (uint64, error) {
	if !n.conn.IsConnected() {
		return 0, fmt.Errorf("NATS 连接当前不可用")
	}
	purgeCtx, cancel := context.WithTimeout(ctx, n.config.Timeout)
	defer cancel()
	stream, err := n.js.Stream(purgeCtx, n.config.DeadStream)
	if err != nil {
		return 0, fmt.Errorf("读取 NATS 死信 Stream 失败: %w", err)
	}
	purged, err := purgeDeadLetterStream(purgeCtx, stream, n.config.DeadSubject)
	if err != nil {
		return 0, fmt.Errorf("清空 NATS 死信 Stream 失败: %w", err)
	}
	return purged, nil
}

type deadLetterStream interface {
	Info(context.Context, ...jetstream.StreamInfoOpt) (*jetstream.StreamInfo, error)
	Purge(context.Context, ...jetstream.StreamPurgeOpt) error
}

func purgeDeadLetterStream(ctx context.Context, stream deadLetterStream, subject string) (uint64, error) {
	info, err := stream.Info(ctx)
	if err != nil {
		return 0, err
	}
	if err := stream.Purge(ctx, jetstream.WithPurgeSubject(subject)); err != nil {
		return 0, err
	}
	return info.State.Msgs, nil
}

func (n *NATS) Close() error {
	if err := n.conn.Drain(); err != nil {
		n.conn.Close()
		return err
	}
	return nil
}

func retryBackoff(maxAttempts int) []time.Duration {
	if maxAttempts <= 1 {
		return nil
	}
	configured := []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute}
	retries := maxAttempts - 1
	if retries > len(configured) {
		retries = len(configured)
	}
	return configured[:retries]
}
