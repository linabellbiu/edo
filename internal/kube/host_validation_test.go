package kube

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/hostidentity"
	"edo/internal/model"
)

func TestClusterHostIdentityMatchesActualNodeNotEndpointOrName(t *testing.T) {
	const machine = "0123456789abcdef0123456789abcdef"
	const hardware = "1234567890abcdef1234567890abcdef"
	const other = "abcdef0123456789abcdef0123456789"
	ip := netip.MustParseAddr("192.0.2.241")
	for _, scenario := range []struct {
		name    string
		node    nodeIdentity
		host    hostidentity.Identity
		matches bool
	}{
		{"actual-node-IP", nodeIdentity{addresses: []netip.Addr{ip}}, hostidentity.Identity{Addresses: []string{"192.0.2.241"}}, true},
		{"mapped-IPv4", nodeIdentity{addresses: []netip.Addr{ip}}, hostidentity.Identity{Addresses: []string{"::ffff:192.0.2.241"}}, true},
		{"different-node-IP", nodeIdentity{addresses: []netip.Addr{ip}}, hostidentity.Identity{Addresses: []string{"192.0.2.240"}}, false},
		{"machine-ID", nodeIdentity{machineID: machine}, hostidentity.Identity{MachineID: machine}, true},
		{"container-hardware-ID", nodeIdentity{systemUUID: hardware, machineID: machine}, hostidentity.Identity{SystemUUID: hardware, MachineID: other}, true},
		{"container-boot-ID", nodeIdentity{bootID: hardware, machineID: machine}, hostidentity.Identity{BootID: hardware, MachineID: other}, true},
		{"boot-mismatch-even-same-IP", nodeIdentity{bootID: hardware, addresses: []netip.Addr{ip}}, hostidentity.Identity{BootID: other, Addresses: []string{"192.0.2.241"}}, false},
		{"hardware-mismatch-even-same-boot", nodeIdentity{systemUUID: hardware, bootID: machine}, hostidentity.Identity{SystemUUID: other, BootID: machine}, false},
		{"empty-boot-falls-back-to-machine", nodeIdentity{bootID: "", machineID: machine}, hostidentity.Identity{BootID: "", MachineID: machine}, true},
		{"hardware-mismatch-even-same-IP", nodeIdentity{systemUUID: hardware, addresses: []netip.Addr{ip}}, hostidentity.Identity{SystemUUID: other, Addresses: []string{"192.0.2.241"}}, false},
		{"machine-mismatch-even-same-IP", nodeIdentity{machineID: machine, addresses: []netip.Addr{ip}}, hostidentity.Identity{MachineID: other, Addresses: []string{"192.0.2.241"}}, false},
		{"no-membership-evidence", nodeIdentity{}, hostidentity.Identity{Addresses: []string{"192.0.2.241"}}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if got := nodeMatchesHost(scenario.node, scenario.host); got != scenario.matches {
				t.Fatalf("match=%v want %v", got, scenario.matches)
			}
		})
	}
	for _, bad := range []string{"", "00000000000000000000000000000000", "ffffffffffffffffffffffffffffffff", "private-token", "{invalid}"} {
		if normalizedIdentityID(bad) != "" {
			t.Fatalf("invalid identity accepted: %q", bad)
		}
	}
}

func TestClusterHostValidationReadsCompleteNodePagination(t *testing.T) {
	service := newKubeTestService(t, time.Second)
	var pages atomic.Int32
	server := newHostValidationServer(t, func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		if r.URL.Query().Get("continue") == "" {
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"NodeList","metadata":{"continue":"next"},"items":[{"metadata":{"name":"other"},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.1"}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"actual-node"},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.241"}]}}]}`))
	})
	cluster := validationClusterFixture(t, service, "paginated", server.URL, kubeconfigForServer(server))
	host := linkedHostFixture(t, service, "paged-host", "")
	if err := service.SetHosts(context.Background(), cluster.ID, []string{host.ID}); err != nil {
		t.Fatal(err)
	}
	if pages.Load() != 2 {
		t.Fatalf("must consume all pages, got %d", pages.Load())
	}
}

func TestClusterHostValidationAcceptsVerifiedContainerBootIdentity(t *testing.T) {
	service := newKubeTestService(t, time.Second)
	server := newHostValidationServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"container-node"},"status":{"addresses":[{"type":"InternalIP","address":"172.18.0.2"}],"nodeInfo":{"bootID":"12345678-90ab-cdef-1234-567890abcdef","machineID":"0123456789abcdef0123456789abcdef"}}}]}`))
	})
	cluster := validationClusterFixture(t, service, "container-cluster", server.URL, kubeconfigForServer(server))
	host := linkedHostFixture(t, service, "container-host", "")
	service.hostIdentityProbe = func(context.Context, model.Host) (hostidentity.Identity, error) {
		return hostidentity.Identity{Addresses: []string{"192.0.2.241"}, MachineID: "abcdef0123456789abcdef0123456789", BootID: "1234567890abcdef1234567890abcdef"}, nil
	}
	if err := service.SetHosts(context.Background(), cluster.ID, []string{host.ID}); err != nil {
		t.Fatalf("matching kernel boot identity should verify container host: %v", err)
	}
	assertClusterHostCount(t, service, cluster.ID, 1)
}

