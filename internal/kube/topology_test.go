package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"edo/internal/database"
	"edo/internal/hostidentity"
	"edo/internal/model"
)

func TestNodeRolesUseControlLabelsAndExplicitDualRole(t *testing.T) {
	for _, scenario := range []struct {
		labels map[string]string
		roles  string
	}{
		{nil, "worker"},
		{map[string]string{"node-role.kubernetes.io/control-plane": ""}, "control-plane"},
		{map[string]string{"node-role.kubernetes.io/master": "true"}, "control-plane"},
		{map[string]string{"node-role.kubernetes.io/worker": ""}, "worker"},
		{map[string]string{"node-role.kubernetes.io/control-plane": "", "node-role.kubernetes.io/worker": ""}, "control-plane,worker"},
	} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: scenario.labels}, Spec: corev1.NodeSpec{Unschedulable: true}}
		if got := strings.Join(NodeRoles(node), ","); got != scenario.roles {
			t.Fatalf("roles=%s want=%s", got, scenario.roles)
		}
	}
	if len(NodeRoles(nil)) != 0 {
		t.Fatal("missing object must not imply a worker")
	}
}

func TestTopologyReadsCompletePagesAndKeepsUnknownResourcesDistinct(t *testing.T) {
	var nodePages, podPages atomic.Int32
	now := time.Now().UTC()
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes":
			nodePages.Add(1)
			if r.URL.Query().Get("limit") != "500" {
				t.Error("node list must be paginated")
			}
			if r.URL.Query().Get("continue") == "" {
				fmt.Fprintf(w, `{"apiVersion":"v1","kind":"NodeList","metadata":{"continue":"next"},"items":[{"metadata":{"name":"control","uid":"c1","creationTimestamp":%q,"labels":{"node-role.kubernetes.io/master":"true"}},"status":{"nodeInfo":{"bootID":"12345678-90ab-cdef-1234-567890abcdef","machineID":"private-machine","systemUUID":"private-hardware"},"capacity":{"cpu":"4","memory":"4Gi"},"allocatable":{"cpu":"3500m","memory":"3Gi","pods":"110"},"conditions":[{"type":"Ready","status":"True"}]}}]}`, now.Add(-time.Hour).Format(time.RFC3339))
			} else {
				fmt.Fprintf(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"worker","uid":"w1","creationTimestamp":%q},"spec":{"unschedulable":true},"status":{"allocatable":{"cpu":"2"}}}]}`, now.Add(-time.Hour).Format(time.RFC3339))
			}
		case "/api/v1/pods":
			podPages.Add(1)
			if r.URL.Query().Get("continue") == "" {
				_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"PodList","metadata":{"continue":"next"},"items":[{"metadata":{"namespace":"kube-system"},"spec":{"nodeName":"control"}}]}`))
			} else {
				_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"namespace":"app"},"spec":{"nodeName":"worker"}},{"spec":{"nodeName":""}}]}`))
			}
		case "/apis/metrics.k8s.io/v1/nodes":
			fmt.Fprintf(w, `{"apiVersion":"metrics.k8s.io/v1","kind":"NodeMetricsList","items":[{"metadata":{"name":"control"},"timestamp":%q,"usage":{"cpu":"0","memory":"64Mi"}},{"metadata":{"name":"worker"},"timestamp":%q,"usage":{"cpu":"1","memory":"128Mi"}}]}`, now.Format(time.RFC3339), now.Add(-3*time.Minute).Format(time.RFC3339))
		default:
			t.Errorf("unexpected topology read: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	result, err := service.NodeTopology(context.Background(), id, map[string]bool{"kubernetes.node.read": true, "kubernetes.workload.read": true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if nodePages.Load() != 2 || podPages.Load() != 2 || len(result.Nodes) != 2 {
		t.Fatalf("pagination incomplete: nodes=%d pods=%d", nodePages.Load(), podPages.Load())
	}
	control, worker := result.Nodes[0], result.Nodes[1]
	if control.CPU.Capacity == nil || *control.CPU.Capacity != 4 || control.CPU.Allocatable == nil || *control.CPU.Allocatable != 3.5 || control.CPU.Used == nil || *control.CPU.Used != 0 {
		t.Fatalf("capacity/zero sample lost: %+v", control.CPU)
	}
	if control.Memory.Used == nil || *control.Memory.Used != 64*1024*1024 || control.Pods.Count == nil || *control.Pods.Count != 1 || control.Pods.Capacity == nil || *control.Pods.Capacity != 110 {
		t.Fatalf("control resources invalid: %+v", control)
	}
	if worker.CPU.Capacity != nil || worker.Memory.Capacity != nil || worker.Memory.Allocatable != nil || worker.CPU.Used != nil || worker.Memory.Used != nil || worker.Pods.Capacity != nil {
		t.Fatalf("missing/stale quantities must remain unknown: %+v", worker)
	}
	if worker.Pods.Count == nil || *worker.Pods.Count != 1 || worker.Pods.State != "ok" || result.MetricsState != "partial" {
		t.Fatalf("pod/metrics state invalid: %+v", worker)
	}
	if result.Hosts != nil || result.HostsState != "no_permission" || control.Hosts != nil {
		t.Fatal("host metadata leaked without permission")
	}
	raw, _ := json.Marshal(result)
	for _, private := range []string{"private-machine", "private-hardware", "12345678-90ab-cdef-1234-567890abcdef"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private node identity leaked")
		}
	}
	if control.Node["kind"] != "Node" || worker.Node["metadata"].(map[string]any)["uid"] != "w1" {
		t.Fatal("node details lack real resource identity")
	}
}

func TestTopologyDoesNotReadPodsWithoutPermissionAndRejectsPartialNodeList(t *testing.T) {
	for _, failure := range []string{"none", "node-page", "node-cycle", "pod-page"} {
		t.Run(failure, func(t *testing.T) {
			var podCalls atomic.Int32
			server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/nodes":
					if failure == "node-page" && r.URL.Query().Get("continue") != "" {
						w.WriteHeader(403)
						_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","reason":"Forbidden","code":403}`))
						return
					}
					continuation := ""
					if failure == "node-page" || failure == "node-cycle" {
						continuation = "repeat"
					}
					fmt.Fprintf(w, `{"apiVersion":"v1","kind":"NodeList","metadata":{"continue":%q},"items":[{"metadata":{"name":"node","uid":"n1"}}]}`, continuation)
				case "/api/v1/pods":
					podCalls.Add(1)
					if r.URL.Query().Get("continue") == "" {
						_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"PodList","metadata":{"continue":"next"},"items":[{"spec":{"nodeName":"node"}}]}`))
					} else {
						w.WriteHeader(403)
						_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","reason":"Forbidden","code":403}`))
					}
				default:
					http.NotFound(w, r)
				}
			})
			service := newKubeTestService(t, time.Second)
			id := createKubeTestCluster(t, service, server)
			result, err := service.NodeTopology(context.Background(), id, map[string]bool{"kubernetes.node.read": true, "kubernetes.workload.read": failure == "pod-page"}, false)
			if failure == "node-page" || failure == "node-cycle" {
				if err == nil || result != nil {
					t.Fatal("partial node list must not become a complete topology")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Nodes[0].Pods.Count != nil {
				t.Fatal("unknown pod count was fabricated as zero")
			}
			if failure == "none" && (podCalls.Load() != 0 || result.Nodes[0].Pods.State != "no_permission") {
				t.Fatal("node reader queried workload data")
			}
			if failure == "pod-page" && (podCalls.Load() != 2 || result.Nodes[0].Pods.State != "unavailable") {
				t.Fatal("partial pods treated as complete")
			}
		})
	}
}

