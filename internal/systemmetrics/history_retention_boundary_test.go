package systemmetrics

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestHistoryRemovesExpiredSamplesFromInactiveNodes(t *testing.T) {
	service, server := testHistoryService(t)
	client := service.history.client
	service.ConfigureHistory(client, "test:inactive-retention", ":18080")
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Minute)
	if err := service.history.append(ctx, Snapshot{CollectedAt: start}); err != nil {
		t.Fatal(err)
	}
	// 这里只推进隔离单元测试的时钟，不能当成真实三天保留验收。
	server.FastForward(71 * time.Hour)
	last := start.Add(71 * time.Hour)
	if err := service.history.append(ctx, Snapshot{CollectedAt: last}); err != nil {
		t.Fatal(err)
	}
	server.FastForward(2 * time.Hour)
	now := start.Add(73 * time.Hour)
	replacement := &Service{logger: service.logger, latest: &Snapshot{}}
	replacement.ConfigureHistory(client, "test:inactive-retention", ":18081")
	if err := replacement.history.append(ctx, Snapshot{CollectedAt: now}); err != nil {
		t.Fatal(err)
	}
	cutoff := strconv.FormatInt(now.Add(-HistoryRetention).UnixMilli(), 10)
	expired, err := client.ZCount(ctx, service.history.key+":60s", "-inf", cutoff).Result()
	if err != nil {
		t.Fatal(err)
	}
	if expired != 0 {
		t.Fatalf("旧节点停止采集后仍保留 %d 条超过三天的采样", expired)
	}
	remaining, err := client.ZCard(ctx, service.history.key+":60s").Result()
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("不能为清理过期项而丢弃旧节点仍在保留期内的采样，实际剩余 %d 条", remaining)
	}
}