func TestClusterHostValidationFailsClosedOnPageErrorOrCycle(t *testing.T) {
	for _, failure := range []string{"forbidden", "cycle", "timeout"} {
		t.Run(failure, func(t *testing.T) {
			service := newKubeTestService(t, 80*time.Millisecond)
			server := newHostValidationServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("continue") == "" {
					_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"NodeList","metadata":{"continue":"next"},"items":[{"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.241"}]}}]}`))
					return
				}
				switch failure {
				case "forbidden":
					http.Error(w, "private upstream token", http.StatusForbidden)
				case "cycle":
					_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"NodeList","metadata":{"continue":"next"},"items":[]}`))
				case "timeout":
					<-r.Context().Done()
				}
			})
			cluster := validationClusterFixture(t, service, "fail-closed", server.URL, kubeconfigForServer(server))
			host := linkedHostFixture(t, service, "failure-host", "")
			if err := service.SetHosts(context.Background(), cluster.ID, []string{host.ID}); !errors.Is(err, ErrClusterHostCheckUnavailable) {
				t.Fatalf("must reject incomplete node data: %v", err)
			}
			assertClusterHostCount(t, service, cluster.ID, 0)
		})
	}
}

func TestClusterHostMismatchRejectsTestCreateUpdateAndKeepsOldAssociation(t *testing.T) {
	service := newKubeTestService(t, time.Second)
	host := linkedHostFixture(t, service, "actual-host", "")
	good := linkedClusterFixture(t, service, "good-cluster", "")
	if err := service.SetHosts(context.Background(), good.ID, []string{host.ID}); err != nil {
		t.Fatal(err)
	}
	badServer := newHostValidationServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"same-hostname-does-not-prove-membership"},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.99"}]}}]}`))
	})
	config := kubeconfigForServer(badServer)
	ids := []string{host.ID}
	input := Input{Name: "wrong-cluster", Mode: model.KubernetesKubeconfig, DefaultNamespace: "default", Kubeconfig: &config, HostIDs: &ids}
	if _, err := service.Test(context.Background(), input); !errors.Is(err, ErrClusterHostMismatch) {
		t.Fatalf("test must reject wrong cluster: %v", err)
	}
	if _, err := service.Create(context.Background(), "admin", input); !errors.Is(err, ErrClusterHostMismatch) {
		t.Fatalf("create must reject wrong cluster: %v", err)
	}
	var count int64
	if err := service.db.Model(&model.KubernetesCluster{}).Where("name = ?", input.Name).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed creation persisted: %d %v", count, err)
	}
	input.HostIDs = nil
	input.Name = good.Name
	if _, err := service.Update(context.Background(), good.ID, input); !errors.Is(err, ErrClusterHostMismatch) {
		t.Fatalf("kubeconfig replacement must revalidate existing hosts: %v", err)
	}
	unchanged, err := service.Find(context.Background(), good.ID)
	if err != nil || unchanged.KubeconfigCiphertext != good.KubeconfigCiphertext || unchanged.APIServer != good.APIServer {
		t.Fatalf("failed update changed connection: %+v %v", unchanged, err)
	}
	bad := validationClusterFixture(t, service, "existing-wrong", badServer.URL, config)
	if err := service.SetHosts(context.Background(), bad.ID, ids); !errors.Is(err, ErrClusterHostMismatch) {
		t.Fatalf("reassignment must reject mismatch: %v", err)
	}
	assertClusterHostCount(t, service, good.ID, 1)
	assertClusterHostCount(t, service, bad.ID, 0)
}

