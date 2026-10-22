package systemmetrics

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func testHistoryService(t *testing.T) (*Service, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	service := &Service{history: &historyStore{client: client, key: "test:node-a", nodeID: "node-a"}, latest: &Snapshot{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return service, server
}

func TestHistoryKeepsBoundedFineAndMinuteSamples(t *testing.T) {
	service, _ := testHistoryService(t)
	ctx := context.Background()
	start := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	for index := range 810 {
		point := Snapshot{CollectedAt: start.Add(time.Duration(index) * SampleInterval), Host: HostMetrics{CPUPercent: float64(index % 100)}}
		if err := service.history.append(ctx, point); err != nil {
			t.Fatal(err)
		}
	}
	now := start.Add(809 * SampleInterval)
	fine, err := service.historyAt(ctx, time.Hour, 0, now)
	if err != nil || len(fine.Points) > 721 || len(fine.Points) < 719 || fine.IntervalSeconds != 5 {
		t.Fatalf("细粒度历史不正确: samples=%d interval=%d err=%v", len(fine.Points), fine.IntervalSeconds, err)
	}
	coarse, err := service.historyAt(ctx, 6*time.Hour, 0, now)
	if err != nil || len(coarse.Points) != 68 || coarse.IntervalSeconds != 60 {
		t.Fatalf("分钟历史不正确: samples=%d interval=%d err=%v", len(coarse.Points), coarse.IntervalSeconds, err)
	}
	if !coarse.Points[len(coarse.Points)-1].CollectedAt.Equal(now) {
		t.Fatal("分钟桶没有保留最新真实采样")
	}
	for index := 1; index < len(fine.Points); index++ {
		if !fine.Points[index].CollectedAt.After(fine.Points[index-1].CollectedAt) {
			t.Fatal("历史采样没有按时间严格递增")
		}
	}
	delta, err := service.historyAt(ctx, time.Hour, now.Add(-SampleInterval).UnixMilli(), now)
	if err != nil || len(delta.Points) != 1 || !delta.Points[0].CollectedAt.Equal(now) {
		t.Fatalf("增量游标重复或遗漏采样: %+v err=%v", delta.Points, err)
	}
}

func TestHistorySurvivesServiceRestartAndSeparatesNodes(t *testing.T) {
	service, server := testHistoryService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	point := Snapshot{CollectedAt: now, Unavailable: []string{"queue"}, Host: HostMetrics{CPUPercent: 1.2}}
	if err := service.history.append(ctx, point); err != nil {
		t.Fatal(err)
	}
	restarted := &Service{history: service.history, latest: &Snapshot{CollectedAt: now}}
	result, err := restarted.historyAt(ctx, 15*time.Minute, 0, now)
	if err != nil || len(result.Points) != 1 || len(result.Points[0].Unavailable) != 1 {
		t.Fatalf("重启后历史或不可用标记丢失: %+v err=%v", result, err)
	}
	other := &Service{history: &historyStore{client: service.history.client, key: "test:node-b", nodeID: "node-b"}, latest: &Snapshot{}}
	result, err = other.historyAt(ctx, 15*time.Minute, 0, now)
	if err != nil || len(result.Points) != 0 {
		t.Fatal("不同节点的监控历史发生混合")
	}
	server.FastForward(73 * time.Hour)
	result, err = restarted.historyAt(ctx, HistoryRetention, 0, now.Add(73*time.Hour))
	if err != nil || len(result.Points) != 0 {
		t.Fatal("停止采集后的历史没有按保留期过期")
	}
}

func TestHistoryTrimsExpiredSamplesWhileCollecting(t *testing.T) {
	service, _ := testHistoryService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for _, at := range []time.Time{now.Add(-73 * time.Hour), now.Add(-49 * time.Hour), now.Add(-2 * time.Hour), now} {
		if err := service.history.append(ctx, Snapshot{CollectedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := service.historyAt(ctx, HistoryRetention, 0, now)
	if err != nil || len(result.Points) != 3 || result.IntervalSeconds != 60 {
		t.Fatalf("历史保留边界不正确: count=%d err=%v", len(result.Points), err)
	}
	if count := service.history.client.ZCard(ctx, service.history.key+":5s").Val(); count != 1 {
		t.Fatalf("一小时前的细粒度采样未清理: %d", count)
	}
	if count := service.history.client.ZCard(ctx, service.history.key+":60s").Val(); count != 3 {
		t.Fatalf("三天前的采样未清理，或两天前的采样被提前删除: %d", count)
	}
}

func TestHistoryRejectsUnboundedAndFutureQueries(t *testing.T) {
	service, _ := testHistoryService(t)
	now := time.Now().UTC()
	for _, query := range []struct {
		window time.Duration
		after  int64
	}{{time.Second, 0}, {48 * time.Hour, 0}, {time.Hour, -1}, {time.Hour, now.Add(time.Hour).UnixMilli()}} {
		if _, err := service.historyAt(context.Background(), query.window, query.after, now); !errors.Is(err, ErrInvalidHistoryRange) {
			t.Fatalf("无效查询未拒绝: %+v err=%v", query, err)
		}
	}
}

func TestCollectorRunsWithoutBrowserAndStopsWithContext(t *testing.T) {
	db, sqlDB, logger := openMetricsTestDB(t, "metrics_history_collector")
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	service := New(db, sqlDB, testWorkerStats{}, testQueueStats{}, logger)
	service.ConfigureHistory(client, "test:collector", ":8080")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for client.ZCard(context.Background(), service.history.key+":5s").Val() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if client.ZCard(context.Background(), service.history.key+":5s").Val() != 1 {
		t.Fatal("没有浏览器查询时后台未采集")
	}
	first, second := service.Current(ctx), service.Current(ctx)
	if !first.CollectedAt.Equal(second.CollectedAt) {
		t.Fatal("浏览器读取改变了采样时间")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("采样协程没有随服务取消退出")
	}
}
