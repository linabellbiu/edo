package host

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/cache"
	"edo/internal/database"
	"edo/internal/model"

	"github.com/google/uuid"
)

func resourceFixture(t *testing.T, s *Service) model.Host {
	t.Helper()
	host := model.Host{ID: uuid.NewString(), Name: uuid.NewString(), Mode: model.HostModeSSH, Address: "192.0.2.1", SSHPort: 22, SSHUsername: "fixture", IsActive: true}
	if err := s.db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.First(&host, "uuid = ?", host.ID).Error; err != nil {
		t.Fatal(err)
	}
	return host
}

func syncTestResources(ctx context.Context, s *Service, collect func(context.Context, model.Host) (ResourceInfo, error)) error {
	return cache.CollectSnapshots(ctx, resourceConcurrency, func(yield func(model.Host) bool) error {
		return s.scanResources(ctx, func(_ string, host model.Host) bool { return yield(host) })
	}, func(host model.Host) {
		s.syncResource(ctx, host, collect)
	})
}

func TestHostResourcesReadOnlyUsesBackgroundSnapshots(t *testing.T) {
	s, db, _ := newHostTestService(t)
	host := resourceFixture(t, s)
	ctx := t.Context()
	value, err := s.Resources(ctx, host.ID)
	if err != nil || value.State != cache.SnapshotPending || value.CollectedAt != nil || value.CPUCores != nil {
		t.Fatalf("首次读取不能触发采集或伪造时间: %+v %v", value, err)
	}
	var calls atomic.Int32
	collect := func(_ context.Context, target model.Host) (ResourceInfo, error) {
		if target.ID != host.ID {
			return ResourceInfo{}, errors.New("其他主机未配置采集模拟")
		}
		calls.Add(1)
		cpu := 8
		return ResourceInfo{State: cache.SnapshotOK, CPUCores: &cpu, OS: "linux"}, nil
	}
	if err := syncTestResources(ctx, s, collect); err != nil {
		t.Fatal(err)
	}
	value, err = s.Resources(ctx, host.ID)
	if err != nil || value.State != cache.SnapshotOK || value.CollectedAt == nil || value.CPUCores == nil || *value.CPUCores != 8 ||
		value.Source != "ssh" || value.Scope != "current_environment" || value.DiskMount != "/" {
		t.Fatalf("后台容量快照错误: %+v %v", value, err)
	}
	*value.CPUCores = 999
	if err := syncTestResources(ctx, s, collect); err != nil {
		t.Fatal(err)
	}
	cached, err := s.Resources(ctx, host.ID)
	if err != nil || calls.Load() != 1 || cached.CPUCores == nil || *cached.CPUCores != 8 {
		t.Fatal("新鲜快照重复采集或响应污染缓存")
	}
	s.resources.Fail(resourceIdentity(host), time.Now())
	stale, err := s.Resources(ctx, host.ID)
	if err != nil || stale.State != cache.SnapshotStale || stale.CPUCores == nil || *stale.CPUCores != 8 || !stale.CollectedAt.Equal(*cached.CollectedAt) {
		t.Fatalf("应保留最后成功的容量和时间: %+v %v", stale, err)
	}
	if err := db.Model(&model.Host{}).Where(&model.Host{ID: host.ID}).Update("address", "192.0.2.2").Error; err != nil {
		t.Fatal(err)
	}
	unknown, err := s.Resources(ctx, host.ID)
	if err != nil || unknown.State != cache.SnapshotPending || unknown.CollectedAt != nil || calls.Load() != 1 {
		t.Fatalf("目标变化应使旧快照不可见: %+v %v", unknown, err)
	}
	encoded, _ := json.Marshal(unknown)
	var fields map[string]any
	_ = json.Unmarshal(encoded, &fields)
	for _, key := range []string{"cpu_cores", "memory_total_bytes", "disk_total_bytes"} {
		if value, ok := fields[key]; !ok || value != nil {
			t.Fatalf("未知容量冒充零: %s %s", key, encoded)
		}
	}
	outside := database.WithDepartmentScope(ctx, database.DepartmentScope{
		DepartmentID: host.DepartmentID, ProjectID: uuid.NewString(),
	})
	if _, err := s.Resources(outside, host.ID); !errors.Is(err, ErrHostNotFound) {
		t.Fatalf("资源快照越过项目范围: %v", err)
	}
}

func TestHostResourcesSlowEndpointDoesNotDelayReadOrFastSnapshot(t *testing.T) {
	s, _, _ := newHostTestService(t)
	slow := resourceFixture(t, s)
	fast := resourceFixture(t, s)
	slowStarted, releaseSlow, scanDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer close(releaseSlow)
	var active, peak atomic.Int32
	ctx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	go func() {
		scanDone <- syncTestResources(ctx, s, func(ctx context.Context, current model.Host) (ResourceInfo, error) {
			count := active.Add(1)
			defer active.Add(-1)
			for previous := peak.Load(); count > previous; previous = peak.Load() {
				if peak.CompareAndSwap(previous, count) {
					break
				}
			}
			if current.ID == slow.ID {
				close(slowStarted)
				select {
				case <-ctx.Done():
					return ResourceInfo{}, ctx.Err()
				case <-releaseSlow:
				}
			}
			cpu := 4
			return ResourceInfo{State: cache.SnapshotOK, CPUCores: &cpu}, nil
		})
	}()
	select {
	case <-slowStarted:
	case <-ctx.Done():
		t.Fatal("后台未启动慢目标采集")
	}
	readCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	first, err := s.Resources(readCtx, slow.ID)
	if err != nil || first.State != cache.SnapshotPending {
		t.Fatalf("读取等待在途 SSH: %+v %v", first, err)
	}
	for {
		next, err := s.Resources(ctx, fast.ID)
		if err != nil {
			t.Fatal(err)
		}
		if next.State == cache.SnapshotOK {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("慢目标阻塞了其他主机快照")
		case <-time.After(time.Millisecond):
		}
	}
	stop()
	<-scanDone
	if peak.Load() > resourceConcurrency {
		t.Fatal("后台采集并发越界")
	}
}

func TestHostResourcesChangedTargetDiscardsLateResult(t *testing.T) {
	s, db, _ := newHostTestService(t)
	host := resourceFixture(t, s)
	if err := syncTestResources(t.Context(), s, func(_ context.Context, current model.Host) (ResourceInfo, error) {
		if current.ID == host.ID {
			if err := db.Model(&model.Host{}).Where(&model.Host{ID: host.ID}).Update("address", "192.0.2.9").Error; err != nil {
				t.Error(err)
			}
		}
		cpu := 8
		return ResourceInfo{State: cache.SnapshotOK, CPUCores: &cpu}, nil
	}); err != nil {
		t.Fatal(err)
	}
	value, err := s.Resources(t.Context(), host.ID)
	if err != nil || value.State != cache.SnapshotPending || value.CPUCores != nil {
		t.Fatalf("显示了已发生变化的连接对应的旧容量: %+v %v", value, err)
	}
}
