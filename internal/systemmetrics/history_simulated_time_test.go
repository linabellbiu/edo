package systemmetrics

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestHistorySimulatedCutoffPhysicalCleanup(t *testing.T) {
	service, _ := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	service.ConfigureHistory(client, "test:simulated-cutoff", ":18080")
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-HistoryRetention)
	key := service.history.key + ":60s"
	for _, offset := range []time.Duration{-time.Millisecond, 0, time.Millisecond} {
		point := Snapshot{CollectedAt: cutoff.Add(offset)}
		data, err := json.Marshal(point)
		if err != nil {
			t.Fatal(err)
		}
		// 直接准备有效历史JSON，避免append的同分钟合并先移除边界样本。
		if err := client.ZAdd(ctx, key, redis.Z{Score: float64(point.CollectedAt.UnixMilli()), Member: string(data)}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := client.ZCard(ctx, key).Result(); err != nil || count != 3 {
		t.Fatalf("边界前置必须包含三个物理样本: count=%d err=%v", count, err)
	}
	hook := &historyCleanupHook{}
	service.history.cleanup.client.AddHook(hook)
	if err := service.history.trimInactive(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(hook.scripts) == 0 {
		t.Fatal("必须经过生产Lua清理，不能只靠历史查询过滤")
	}
	if count, err := client.ZCount(ctx, key, "-inf", strconv.FormatInt(cutoff.UnixMilli(), 10)).Result(); err != nil || count != 0 {
		t.Fatalf("截止前及恰好截止的物理样本必须删除: count=%d err=%v", count, err)
	}
	if count, err := client.ZCard(ctx, key).Result(); err != nil || count != 1 {
		t.Fatalf("截止后一毫秒的样本必须保留: count=%d err=%v", count, err)
	}
	remaining, err := client.ZRangeWithScores(ctx, key, 0, -1).Result()
	if err != nil || len(remaining) != 1 || remaining[0].Score != float64(cutoff.Add(time.Millisecond).UnixMilli()) {
		t.Fatalf("保留的物理score错误: samples=%v err=%v", remaining, err)
	}
	var point Snapshot
	if err := json.Unmarshal([]byte(remaining[0].Member.(string)), &point); err != nil || !point.CollectedAt.Equal(cutoff.Add(time.Millisecond)) {
		t.Fatalf("保留样本的真实时间错误: collected_at=%v err=%v", point.CollectedAt, err)
	}
	if score, err := client.ZScore(ctx, service.history.retentionIndex(), key).Result(); err != nil || score != float64(now.Add(time.Millisecond).UnixMilli()) {
		t.Fatalf("应按最早有效样本重新排期: score=%v err=%v", score, err)
	}
}

func TestHistorySimulatedTTLPhysicalExpiry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		suffix    string
		retention time.Duration
		padding   time.Duration
	}{
		{"细粒度", ":5s", time.Hour, time.Minute},
		{"分钟历史", ":60s", HistoryRetention, 0},
		{"到期索引", ":retention", HistoryRetention, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, server := testHistoryService(t)
			client, ctx := service.history.client, context.Background()
			service.ConfigureHistory(client, "test:simulated-ttl", ":18080")
			if err := service.history.append(ctx, Snapshot{CollectedAt: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}); err != nil {
				t.Fatal(err)
			}
			key := service.history.key + tc.suffix
			if tc.suffix == ":retention" {
				key = service.history.retentionIndex()
			}
			ttl, err := client.PTTL(ctx, key).Result()
			if err != nil || ttl != tc.retention+tc.padding {
				t.Fatalf("生产写入的TTL或宽限变化: ttl=%v retention=%v padding=%v err=%v", ttl, tc.retention, tc.padding, err)
			}
			t.Logf("生产TTL=%v，保留期=%v，宽限=%v；仅推进本测试miniredis", ttl, tc.retention, ttl-tc.retention)
			server.FastForward(ttl - time.Millisecond)
			if count, err := client.Exists(ctx, key).Result(); err != nil || count != 1 {
				t.Fatalf("TTL截止前一毫秒不能提前删除: exists=%d err=%v", count, err)
			}
			if left, err := client.PTTL(ctx, key).Result(); err != nil || left != time.Millisecond {
				t.Fatalf("截止前剩余TTL必须真实为一毫秒: ttl=%v err=%v", left, err)
			}
			server.FastForward(time.Millisecond)
			if count, err := client.Exists(ctx, key).Result(); err != nil || count != 0 {
				t.Fatalf("TTL到期必须物理删除键，不能只查空时间窗口: exists=%d err=%v", count, err)
			}
			if left, err := client.PTTL(ctx, key).Result(); err != nil || left >= 0 {
				t.Fatalf("删除后的键不能还有TTL: ttl=%v err=%v", left, err)
			}
		})
	}
}

