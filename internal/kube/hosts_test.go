package kube

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/hostidentity"
	"edo/internal/model"
)

func TestClusterHostLinksCanBeTransferredAndRemovedWithoutChangingResources(t *testing.T) {
	service := newKubeTestService(t, time.Second)
	ctx := context.Background()
	clusterA := linkedClusterFixture(t, service, "cluster-a", "")
	clusterB := linkedClusterFixture(t, service, "cluster-b", "")
	host := linkedHostFixture(t, service, "host-a", "")
	ssh := model.HostCapability{HostID: host.ID, Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := service.db.Create(&ssh).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.SetHosts(ctx, clusterA.ID, []string{host.ID}); err != nil {
		t.Fatal(err)
	}
	var capability model.HostCapability
	if err := service.db.Where("host_id = ? AND kind = ?", host.ID, model.HostCapabilityKubernetes).First(&capability).Error; err != nil {
		t.Fatal(err)
	}
	if capability.RuntimeID != clusterA.ID || capability.Status != model.HostCapabilityUnchecked {
		t.Fatalf("初次关联应等待状态检查: %+v", capability)
	}
	if err := service.db.Model(&capability).Updates(map[string]any{"status": model.HostCapabilityReady, "version": "v1.35.5"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.SetHosts(ctx, clusterA.ID, []string{host.ID, host.ID}); err != nil {
		t.Fatal(err)
	}
	if err := service.db.Where("host_id = ? AND kind = ?", host.ID, model.HostCapabilityKubernetes).First(&capability).Error; err != nil {
		t.Fatal(err)
	}
	if capability.Status != model.HostCapabilityReady || capability.Version != "v1.35.5" {
		t.Fatalf("重复保存不应清除已检查状态: %+v", capability)
	}
	if err := service.SetHosts(ctx, clusterB.ID, []string{host.ID}); err != nil {
		t.Fatal(err)
	}
	linked, err := service.LinkedHosts(ctx, []string{clusterA.ID, clusterB.ID})
	if err != nil || len(linked[clusterA.ID]) != 0 || len(linked[clusterB.ID]) != 1 || linked[clusterB.ID][0].ID != host.ID {
		t.Fatalf("主机关系应原子转移到新集群: %+v, %v", linked, err)
	}
	if err := service.SetHosts(ctx, clusterB.ID, []string{}); err != nil {
		t.Fatal(err)
	}
	var capabilities []model.HostCapability
	if err := service.db.Where("host_id = ?", host.ID).Find(&capabilities).Error; err != nil {
		t.Fatal(err)
	}
	if len(capabilities) != 1 || capabilities[0].Kind != model.HostCapabilitySSH {
		t.Fatalf("解除集群关联必须保留主机其他能力: %+v", capabilities)
	}
	for _, clusterID := range []string{clusterA.ID, clusterB.ID} {
		cluster, err := service.Find(ctx, clusterID)
		if err != nil || !cluster.IsActive {
			t.Fatalf("解除关联不应删除或停用集群: %+v, %v", cluster, err)
		}
	}
}

func TestClusterHostsRejectInvalidSelectionWithoutLosingExistingLinks(t *testing.T) {
	service := newKubeTestService(t, time.Second)
	cluster := linkedClusterFixture(t, service, "cluster-a", "project-a")
	hostA := linkedHostFixture(t, service, "host-a", "project-a")
	hostB := linkedHostFixture(t, service, "host-b", "project-b")
	builtin := linkedHostFixture(t, service, "local-host", "project-a")
	if err := service.db.Model(&builtin).Updates(map[string]any{"is_builtin": true, "mode": model.HostModeLocal}).Error; err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: cluster.DepartmentID, ProjectID: cluster.ProjectID, AllDepartments: true})
	if err := service.SetHosts(ctx, cluster.ID, []string{hostA.ID}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]string{{hostB.ID}, {builtin.ID}, {"missing"}, {""}, {hostA.ID, "missing"}} {
		if err := service.SetHosts(ctx, cluster.ID, invalid); !errors.Is(err, ErrInvalidClusterHosts) {
			t.Fatalf("应拒绝无效或跨项目主机 %v: %v", invalid, err)
		}
		links, err := service.LinkedHosts(ctx, []string{cluster.ID})
		if err != nil || len(links[cluster.ID]) != 1 || links[cluster.ID][0].ID != hostA.ID {
			t.Fatalf("无效保存不能破坏现有关联: %+v, %v", links, err)
		}
	}
	ctxB := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: cluster.DepartmentID, ProjectID: "project-b", AllDepartments: true})
	if err := service.SetHosts(ctxB, cluster.ID, []string{hostB.ID}); !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("不能通过 ID 修改另一项目的集群: %v", err)
	}
	links, err := service.LinkedHosts(ctxB, []string{cluster.ID})
	if err != nil || len(links[cluster.ID]) != 0 {
		t.Fatalf("关联摘要不得返回其他项目的主机: %+v, %v", links, err)
	}
}