func TestTopologyMapsOneVerifiedHostToMultipleNodeRolesAndCachesOnlyIdentity(t *testing.T) {
	const boot = "1234567890abcdef1234567890abcdef"
	var probes atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/nodes" {
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"control","uid":"c","labels":{"node-role.kubernetes.io/control-plane":""}},"status":{"nodeInfo":{"bootID":"12345678-90ab-cdef-1234-567890abcdef"}}},{"metadata":{"name":"worker","uid":"w"},"status":{"nodeInfo":{"bootID":"12345678-90ab-cdef-1234-567890abcdef"}}}]}`))
			return
		}
		http.NotFound(w, r)
	})
	service := newKubeTestService(t, time.Second)
	service.topologyHosts = newTopologyHostIdentityCache()
	id := createKubeTestCluster(t, service, server)
	host := model.Host{ID: "nas-role-host", Name: "NAS", Mode: model.HostModeSSH, Address: "192.0.2.241", SSHPort: 22, IsActive: true}
	if err := service.db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	capability := model.HostCapability{HostID: host.ID, Kind: model.HostCapabilityKubernetes, RuntimeID: id, Status: model.HostCapabilityReady}
	if err := service.db.Create(&capability).Error; err != nil {
		t.Fatal(err)
	}
	service.hostIdentityProbe = func(context.Context, model.Host) (hostidentity.Identity, error) {
		probes.Add(1)
		return hostidentity.Identity{BootID: boot, MachineID: "abcdef0123456789abcdef0123456789"}, nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := service.NodeTopology(context.Background(), id, map[string]bool{"kubernetes.node.read": true}, true)
		if err != nil {
			t.Fatal(err)
		}
		if result.HostsState != "ok" || len(result.Hosts) != 1 || result.Hosts[0].State != "matched" || strings.Join(result.Hosts[0].Roles, ",") != "control-plane,worker" || strings.Join(result.Hosts[0].NodeUIDs, ",") != "c,w" {
			t.Fatalf("one physical host can own both roles: %+v", result.Hosts)
		}
		for _, node := range result.Nodes {
			if len(node.Hosts) != 1 || node.Hosts[0].ID != host.ID {
				t.Fatal("host/node mapping incomplete")
			}
		}
	}
	if probes.Load() != 1 {
		t.Fatalf("repeated reads must not repeat SSH: %d", probes.Load())
	}
	if err := service.db.Model(&host).Update("address", "192.0.2.242").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.NodeTopology(context.Background(), id, map[string]bool{"kubernetes.node.read": true}, true); err != nil {
		t.Fatal(err)
	}
	if probes.Load() != 2 {
		t.Fatal("connection change must invalidate host identity cache")
	}
	result, err := service.NodeTopology(context.Background(), id, map[string]bool{"kubernetes.node.read": true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Hosts != nil || result.Nodes[0].Hosts != nil || result.HostsState != "no_permission" {
		t.Fatal("privileged cache exposed host metadata to node-only reader")
	}
	if probes.Load() != 2 {
		t.Fatal("unprivileged read attempted SSH")
	}
}

func TestTopologyCannotCrossProjectOrInferUnknownHostRole(t *testing.T) {
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/nodes" {
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"worker","uid":"w"},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.1"}]}}]}`))
			return
		}
		http.NotFound(w, r)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	host := linkedHostFixture(t, service, "unmatched-role-host", "")
	if err := service.db.Create(&model.HostCapability{HostID: host.ID, Kind: model.HostCapabilityKubernetes, RuntimeID: id}).Error; err != nil {
		t.Fatal(err)
	}
	for _, unavailable := range []bool{false, true} {
		service.topologyHosts = newTopologyHostIdentityCache()
		service.hostIdentityProbe = func(context.Context, model.Host) (hostidentity.Identity, error) {
			if unavailable {
				return hostidentity.Identity{}, errors.New("private SSH diagnostic")
			}
			return hostidentity.Identity{Addresses: []string{"192.0.2.241"}}, nil
		}
		result, err := service.NodeTopology(context.Background(), id, map[string]bool{"kubernetes.node.read": true}, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Hosts) != 1 || len(result.Hosts[0].Roles) != 0 || len(result.Nodes[0].Hosts) != 0 || result.HostsState != "partial" {
			t.Fatal("unverified host must not be assigned a node role")
		}
		if unavailable && result.Hosts[0].State != "unavailable" || !unavailable && result.Hosts[0].State != "unmatched" {
			t.Fatalf("unknown host state wrong: %+v", result.Hosts[0])
		}
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: database.DefaultDepartmentID, ProjectID: "another-project", AllDepartments: true})
	if _, err := service.NodeTopology(ctx, id, map[string]bool{"kubernetes.node.read": true}, true); !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("cross-project topology must be hidden: %v", err)
	}
	if _, err := service.NodeTopology(context.Background(), id, nil, true); !errors.Is(err, ErrResourceForbidden) {
		t.Fatalf("node permission required: %v", err)
	}
}