func TestHistorySimulatedRestartAndGap(t *testing.T) {
	service, server := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	const namespace, listener = "test:simulated-restart-gap", ":18080"
	service.ConfigureHistory(client, namespace, listener)
	start := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	if err := service.history.append(ctx, Snapshot{CollectedAt: start, Process: ProcessMetrics{UptimeSeconds: 10}}); err != nil {
		t.Fatal(err)
	}
	server.FastForward(71 * time.Hour)
	before := Snapshot{
		CollectedAt: start.Add(71 * time.Hour), Process: ProcessMetrics{RSSBytes: 4096, UptimeSeconds: 1000},
		Runtime: RuntimeMetrics{GCCycles: 99}, Unavailable: []string{"queue"},
	}
	if err := service.history.append(ctx, before); err != nil {
		t.Fatal(err)
	}
	// 模拟停止采集两小时；不改Go、宿主机或真实Redis时钟。
	server.FastForward(2 * time.Hour)
	coarseKey := service.history.key + ":60s"
	if count, err := client.ZCard(ctx, coarseKey).Result(); err != nil || count != 2 {
		t.Fatalf("重建前必须仍有过期和有效两个物理样本: count=%d err=%v", count, err)
	}
	after := Snapshot{
		CollectedAt: start.Add(73 * time.Hour), Process: ProcessMetrics{RSSBytes: 2048, UptimeSeconds: 1},
		Runtime: RuntimeMetrics{GCCycles: 1},
	}
	restarted := &Service{logger: service.logger, latest: &after}
	restarted.ConfigureHistory(client, namespace, listener)
	if restarted.history == service.history || restarted.history.key != service.history.key || restarted.history.nodeID != service.history.nodeID || restarted.history.cleanup.registration == service.history.cleanup.registration {
		t.Fatal("必须重新构造同logical node的Service和独立注册，不能复用原history指针")
	}
	if err := restarted.history.append(ctx, after); err != nil {
		t.Fatal(err)
	}
	cutoff := strconv.FormatInt(after.CollectedAt.Add(-HistoryRetention).UnixMilli(), 10)
	if count, err := client.ZCount(ctx, coarseKey, "-inf", cutoff).Result(); err != nil || count != 0 {
		t.Fatalf("新Service恢复采集后必须物理移除过期样本: count=%d err=%v", count, err)
	}
	if count, err := client.ZCard(ctx, coarseKey).Result(); err != nil || count != 2 {
		t.Fatalf("只保留有效旧样本和新样本，不在断档补零: count=%d err=%v", count, err)
	}
	history, err := restarted.historyAt(ctx, HistoryRetention, 0, after.CollectedAt)
	if err != nil || len(history.Points) != 2 || history.IntervalSeconds != 60 {
		t.Fatalf("重建后历史数量或分辨率错误: history=%+v err=%v", history, err)
	}
	oldPoint, newPoint := history.Points[0], history.Points[1]
	if !oldPoint.CollectedAt.Equal(before.CollectedAt) || !newPoint.CollectedAt.Equal(after.CollectedAt) || newPoint.CollectedAt.Sub(oldPoint.CollectedAt) != 2*time.Hour {
		t.Fatalf("两小时断档必须保持原始时间而非插值: old=%v new=%v", oldPoint.CollectedAt, newPoint.CollectedAt)
	}
	if len(oldPoint.Unavailable) != 1 || oldPoint.Unavailable[0] != "queue" || oldPoint.Process.RSSBytes != 4096 || newPoint.Process.RSSBytes != 2048 {
		t.Fatal("缺测标记或已有有效数值被改写")
	}
	if oldPoint.Process.UptimeSeconds != 1000 || newPoint.Process.UptimeSeconds != 1 || oldPoint.Runtime.GCCycles != 99 || newPoint.Runtime.GCCycles != 1 {
		t.Fatal("重建后的uptime及累计GC应保留真实回退，不得接成旧进程累计值")
	}
}