func TestClusterCreationWithHostsIsAtomicAndIndependentClusterNeedsNoHost(t *testing.T) {
	service := newKubeTestService(t, time.Second)
	server := newHostValidationServer(t, nil)
	kubeconfig := kubeconfigForServer(server)
	input := Input{Name: "new-cluster", Mode: model.KubernetesKubeconfig, DefaultNamespace: "default", Kubeconfig: &kubeconfig}
	badIDs := []string{"missing-host"}
	input.HostIDs = &badIDs
	if _, err := service.Create(context.Background(), "admin", input); !errors.Is(err, ErrInvalidClusterHosts) {
		t.Fatalf("创建时无效主机关联应报错: %v", err)
	}
	clusters, err := service.List(context.Background())
	if err != nil || len(clusters) != 0 {
		t.Fatalf("关联失败应回滚集群创建: %+v, %v", clusters, err)
	}
	input.HostIDs = nil
	cluster, err := service.Create(context.Background(), "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	links, err := service.LinkedHosts(context.Background(), []string{cluster.ID})
	if err != nil || links[cluster.ID] == nil || len(links[cluster.ID]) != 0 {
		t.Fatalf("仅有集群时无需虚构主机: %+v, %v", links, err)
	}
	host := linkedHostFixture(t, service, "host-a", "")
	hostIDs := []string{host.ID}
	input.Name, input.HostIDs = "linked-cluster", &hostIDs
	linked, err := service.Create(context.Background(), "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	input.Name, input.HostIDs = "renamed-cluster", nil
	if _, err := service.Update(context.Background(), linked.ID, input); err != nil {
		t.Fatal(err)
	}
	links, err = service.LinkedHosts(context.Background(), []string{linked.ID})
	if err != nil || len(links[linked.ID]) != 1 || links[linked.ID][0].ID != host.ID {
		t.Fatalf("未传 host_ids 的配置更新应保留现有关联: %+v, %v", links, err)
	}
}

func linkedClusterFixture(t *testing.T, service *Service, id, projectID string) model.KubernetesCluster {
	t.Helper()
	server := newHostValidationServer(t, nil)
	encrypted, err := service.secrets.Encrypt(kubeconfigForServer(server), kubeconfigAAD(id))
	if err != nil {
		t.Fatal(err)
	}
	cluster := model.KubernetesCluster{ID: id, Name: id, Mode: model.KubernetesKubeconfig, APIServer: server.URL, KubeconfigCiphertext: encrypted, ProjectID: projectID, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := service.db.Create(&cluster).Error; err != nil {
		t.Fatal(err)
	}
	return cluster
}

func linkedHostFixture(t *testing.T, service *Service, id, projectID string) model.Host {
	t.Helper()
	service.hostIdentityProbe = func(_ context.Context, host model.Host) (hostidentity.Identity, error) {
		return hostidentity.Identity{Addresses: []string{host.Address}}, nil
	}
	host := model.Host{ID: id, Name: id, Mode: model.HostModeSSH, Address: "192.0.2.241", SSHPort: 22, ProjectID: projectID, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := service.db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	return host
}

func newHostValidationServer(t *testing.T, nodesHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"gitVersion": "v1.35.5"})
		case "/api/v1/nodes":
			if nodesHandler != nil {
				nodesHandler(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"node-a"},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.241"}]}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
