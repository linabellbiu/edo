package kube

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/hostidentity"
	"edo/internal/model"
)

func TestAppendHostsPreservesOfflineExistingHostsAndOtherCapabilities(t *testing.T) {
	service := newKubeTestService(t, 3*time.Second)
	cluster := linkedClusterFixture(t, service, "append-offline-cluster", "")
	old := linkedHostFixture(t, service, "offline-existing", "")
	added := linkedHostFixture(t, service, "append-new", "")
	if err := service.SetHosts(context.Background(), cluster.ID, []string{old.ID}); err != nil {
		t.Fatal(err)
	}
	ssh := model.HostCapability{HostID: added.ID, Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady, Version: "unchanged", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := service.db.Create(&ssh).Error; err != nil {
		t.Fatal(err)
	}
	probed := []string{}
	service.hostIdentityProbe = func(_ context.Context, host model.Host) (hostidentity.Identity, error) {
		probed = append(probed, host.ID)
		if host.ID == old.ID {
			return hostidentity.Identity{}, errors.New("existing host is offline")
		}
		return hostidentity.Identity{Addresses: []string{host.Address}}, nil
	}
	if err := service.AppendHosts(context.Background(), cluster.ID, []string{added.ID, added.ID}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(probed, []string{added.ID}) {
		t.Fatalf("must verify only the new target, probed %v", probed)
	}
	assertAppendHostIDs(t, service, cluster.ID, []string{old.ID, added.ID})
	var currentSSH model.HostCapability
	if err := service.db.Where("host_id = ? AND kind = ?", added.ID, model.HostCapabilitySSH).First(&currentSSH).Error; err != nil {
		t.Fatal(err)
	}
	if currentSSH.Status != ssh.Status || currentSSH.Version != ssh.Version {
		t.Fatal("appending a cluster relationship changed the SSH capability")
	}
	if err := service.AppendHosts(context.Background(), cluster.ID, nil); err != nil {
		t.Fatal(err)
	}
	assertAppendHostIDs(t, service, cluster.ID, []string{old.ID, added.ID})
}

func TestAppendHostsConcurrentTargetsKeepBothAssociations(t *testing.T) {
	service := newKubeTestService(t, 5*time.Second)
	cluster := linkedClusterFixture(t, service, "append-concurrent", "")
	a := linkedHostFixture(t, service, "append-a", "")
	b := linkedHostFixture(t, service, "append-b", "")
	entered := make(chan string, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseProbes := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseProbes)
	service.hostIdentityProbe = func(ctx context.Context, host model.Host) (hostidentity.Identity, error) {
		entered <- host.ID
		select {
		case <-release:
			return hostidentity.Identity{Addresses: []string{host.Address}}, nil
		case <-ctx.Done():
			return hostidentity.Identity{}, ctx.Err()
		}
	}
	results := make(chan error, 2)
	for _, id := range []string{a.ID, b.ID} {
		go func() { results <- service.AppendHosts(context.Background(), cluster.ID, []string{id}) }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("both appends must finish their initial reads before either writes")
		}
	}
	releaseProbes()
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent append did not complete")
		}
	}
	assertAppendHostIDs(t, service, cluster.ID, []string{a.ID, b.ID})
}

func TestAppendHostsRejectsChangedConnectionSnapshotsWithoutChangingLinks(t *testing.T) {
	for _, scenario := range []string{"new-host-connection", "new-host-project", "cluster-connection"} {
		t.Run(scenario, func(t *testing.T) {
			service := newKubeTestService(t, time.Second)
			cluster := linkedClusterFixture(t, service, "append-snapshot", "")
			existing := linkedHostFixture(t, service, "snapshot-existing", "")
			target := linkedHostFixture(t, service, "snapshot-target", "")
			if err := service.SetHosts(context.Background(), cluster.ID, []string{existing.ID}); err != nil {
				t.Fatal(err)
			}
			service.hostIdentityProbe = func(_ context.Context, host model.Host) (hostidentity.Identity, error) {
				var err error
				switch scenario {
				case "new-host-connection":
					err = service.db.Model(&model.Host{}).Where("uuid = ?", target.ID).Update("ssh_credential_ciphertext", "changed-after-probe").Error
				case "new-host-project":
					err = service.db.Model(&model.Host{}).Where("uuid = ?", target.ID).Update("project_id", "different-project").Error
				case "cluster-connection":
					err = service.db.Model(&model.KubernetesCluster{}).Where("uuid = ?", cluster.ID).Update("api_server", "https://changed.example").Error
				}
				if err != nil {
					return hostidentity.Identity{}, err
				}
				return hostidentity.Identity{Addresses: []string{host.Address}}, nil
			}
			if err := service.AppendHosts(context.Background(), cluster.ID, []string{target.ID}); !errors.Is(err, ErrClusterHostCheckUnavailable) {
				t.Fatalf("stale snapshot must fail: %v", err)
			}
			assertAppendHostIDs(t, service, cluster.ID, []string{existing.ID})
		})
	}
}

