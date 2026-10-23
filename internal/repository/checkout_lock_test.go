package repository

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckoutLocksReclaimCompletedVersions(t *testing.T) {
	service := NewService(nil, nil, nil, nil, 1)
	for version := range 10_000 {
		release, err := service.acquireCheckoutLock(context.Background(), "workspace:"+strconv.Itoa(version))
		if err != nil {
			t.Fatal(err)
		}
		release()
		release()
	}
	if len(service.checkoutLocks) != 0 {
		t.Fatalf("已完成版本仍占用锁记录: %d", len(service.checkoutLocks))
	}
}

func TestCheckoutLockCancellationKeepsCurrentHolder(t *testing.T) {
	service := NewService(nil, nil, nil, nil, 1)
	const key = "workspace:shared-version"
	release, err := service.acquireCheckoutLock(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() {
		unlock, err := service.acquireCheckoutLock(ctx, key)
		if unlock != nil {
			unlock()
		}
		result <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		service.checkoutLocksMu.Lock()
		references := service.checkoutLocks[key].references
		service.checkoutLocksMu.Unlock()
		if references == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("等待者没有登记锁引用")
		}
		runtime.Gosched()
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("等待取消后错误不正确: %v", err)
	}
	service.checkoutLocksMu.Lock()
	lock := service.checkoutLocks[key]
	if lock == nil || lock.references != 1 {
		service.checkoutLocksMu.Unlock()
		t.Fatal("等待者取消后误删了持有者的锁")
	}
	service.checkoutLocksMu.Unlock()
	release()
	if len(service.checkoutLocks) != 0 {
		t.Fatalf("持有者释放后仍保留锁: %d", len(service.checkoutLocks))
	}
	if unlock, err := service.acquireCheckoutLock(ctx, "workspace:cancelled"); unlock != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消的请求不应获得锁: unlock=%v err=%v", unlock != nil, err)
	}
	if len(service.checkoutLocks) != 0 {
		t.Fatal("已取消请求遗留锁记录")
	}
}

func TestCheckoutLockMaintainsMutualExclusionDuringReclamation(t *testing.T) {
	service := NewService(nil, nil, nil, nil, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var active atomic.Int32
	var workers sync.WaitGroup
	for range 24 {
		workers.Go(func() {
			for range 100 {
				release, err := service.acquireCheckoutLock(ctx, "repository:shared")
				if err != nil {
					t.Error(err)
					return
				}
				if active.Add(1) != 1 {
					t.Error("同一路径存在多个持有者")
				}
				runtime.Gosched()
				active.Add(-1)
				release()
			}
		})
	}
	workers.Wait()
	if len(service.checkoutLocks) != 0 {
		t.Fatalf("并发任务完成后仍保留锁: %d", len(service.checkoutLocks))
	}
}

func TestCheckoutLockAllowsIndependentPathsAndConcurrentRelease(t *testing.T) {
	service := NewService(nil, nil, nil, nil, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := service.acquireCheckoutLock(ctx, "workspace:first")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first)
	second, err := service.acquireCheckoutLock(ctx, "workspace:second")
	if err != nil {
		t.Fatalf("独立工作区不应等待其他路径: %v", err)
	}
	t.Cleanup(second)
	var releases sync.WaitGroup
	for range 16 {
		releases.Go(first)
		releases.Go(second)
	}
	releases.Wait()
	if len(service.checkoutLocks) != 0 {
		t.Fatalf("重复并发释放后仍保留锁: %d", len(service.checkoutLocks))
	}
}

func BenchmarkCheckoutLockUniqueVersions(b *testing.B) {
	service := NewService(nil, nil, nil, nil, 1)
	ctx := context.Background()
	b.ReportAllocs()
	version := 0
	for b.Loop() {
		release, err := service.acquireCheckoutLock(ctx, "workspace:"+strconv.Itoa(version))
		if err != nil {
			b.Fatal(err)
		}
		release()
		version++
	}
	b.ReportMetric(float64(len(service.checkoutLocks)), "retained_locks")
}

func BenchmarkCheckoutLockContended(b *testing.B) {
	service := NewService(nil, nil, nil, nil, 1)
	ctx := context.Background()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			release, err := service.acquireCheckoutLock(ctx, "repository:shared")
			if err != nil {
				b.Error(err)
				return
			}
			release()
		}
	})
	b.ReportMetric(float64(len(service.checkoutLocks)), "retained_locks")
}
