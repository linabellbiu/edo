package cache

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshotStoreAgeFailureAndCapacity(t *testing.T) {
	const fresh, retry, retain = time.Minute, 15 * time.Second, 10 * time.Minute
	start := time.Now().UTC()
	store := NewSnapshotStore[string](2, fresh, retain)
	if value, status := store.Get("a", start); value != "" || status.State != SnapshotPending || status.CollectedAt != nil {
		t.Fatalf("意外的首次读取状态: %+v", status)
	}
	store.Put("a", "成功结果", start)
	store.Fail("a", start.Add(fresh/2))
	if value, status := store.Get("a", start.Add(fresh/2)); value != "成功结果" || status.State != SnapshotStale ||
		status.CollectedAt == nil || !status.CollectedAt.Equal(start) {
		t.Fatalf("采集失败改写了最后成功时间: %+v", status)
	}
	if store.Due("a", start.Add(fresh/2), fresh, retry) || !store.Due("a", start.Add(fresh/2+retry), fresh, retry) {
		t.Fatal("失败重试间隔错误")
	}
	store.Fail("b", start.Add(fresh))
	if value, status := store.Get("b", start.Add(fresh)); value != "" || status.State != SnapshotUnavailable || status.CollectedAt != nil {
		t.Fatalf("初次失败产生了虚假数据: %+v", status)
	}
	store.Fail("a", start.Add(retain))
	if value, status := store.Get("a", start.Add(retain)); value != "" || status.State != SnapshotUnavailable || status.CollectedAt != nil {
		t.Fatalf("过期数据仍然可读: %+v", status)
	}
	store.Put("c", "最新结果", start.Add(retain))
	if _, status := store.Get("b", start.Add(retain)); status.State != SnapshotPending {
		t.Fatalf("超出容量后没有淘汰最旧条目: %+v", status)
	}
}

func TestSnapshotWorkersContinueScanningWhileOneTargetIsInFlight(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	fastUpdates, slowStarted, done := make(chan struct{}, 2), make(chan struct{}), make(chan error, 1)
	var slowCalls atomic.Int32
	go func() {
		done <- RunSnapshotWorkers(ctx, 5*time.Millisecond, "test_snapshots", 2,
			func(_ context.Context, yield func(string, string) bool) error {
				if yield("slow", "slow") {
					yield("fast", "fast")
				}
				return nil
			}, func(ctx context.Context, target string) {
				if target == "slow" {
					if slowCalls.Add(1) == 1 {
						close(slowStarted)
					}
					<-ctx.Done()
					return
				}
				select {
				case fastUpdates <- struct{}{}:
				default:
				}
			})
	}()
	select {
	case <-slowStarted:
	case <-ctx.Done():
		t.Fatal("慢目标没有启动")
	}
	for range 2 {
		select {
		case <-fastUpdates:
		case <-ctx.Done():
			t.Fatal("慢目标阻塞了其他目标后续扫描")
		}
	}
	cancel()
	if err := <-done; err != nil || slowCalls.Load() != 1 {
		t.Fatalf("采集重复调度或没有干净停止: calls=%d err=%v", slowCalls.Load(), err)
	}
}
