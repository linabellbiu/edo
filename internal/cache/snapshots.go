package cache

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	SnapshotPending     = "pending"
	SnapshotOK          = "ok"
	SnapshotStale       = "stale"
	SnapshotUnavailable = "unavailable"
)

type SnapshotStatus struct {
	State       string     `json:"state"`
	CollectedAt *time.Time `json:"collected_at"`
}

type snapshotEntry[T any] struct {
	value       T
	collectedAt time.Time
	lastAttempt time.Time
	failed      bool
}

// SnapshotStore 保存后台采集好的只读值，读取不触发采集。
// 值应视为不可变，若调用者需要修改，须复制后使用。
type SnapshotStore[T any] struct {
	mu        sync.RWMutex
	entries   map[string]snapshotEntry[T]
	limit     int
	freshFor  time.Duration
	retainFor time.Duration
}

func NewSnapshotStore[T any](limit int, freshFor, retainFor time.Duration) *SnapshotStore[T] {
	return &SnapshotStore[T]{entries: make(map[string]snapshotEntry[T]), limit: limit, freshFor: freshFor, retainFor: retainFor}
}

func (s *SnapshotStore[T]) Get(key string, now time.Time) (T, SnapshotStatus) {
	s.mu.RLock()
	entry, ok := s.entries[key]
	s.mu.RUnlock()
	var zero T
	if !ok {
		return zero, SnapshotStatus{State: SnapshotPending}
	}
	if entry.collectedAt.IsZero() || now.Sub(entry.collectedAt) >= s.retainFor {
		return zero, SnapshotStatus{State: SnapshotUnavailable}
	}
	state := SnapshotOK
	if entry.failed || now.Sub(entry.collectedAt) >= s.freshFor {
		state = SnapshotStale
	}
	collectedAt := entry.collectedAt
	return entry.value, SnapshotStatus{State: state, CollectedAt: &collectedAt}
}

func (s *SnapshotStore[T]) Due(key string, now time.Time, refreshAfter, retryAfter time.Duration) bool {
	s.mu.RLock()
	entry, ok := s.entries[key]
	s.mu.RUnlock()
	if !ok {
		return true
	}
	interval := refreshAfter
	if entry.failed {
		interval = retryAfter
	}
	return now.Sub(entry.lastAttempt) >= interval
}

func (s *SnapshotStore[T]) Put(key string, value T, now time.Time) {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.save(key, snapshotEntry[T]{value: value, collectedAt: now, lastAttempt: now})
}

func (s *SnapshotStore[T]) Fail(key string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[key]
	if now.Sub(entry.collectedAt) >= s.retainFor {
		entry = snapshotEntry[T]{}
	}
	entry.lastAttempt = now.UTC()
	entry.failed = true
	s.save(key, entry)
}

func (s *SnapshotStore[T]) save(key string, entry snapshotEntry[T]) {
	if _, exists := s.entries[key]; !exists && len(s.entries) >= s.limit {
		var oldest string
		var at time.Time
		for id, current := range s.entries {
			if at.IsZero() || current.lastAttempt.Before(at) {
				oldest, at = id, current.lastAttempt
			}
		}
		delete(s.entries, oldest)
	}
	s.entries[key] = entry
}

// RunSnapshotSync 定期扫描已保存的目标，失败留待下一轮，无需页面触发重试。
func RunSnapshotSync(ctx context.Context, interval time.Duration, operation string, scan func(context.Context) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := scan(ctx); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "后台资源同步扫描失败", "operation", operation, "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}

// RunSnapshotWorkers 的扫描周期与单个目标的采集完成时间独立，正在采集的键不会重复排队。
func RunSnapshotWorkers[T any](ctx context.Context, interval time.Duration, operation string, workers int,
	scan func(context.Context, func(string, T) bool) error, collect func(context.Context, T),
) error {
	type work struct {
		key    string
		target T
	}
	var pending sync.Map
	queue := make(chan work, workers)
	var group sync.WaitGroup
	for range workers {
		group.Go(func() {
			for item := range queue {
				if ctx.Err() != nil {
					return
				}
				collect(ctx, item.target)
				pending.Delete(item.key)
			}
		})
	}
	err := RunSnapshotSync(ctx, interval, operation, func(ctx context.Context) error {
		return scan(ctx, func(key string, target T) bool {
			if ctx.Err() != nil {
				return false
			}
			if _, exists := pending.LoadOrStore(key, struct{}{}); exists {
				return true
			}
			select {
			case <-ctx.Done():
				pending.Delete(key)
				return false
			case queue <- work{key: key, target: target}:
				return true
			}
		})
	})
	close(queue)
	group.Wait()
	return err
}

// CollectSnapshots 限制后台并发与排队，对每个目标独立回填结果。
func CollectSnapshots[T any](ctx context.Context, workers int, scan func(yield func(T) bool) error, collect func(T)) error {
	queue := make(chan T, workers)
	var group sync.WaitGroup
	for range workers {
		group.Go(func() {
			for target := range queue {
				if ctx.Err() != nil {
					return
				}
				collect(target)
			}
		})
	}
	err := scan(func(target T) bool {
		select {
		case <-ctx.Done():
			return false
		case queue <- target:
			return true
		}
	})
	close(queue)
	group.Wait()
	return err
}