func TestTopologyQuantityPreservesMissingCapacityAndValidZero(t *testing.T) {
	if topologyResourceQuantity(nil, corev1.ResourceCPU) != nil {
		t.Fatal("missing CPU must stay unknown")
	}
	value := topologyResourceQuantity(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0")}, corev1.ResourceCPU)
	if value == nil || *value != 0 {
		t.Fatal("explicit zero must remain zero")
	}
	if topologyPodCapacity(nil) != nil {
		t.Fatal("missing Pod capacity must stay unknown")
	}
	pods := topologyPodCapacity(corev1.ResourceList{corev1.ResourcePods: resource.MustParse("0")})
	if pods == nil || *pods != 0 {
		t.Fatal("explicit zero Pod capacity must remain zero")
	}
}

func TestTopologyMetricsRejectsMismatchedNodeUID(t *testing.T) {
	now := time.Now().UTC()
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes":
			fmt.Fprintf(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"reused-name","uid":"current-node","creationTimestamp":%q}}]}`, now.Add(-time.Hour).Format(time.RFC3339))
		case "/apis/metrics.k8s.io/v1/nodes":
			fmt.Fprintf(w, `{"apiVersion":"metrics.k8s.io/v1","kind":"NodeMetricsList","items":[{"metadata":{"name":"reused-name","uid":"old-node"},"timestamp":%q,"usage":{"cpu":"1","memory":"128Mi"}}]}`, now.Format(time.RFC3339))
		default:
			http.NotFound(w, r)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	result, err := service.NodeTopology(context.Background(), id, map[string]bool{"kubernetes.node.read": true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Nodes[0].CPU.Used != nil || result.Nodes[0].Memory.Used != nil || result.Nodes[0].MetricsTimestamp != nil {
		t.Fatal("same-name metric belonging to another Node UID must stay unknown")
	}
}

func TestTopologyMetricsUsesDirectBetaFallbackAndCompletePages(t *testing.T) {
	var stableReads, betaPages atomic.Int32
	now := time.Now().UTC().Format(time.RFC3339)
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes":
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"a","uid":"a1"}},{"metadata":{"name":"b","uid":"b1"}}]}`))
		case "/apis/metrics.k8s.io/v1/nodes":
			stableReads.Add(1)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","reason":"NotFound","code":404}`))
		case "/apis/metrics.k8s.io/v1beta1/nodes":
			betaPages.Add(1)
			if r.URL.Query().Get("limit") != "500" {
				t.Error("metrics must request complete pages")
			}
			if r.URL.Query().Get("continue") == "" {
				fmt.Fprintf(w, `{"apiVersion":"metrics.k8s.io/v1beta1","kind":"NodeMetricsList","metadata":{"continue":"next"},"items":[{"metadata":{"name":"a","uid":"a1"},"timestamp":%q,"usage":{"cpu":"0","memory":"0"}}]}`, now)
			} else {
				fmt.Fprintf(w, `{"apiVersion":"metrics.k8s.io/v1beta1","kind":"NodeMetricsList","items":[{"metadata":{"name":"b","uid":"b1"},"timestamp":%q,"usage":{"cpu":"250m","memory":"128Mi"}}]}`, now)
			}
		default:
			http.NotFound(w, r)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	result, err := service.NodeTopology(context.Background(), id, map[string]bool{"kubernetes.node.read": true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if stableReads.Load() != 1 || betaPages.Load() != 2 || result.MetricsState != "ok" {
		t.Fatalf("metrics fallback/pages incomplete: stable=%d beta=%d state=%s", stableReads.Load(), betaPages.Load(), result.MetricsState)
	}
	if result.Nodes[0].CPU.Used == nil || *result.Nodes[0].CPU.Used != 0 || result.Nodes[1].CPU.Used == nil || *result.Nodes[1].CPU.Used != 0.25 || result.Nodes[1].MetricsTimestamp == nil {
		t.Fatal("direct metrics quantities or sample timestamp missing")
	}
}

func TestTopologyHostIdentityCacheIsBoundedAndDoesNotCacheCancellation(t *testing.T) {
	cache := newTopologyHostIdentityCache()
	var calls int
	reader := func(context.Context, model.Host) (hostidentity.Identity, error) {
		calls++
		return hostidentity.Identity{BootID: "1234567890abcdef1234567890abcdef"}, nil
	}
	host := model.Host{ID: "cached-host", Address: "192.0.2.241", SSHPort: 22}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.read(ctx, host, reader); !errors.Is(err, context.Canceled) || calls != 0 || len(cache.entries) != 0 {
		t.Fatal("cancelled reads must not create or use cache entries")
	}
	if _, err := cache.read(context.Background(), host, reader); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.read(context.Background(), host, reader); err != nil || calls != 1 {
		t.Fatal("identity not reused during TTL")
	}
	entry := cache.entries[host.ID]
	entry.expires = time.Now().Add(-time.Second)
	cache.entries[host.ID] = entry
	if _, err := cache.read(context.Background(), host, reader); err != nil || calls != 2 {
		t.Fatal("expired identity must be read again")
	}
	for i := 0; i < 550; i++ {
		host.ID = fmt.Sprintf("host-%d", i)
		if _, err := cache.read(context.Background(), host, reader); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) > 512 {
		t.Fatalf("identity cache grew without bound: %d", len(cache.entries))
	}
}
