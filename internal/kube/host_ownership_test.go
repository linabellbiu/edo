package kube

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"edo/internal/hostidentity"
	"edo/internal/model"
	"gorm.io/datatypes"
)

func TestHostOwnershipRechecksAssociationAfterRemoteValidation(t *testing.T) {
	for _, operation := range []string{"replace", "append", "host-save"} {
		t.Run(operation, func(t *testing.T) {
			s := newKubeTestService(t, time.Second)
			a := linkedClusterFixture(t, s, "ownership-a", "")
			b := linkedClusterFixture(t, s, "ownership-b", "")
			host := linkedHostFixture(t, s, "ownership-host", "")
			s.hostIdentityProbe = func(_ context.Context, current model.Host) (hostidentity.Identity, error) {
				if err := s.db.Create(&model.HostCapability{HostID: host.ID, Kind: model.HostCapabilityKubernetes, RuntimeID: a.ID}).Error; err != nil {
					t.Fatal(err)
				}
				return hostidentity.Identity{Addresses: []string{current.Address}}, nil
			}
			var err error
			switch operation {
			case "replace":
				err = s.SetHosts(context.Background(), b.ID, []string{host.ID})
			case "append":
				err = s.AppendHosts(context.Background(), b.ID, []string{host.ID})
			case "host-save":
				guard, prepareErr := s.ValidateHostForSave(context.Background(), b.ID, host)
				err = prepareErr
				if err == nil {
					err = s.db.Transaction(guard)
				}
			}
			if !errors.Is(err, ErrHostClusterOccupied) {
				t.Fatalf("concurrent association accepted: %v", err)
			}
			assertAppendHostIDs(t, s, a.ID, []string{host.ID})
			assertAppendHostIDs(t, s, b.ID, nil)
		})
	}
}

func TestHostOwnershipRecognizesK3dHostWithoutClaimingPhysicalNode(t *testing.T) {
	s := newKubeTestService(t, time.Second)
	cluster := linkedClusterFixture(t, s, "k3d-ownership", "")
	host := linkedHostFixture(t, s, "k3d-host", "")
	hosts, _ := json.Marshal([]map[string]string{{"id": host.ID}})
	record := model.K3sInstallation{ID: "k3d-source", Action: "create", Status: "succeeded", Name: "k3d", ClusterID: cluster.ID, Hosts: datatypes.JSON(hosts), Preview: datatypes.JSON(`{"mode":"k3d"}`), InputCiphertext: "fixture-only", ExpiresAt: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(), DepartmentID: cluster.DepartmentID, ProjectID: cluster.ProjectID}
	if err := s.db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := RequireHostClusterAvailable(context.Background(), s.db, []string{host.ID}, ""); !errors.Is(err, ErrHostClusterOccupied) {
		t.Fatalf("k3d host reusable: %v", err)
	}
	if err := RequireHostClusterAvailable(context.Background(), s.db, []string{host.ID}, cluster.ID); err != nil {
		t.Fatal(err)
	}
	var capabilities int64
	s.db.Model(&model.HostCapability{}).Where("host_id = ? AND kind = ?", host.ID, model.HostCapabilityKubernetes).Count(&capabilities)
	if capabilities != 0 {
		t.Fatal("k3d host was fabricated as a physical Kubernetes node")
	}
}
