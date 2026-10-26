package host

import (
	"context"
	"errors"
	"testing"
	"time"

	"edo/internal/model"
)

type associatedClusterProbe struct {
	err error
}

func (p *associatedClusterProbe) Ping(context.Context, string) (string, error) {
	return "v1.35.5+k3s1", p.err
}

func TestExplicitKubernetesAssociationRefreshUsesClusterAPIWithoutSSHOrHostBinaries(t *testing.T) {
	_, db, secrets := newHostTestService(t)
	probe := &associatedClusterProbe{}
	// Docker/SSH 探测器为空且主机没有凭据，模拟只有容器内 K3s 的主机。
	service := NewService(db, secrets, nil, probe)
	host := model.Host{ID: "k3d-nas-host", Name: "K3s 容器主机", Mode: model.HostModeSSH, Address: "192.0.2.241", IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	capability := model.HostCapability{HostID: host.ID, Kind: model.HostCapabilityKubernetes, RuntimeID: "container-k3s-cluster", Status: model.HostCapabilityUnchecked, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&capability).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.RefreshRuntimeStatuses(context.Background()); err != nil {
		t.Fatal(err)
	}
	readCapability := func() model.HostCapability {
		t.Helper()
		var current model.HostCapability
		if err := db.Where("host_id = ? AND kind = ?", host.ID, model.HostCapabilityKubernetes).First(&current).Error; err != nil {
			t.Fatal(err)
		}
		return current
	}
	current := readCapability()
	if current.RuntimeID != capability.RuntimeID || current.Status != model.HostCapabilityReady || current.Version != "v1.35.5+k3s1" {
		t.Fatalf("关联集群可达时无需在主机中发现 kubelet/kubectl: %+v", current)
	}
	probe.err = errors.New("temporary cluster API failure")
	if _, err := service.RefreshRuntimeStatuses(context.Background()); err != nil {
		t.Fatal(err)
	}
	current = readCapability()
	if current.RuntimeID != capability.RuntimeID || current.Status != model.HostCapabilityUnreachable {
		t.Fatalf("临时 API 故障只能改变状态，不能清除显式关联: %+v", current)
	}
	probe.err = nil
	if _, err := service.RefreshRuntimeStatuses(context.Background()); err != nil {
		t.Fatal(err)
	}
	if current := readCapability(); current.RuntimeID != capability.RuntimeID || current.Status != model.HostCapabilityReady {
		t.Fatalf("API 恢复后应继续使用原集群关联: %+v", current)
	}
}
