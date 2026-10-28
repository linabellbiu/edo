package host

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/model"
	"github.com/google/uuid"
)

func resourceFixture(t *testing.T, s *Service) model.Host {
	t.Helper()
	host := model.Host{ID: uuid.NewString(), Name: uuid.NewString(), Mode: model.HostModeSSH, Address: "192.0.2.1", SSHPort: 22, SSHUsername: "fixture", IsActive: true}
	if err := s.db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	return host
}

func TestHostResourcesCacheExpiryIdentityAndUnknownNull(t *testing.T) {
	s, db, _ := newHostTestService(t)
	host := resourceFixture(t, s)
	now := time.Now()
	calls := 0
	s.resources.clock = func() time.Time { return now }
	s.resources.collect = func(context.Context, model.Host) (ResourceInfo, error) {
		calls++
		cpu := 8
		return ResourceInfo{State: "ok", CPUCores: &cpu, OS: "linux"}, nil
	}
	value, err := s.Resources(context.Background(), host.ID)
	if err != nil || value.CPUCores == nil || *value.CPUCores != 8 || value.Source != "ssh" || value.Scope != "current_environment" || value.DiskMount != "/" {
		t.Fatalf("资源值不正确: %+v %v", value, err)
	}
	*value.CPUCores = 999
	cached, err := s.Resources(context.Background(), host.ID)
	if err != nil || calls != 1 || *cached.CPUCores != 8 {
		t.Fatal("未复用缓存或返回值污染缓存")
	}
	now = now.Add(resourceTTL)
	if _, err := s.Resources(context.Background(), host.ID); err != nil || calls != 2 {
		t.Fatal("过期缓存未重采")
	}
	if err := db.Model(&model.Host{}).Where(&model.Host{ID: host.ID}).Update("address", "192.0.2.2").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resources(context.Background(), host.ID); err != nil || calls != 3 {
		t.Fatal("目标配置改变仍使用旧缓存")
	}
	s.resources.collect = func(context.Context, model.Host) (ResourceInfo, error) {
		calls++
		return ResourceInfo{}, errors.New("fixture unavailable")
	}
	now = now.Add(resourceTTL)
	unknown, err := s.Resources(context.Background(), host.ID)
	if err != nil || unknown.State != "unavailable" {
		t.Fatal("失败未表示不可用")
	}
	encoded, _ := json.Marshal(unknown)
	var fields map[string]any
	_ = json.Unmarshal(encoded, &fields)
	for _, key := range []string{"cpu_cores", "memory_total_bytes", "disk_total_bytes"} {
		if value, ok := fields[key]; !ok || value != nil {
			t.Fatalf("未知容量冒充零: %s %s", key, encoded)
		}
	}
	_, _ = s.Resources(context.Background(), host.ID)
	if calls != 4 {
		t.Fatal("失败未负缓存")
	}
	now = now.Add(resourceFailureTTL)
	_, _ = s.Resources(context.Background(), host.ID)
	if calls != 5 {
		t.Fatal("失败缓存未短期过期")
	}
}

func TestHostResourcesBoundConcurrencyCoalescesAndCancels(t *testing.T) {
	s, _, _ := newHostTestService(t)
	hosts := make([]model.Host, resourceConcurrency+1)
	for i := range hosts {
		hosts[i] = resourceFixture(t, s)
	}
	started, release := make(chan struct{}, resourceConcurrency), make(chan struct{})
	var calls, active, peak atomic.Int32
	s.resources.collect = func(ctx context.Context, _ model.Host) (ResourceInfo, error) {
		calls.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); n > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, n) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return ResourceInfo{}, ctx.Err()
		case <-release:
			cpu := 4
			return ResourceInfo{State: "ok", CPUCores: &cpu}, nil
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < resourceConcurrency; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, _ = s.Resources(context.Background(), hosts[i].ID) }(i)
		<-started
	}
	queued := make(chan ResourceInfo, 1)
	go func() { value, _ := s.Resources(context.Background(), hosts[resourceConcurrency].ID); queued <- value }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Resources(ctx, hosts[0].ID); err == nil {
		t.Fatal("取消未传播")
	}
	waitContext, stopWaiting := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stopWaiting()
	if _, err := s.Resources(waitContext, hosts[0].ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("同目标等待不可取消: %v", err)
	}
	followerDone := make(chan struct{})
	go func() { defer close(followerDone); _, _ = s.Resources(context.Background(), hosts[0].ID) }()
	close(release)
	wg.Wait()
	<-followerDone
	if value := <-queued; value.State != "ok" {
		t.Fatal("分页排队采集被当成未知")
	}
	if peak.Load() > resourceConcurrency || calls.Load() != resourceConcurrency+1 {
		t.Fatal("同目标重复采集或并发越界")
	}
}

func TestHostResourcesCancellationAndChangedTargetDiscardResult(t *testing.T) {
	s, db, _ := newHostTestService(t)
	host := resourceFixture(t, s)
	s.resources.collect = func(ctx context.Context, _ model.Host) (ResourceInfo, error) {
		<-ctx.Done()
		return ResourceInfo{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := s.Resources(ctx, host.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时未保留: %v", err)
	}
	s.resources.collect = func(context.Context, model.Host) (ResourceInfo, error) {
		if err := db.Model(&model.Host{}).Where(&model.Host{ID: host.ID}).Update("address", "192.0.2.9").Error; err != nil {
			t.Fatal(err)
		}
		cpu := 8
		return ResourceInfo{State: "ok", CPUCores: &cpu}, nil
	}
	value, err := s.Resources(context.Background(), host.ID)
	if err != nil || value.State != "unavailable" || value.CPUCores != nil {
		t.Fatal("目标变更后的迟到容量被返回")
	}
	if _, err := s.Resources(context.Background(), ""); !errors.Is(err, ErrHostNotFound) {
		t.Fatal("空ID未拒绝")
	}
}

func TestHostResourcesCacheHasFixedEntryLimit(t *testing.T) {
	s, _, _ := newHostTestService(t)
	host := resourceFixture(t, s)
	s.resources.collect = func(context.Context, model.Host) (ResourceInfo, error) {
		cpu := 2
		return ResourceInfo{State: "ok", CPUCores: &cpu}, nil
	}
	s.resources.entries = make(map[[32]byte]resourceEntry)
	s.resources.flights = make(map[[32]byte]*resourceFlight)
	s.resources.slots = make(chan struct{}, resourceConcurrency)
	for i := 0; i < resourceCacheLimit; i++ {
		key := [32]byte{byte(i), byte(i >> 8)}
		s.resources.entries[key] = resourceEntry{expires: time.Now().Add(time.Hour)}
	}
	if _, err := s.Resources(context.Background(), host.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.resources.entries) != resourceCacheLimit {
		t.Fatalf("容量缓存无界增长: %d", len(s.resources.entries))
	}
}