func TestClusterHostSSHFailureAndConcurrentChangesCannotBeBypassed(t *testing.T) {
	for _, scenario := range []string{"ssh-failure", "host-changed", "cluster-changed"} {
		t.Run(scenario, func(t *testing.T) {
			service := newKubeTestService(t, time.Second)
			cluster := linkedClusterFixture(t, service, "race-cluster", "")
			host := linkedHostFixture(t, service, "race-host", "")
			service.hostIdentityProbe = func(_ context.Context, current model.Host) (hostidentity.Identity, error) {
				switch scenario {
				case "ssh-failure":
					return hostidentity.Identity{}, errors.New("private SSH credentials must never surface")
				case "host-changed":
					if err := service.db.Model(&model.Host{}).Where("uuid = ?", host.ID).Update("address", "192.0.2.99").Error; err != nil {
						t.Fatal(err)
					}
				case "cluster-changed":
					if err := service.db.Model(&model.KubernetesCluster{}).Where("uuid = ?", cluster.ID).Update("api_server", "https://changed.example").Error; err != nil {
						t.Fatal(err)
					}
				}
				return hostidentity.Identity{Addresses: []string{current.Address}}, nil
			}
			if err := service.SetHosts(context.Background(), cluster.ID, []string{host.ID}); !errors.Is(err, ErrClusterHostCheckUnavailable) {
				t.Fatalf("must reject %s: %v", scenario, err)
			}
			assertClusterHostCount(t, service, cluster.ID, 0)
		})
	}
}

func TestClusterHostRemovalNeedsNoNodeOrSSHAccess(t *testing.T) {
	service := newKubeTestService(t, time.Second)
	cluster := linkedClusterFixture(t, service, "offline-cluster", "")
	host := linkedHostFixture(t, service, "offline-host", "")
	if err := service.SetHosts(context.Background(), cluster.ID, []string{host.ID}); err != nil {
		t.Fatal(err)
	}
	service.hostIdentityProbe = func(context.Context, model.Host) (hostidentity.Identity, error) {
		t.Fatal("unlink must not use SSH")
		return hostidentity.Identity{}, nil
	}
	if err := service.db.Model(&model.KubernetesCluster{}).Where("uuid = ?", cluster.ID).Update("kubeconfig_ciphertext", "unreadable").Error; err != nil {
		t.Fatal(err)
	}
	if err := service.SetHosts(context.Background(), cluster.ID, []string{}); err != nil {
		t.Fatal(err)
	}
	assertClusterHostCount(t, service, cluster.ID, 0)
}

func TestClusterHostSaveGuardRejectsConnectionChangesAfterValidation(t *testing.T) {
	service := newKubeTestService(t, time.Second)
	cluster := linkedClusterFixture(t, service, "guard-cluster", "")
	host := linkedHostFixture(t, service, "guard-host", "")
	host.DepartmentID = ""
	guard, err := service.ValidateHostForSave(context.Background(), cluster.ID, host)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.db.Transaction(guard); err != nil {
		t.Fatalf("unchanged snapshot must save: %v", err)
	}
	if err := service.db.Model(&model.KubernetesCluster{}).Where("uuid = ?", cluster.ID).Update("kubeconfig_ciphertext", "changed-after-probe").Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Transaction(guard); !errors.Is(err, ErrClusterHostCheckUnavailable) {
		t.Fatalf("stale host save must be refused: %v", err)
	}
}

func validationClusterFixture(t *testing.T, service *Service, id, server, config string) model.KubernetesCluster {
	t.Helper()
	encrypted, err := service.secrets.Encrypt(config, kubeconfigAAD(id))
	if err != nil {
		t.Fatal(err)
	}
	cluster := model.KubernetesCluster{ID: id, Name: id, Mode: model.KubernetesKubeconfig, APIServer: server, KubeconfigCiphertext: encrypted, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := service.db.Create(&cluster).Error; err != nil {
		t.Fatal(err)
	}
	return cluster
}

func assertClusterHostCount(t *testing.T, service *Service, id string, want int) {
	t.Helper()
	links, err := service.LinkedHosts(context.Background(), []string{id})
	if err != nil || len(links[id]) != want {
		value, _ := json.Marshal(links)
		t.Fatalf("cluster links=%s want %d err=%v", value, want, err)
	}
}
