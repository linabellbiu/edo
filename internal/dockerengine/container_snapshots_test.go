package dockerengine

import (
	"errors"
	"testing"
	"time"

	"edo/internal/cache"
	"edo/internal/database"
	"edo/internal/model"
)

func TestContainerSnapshotUsesCurrentProjectConnectionAndPreservesFiltering(t *testing.T) {
	service, db := newMonitorTestService(t)
	owner := database.WithDepartmentScope(t.Context(), database.DepartmentScope{
		DepartmentID: database.DefaultDepartmentID,
		ProjectID:    database.DefaultProjectID(database.DefaultDepartmentID),
	})
	host := model.Host{ID: "snapshot-host", Mode: model.HostModeSSH, Name: "snapshot-host", IsActive: true}
	if err := db.WithContext(owner).Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	endpoint := model.DockerEndpoint{ID: "snapshot-endpoint", Name: "snapshot-endpoint", HostID: host.ID, IsActive: true}
	if err := db.WithContext(owner).Create(&endpoint).Error; err != nil {
		t.Fatal(err)
	}
	_, key, err := service.containerSnapshotTarget(owner, endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	items := []Container{{ID: "one", Names: []string{"first"}, State: "running"},
		{ID: "two", Names: []string{"second"}, State: "exited"},
		{ID: "three", Names: []string{"third"}, State: "paused"}}
	service.containerSnapshots.Put(key, items, time.Now())
	running, err := service.ContainerListSnapshot(owner, endpoint.ID, false)
	if err != nil || running.Snapshot.State != cache.SnapshotOK || len(running.Containers) != 2 || running.Containers[1].ID != "three" {
		t.Fatalf("缓存过滤不保留运行中的容器: %+v %v", running, err)
	}
	running.Containers[0].Names[0] = "changed"
	all, err := service.ContainerListSnapshot(owner, endpoint.ID, true)
	if err != nil || len(all.Containers) != 3 || all.Containers[0].Names[0] != "first" {
		t.Fatalf("读取结果污染了内存快照或遗漏容器: %+v %v", all, err)
	}
	service.containerSnapshots.Fail(key, time.Now())
	if stale, err := service.ContainerListSnapshot(owner, endpoint.ID, true); err != nil ||
		stale.Snapshot.State != cache.SnapshotStale || stale.Snapshot.CollectedAt == nil || len(stale.Containers) != 3 {
		t.Fatalf("同步故障导致最后成功结果丢失: %+v %v", stale, err)
	}
	if err := db.WithContext(owner).Model(&host).UpdateColumn("ssh_host_key_fingerprint", "changed-key").Error; err != nil {
		t.Fatal(err)
	}
	changed, err := service.ContainerListSnapshot(owner, endpoint.ID, true)
	if err != nil || changed.Snapshot.State != cache.SnapshotPending || len(changed.Containers) != 0 {
		t.Fatalf("连接变更后读取旧容器快照: %+v %v", changed, err)
	}
	foreign := database.WithDepartmentScope(t.Context(), database.DepartmentScope{DepartmentID: database.DefaultDepartmentID, ProjectID: "foreign-project"})
	if _, err := service.ContainerListSnapshot(foreign, endpoint.ID, true); !errors.Is(err, ErrEndpointNotFound) {
		t.Fatalf("容器快照不遵守项目边界: %v", err)
	}
}