func TestAppendHostsEnforcesProjectDepartmentAndHostBoundaries(t *testing.T) {
	service := newKubeTestService(t, time.Second)
	cluster := linkedClusterFixture(t, service, "append-project", "project-a")
	existing := linkedHostFixture(t, service, "project-existing", "project-a")
	foreign := linkedHostFixture(t, service, "project-foreign", "project-b")
	department := linkedHostFixture(t, service, "department-foreign", "project-a")
	builtin := linkedHostFixture(t, service, "append-builtin", "project-a")
	if err := service.db.Model(&department).Update("department_id", "different-department").Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Model(&builtin).Updates(map[string]any{"is_builtin": true, "mode": model.HostModeLocal}).Error; err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: cluster.DepartmentID, ProjectID: cluster.ProjectID, AllDepartments: true})
	if err := service.AppendHosts(ctx, cluster.ID, []string{existing.ID}); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{{foreign.ID}, {department.ID}, {builtin.ID}, {"missing"}, {""}, {existing.ID, foreign.ID}} {
		if err := service.AppendHosts(ctx, cluster.ID, ids); !errors.Is(err, ErrInvalidClusterHosts) {
			t.Fatalf("must reject invalid target %v: %v", ids, err)
		}
		assertAppendHostIDs(t, service, cluster.ID, []string{existing.ID})
	}
	otherProject := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: cluster.DepartmentID, ProjectID: "project-b", AllDepartments: true})
	if err := service.AppendHosts(otherProject, cluster.ID, []string{foreign.ID}); !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("must not append to a cluster outside the current project: %v", err)
	}
}

func TestAppendHostsTransfersOnlyRequestedTargetAndKeepsVerifiedStatus(t *testing.T) {
	service := newKubeTestService(t, time.Second)
	a := linkedClusterFixture(t, service, "append-transfer-a", "")
	b := linkedClusterFixture(t, service, "append-transfer-b", "")
	target := linkedHostFixture(t, service, "append-transfer-target", "")
	other := linkedHostFixture(t, service, "append-transfer-other", "")
	if err := service.SetHosts(context.Background(), a.ID, []string{target.ID, other.ID}); err != nil {
		t.Fatal(err)
	}
	if err := service.db.Model(&model.HostCapability{}).Where("host_id = ? AND kind = ?", target.ID, model.HostCapabilityKubernetes).Updates(map[string]any{"status": model.HostCapabilityReady, "version": "v1.35.5", "use_sudo": true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.AppendHosts(context.Background(), a.ID, []string{target.ID}); err != nil {
		t.Fatal(err)
	}
	var capability model.HostCapability
	if err := service.db.Where("host_id = ? AND kind = ?", target.ID, model.HostCapabilityKubernetes).First(&capability).Error; err != nil {
		t.Fatal(err)
	}
	if capability.Status != model.HostCapabilityReady || capability.Version != "v1.35.5" || !capability.UseSudo {
		t.Fatal("repeating an association must retain the checked capability state")
	}
	if err := service.AppendHosts(context.Background(), b.ID, []string{target.ID}); err != nil {
		t.Fatal(err)
	}
	assertAppendHostIDs(t, service, a.ID, []string{other.ID})
	assertAppendHostIDs(t, service, b.ID, []string{target.ID})
	capability = model.HostCapability{}
	if err := service.db.Where("host_id = ? AND kind = ?", target.ID, model.HostCapabilityKubernetes).First(&capability).Error; err != nil {
		t.Fatal(err)
	}
	if capability.Status != model.HostCapabilityUnchecked || capability.Version != "" || capability.UseSudo {
		t.Fatal("moving a relationship must reset the checked capability state")
	}
}

func assertAppendHostIDs(t *testing.T, service *Service, clusterID string, want []string) {
	t.Helper()
	links, err := service.LinkedHosts(context.Background(), []string{clusterID})
	if err != nil {
		t.Fatal(err)
	}
	actual := make([]string, 0, len(links[clusterID]))
	for _, host := range links[clusterID] {
		actual = append(actual, host.ID)
	}
	slices.Sort(actual)
	want = append([]string{}, want...)
	slices.Sort(want)
	if !slices.Equal(actual, want) {
		t.Fatalf("cluster links=%v want %v", actual, want)
	}
}
