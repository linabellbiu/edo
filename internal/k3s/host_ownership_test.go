package k3s

import (
	"errors"
	"testing"
	"time"

	"edo/internal/kube"
	"edo/internal/model"
	"github.com/google/uuid"
)

func TestHostOwnershipBlocksNativeAndK3dBeforeRemoteOperations(t *testing.T) {
	for _, mode := range []string{"native", "k3d"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			if err := f.db.Create(&model.HostCapability{HostID: f.host.ID, Kind: model.HostCapabilityKubernetes, RuntimeID: uuid.NewString()}).Error; err != nil {
				t.Fatal(err)
			}
			input := f.input()
			input.Mode = mode
			if _, err := f.service.Check(f.ctx, f.actor.ID, input); !errors.Is(err, kube.ErrHostClusterOccupied) {
				t.Fatalf("check accepted occupied host: %v", err)
			}
			if _, err := f.service.Preview(f.ctx, f.actor.ID, input); !errors.Is(err, kube.ErrHostClusterOccupied) {
				t.Fatalf("preview accepted occupied host: %v", err)
			}
			if len(f.transport.writes) != 0 || f.transport.reads != 0 || f.transport.privileges != 0 {
				t.Fatal("occupied host contacted remotely")
			}
			var count int64
			if err := f.db.Model(&model.K3sInstallation{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("task persisted: %d %v", count, err)
			}
		})
	}
}

func TestHostOwnershipChangesAfterPreviewBlockExecution(t *testing.T) {
	f := newFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.HostCapability{HostID: f.host.ID, Kind: model.HostCapabilityKubernetes, RuntimeID: uuid.NewString()}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); !errors.Is(err, kube.ErrHostClusterOccupied) {
		t.Fatalf("execute accepted reassigned host: %v", err)
	}
	var locks int64
	f.db.Model(&model.K3sInstallationHostLock{}).Count(&locks)
	if locks != 0 || len(f.transport.writes) != 0 {
		t.Fatal("blocked execution acquired lock or wrote remotely")
	}
}

func TestHostOwnershipRecheckAllowsOwnClusterAndRejectsAnother(t *testing.T) {
	f := newFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	var record model.K3sInstallation
	if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.service.decode(record)
	if err != nil {
		t.Fatal(err)
	}
	cluster := model.KubernetesCluster{ID: uuid.NewString(), Name: plan.Name, ProjectID: f.host.ProjectID, DepartmentID: f.host.DepartmentID, CreatedBy: f.actor.ID, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := f.db.Create(&cluster).Error; err != nil {
		t.Fatal(err)
	}
	record.ClusterID = cluster.ID
	if err := f.db.Create(&model.HostCapability{HostID: f.host.ID, Kind: model.HostCapabilityKubernetes, RuntimeID: cluster.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.service.recheck(f.ctx, record, snapshot); err != nil {
		t.Fatalf("own imported cluster rejected: %v", err)
	}
	if err := f.db.Model(&model.HostCapability{}).Where("host_id = ? AND kind = ?", f.host.ID, model.HostCapabilityKubernetes).Update("runtime_id", uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.service.recheck(f.ctx, record, snapshot); !errors.Is(err, kube.ErrHostClusterOccupied) {
		t.Fatalf("background recheck accepted other cluster: %v", err)
	}
}
