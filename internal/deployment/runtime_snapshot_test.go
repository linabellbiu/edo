package deployment

import (
	"context"
	"testing"
	"time"

	"edo/internal/cache"
	"edo/internal/model"
)

func TestDeploymentRuntimeReadUsesBackgroundSnapshot(t *testing.T) {
	fixture := newContainerControlFixture(t)
	state, status, err := fixture.service.CachedRuntimeState(fixture.ctx, fixture.record.ID)
	if err != nil || state != nil || status.State != cache.SnapshotPending {
		t.Fatalf("首次读取应只返回等待状态: state=%#v status=%#v err=%v", state, status, err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fixture.service.RunRuntimeSnapshotSync(ctx) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for status.State == cache.SnapshotPending {
		select {
		case <-deadline:
			t.Fatal("后台未生成运行实例快照")
		case <-tick.C:
		}
		state, status, err = fixture.service.CachedRuntimeState(fixture.ctx, fixture.record.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if status.State != cache.SnapshotOK || status.CollectedAt == nil || state == nil || len(state.Services) == 0 {
		t.Fatalf("后台运行状态不完整: state=%#v status=%#v", state, status)
	}
	if err := fixture.db.Model(&model.Host{}).Where("uuid = ?", fixture.record.HostID).
		Update("updated_at", time.Now().Add(time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	state, status, err = fixture.service.CachedRuntimeState(fixture.ctx, fixture.record.ID)
	if err != nil || state != nil || status.State != cache.SnapshotPending {
		t.Fatalf("设置变更后不能复用旧快照: state=%#v status=%#v err=%v", state, status, err)
	}
}
