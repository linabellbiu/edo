package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

func TestNodeTopologyPermissionsKeepPodsAndSSHHostsSeparate(t *testing.T) {
	var nodeReads, podReads atomic.Int32
	var incompleteNodes atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_, _ = io.WriteString(w, `{"gitVersion":"v1.35.5"}`)
		case "/api/v1/nodes":
			nodeReads.Add(1)
			if incompleteNodes.Load() {
				_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","metadata":{"continue":"repeat-page"},"items":[]}`)
				return
			}
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"control","uid":"n1","labels":{"node-role.kubernetes.io/master":"true"}},"status":{"capacity":{"cpu":"4","memory":"4Gi"},"allocatable":{"cpu":"3","memory":"3Gi","pods":"110"},"nodeInfo":{"bootID":"private-boot-id","machineID":"private-machine-id","systemUUID":"private-system-id"}}}]}`)
		case "/api/v1/pods":
			podReads.Add(1)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"PodList","items":[{"spec":{"nodeName":"control"}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"NotFound","code":404}`)
		}
	}))
	defer server.Close()
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, current *gorm.DB) { db = current })
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters", map[string]any{"name": "topology-cluster", "mode": "kubeconfig", "default_namespace": "default", "kubeconfig": kubeconfigForHTTPTest(server)}, admin)
	if created.Code != http.StatusCreated {
		t.Fatal(created.Code, created.Body.String())
	}
	var response struct {
		Cluster struct {
			ID string `json:"id"`
		} `json:"cluster"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	clusterID := response.Cluster.ID
	host := model.Host{ID: "topology-http-host", Name: "Private SSH host", Mode: model.HostModeSSH, Address: "192.0.2.241", SSHPort: 22, SSHUsername: "private-username", SSHCredentialCiphertext: "private-ciphertext", IsActive: true}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.HostCapability{HostID: host.ID, Kind: model.HostCapabilityKubernetes, RuntimeID: clusterID}).Error; err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/kubernetes/clusters/" + clusterID + "/topology"
	for _, scenario := range []struct {
		name        string
		permissions []string
		allowed     bool
		pods        bool
		hosts       bool
	}{
		{"node-only", []string{"kubernetes.node.read"}, true, false, false},
		{"node-pods", []string{"kubernetes.node.read", "kubernetes.workload.read"}, true, true, false},
		{"node-hosts", []string{"kubernetes.node.read", "cluster.read"}, true, false, true},
		{"host-only", []string{"cluster.read"}, false, false, false},
		{"workload-only", []string{"kubernetes.workload.read"}, false, false, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			role := createRoleForHTTPTest(t, router, admin, "topology-"+scenario.name, scenario.permissions)
			username := "topo-" + scenario.name
			createUserForHTTPTest(t, router, admin, username, database.DefaultDepartmentID, []string{role})
			reader := loginForDepartmentTest(t, router, username)
			beforeNodes, beforePods := nodeReads.Load(), podReads.Load()
			result := performJSONRequest(t, router, http.MethodGet, path, nil, reader)
			if !scenario.allowed {
				if result.Code != http.StatusForbidden || nodeReads.Load() != beforeNodes || podReads.Load() != beforePods {
					t.Fatalf("unauthorized topology queried cluster: %d %s", result.Code, result.Body.String())
				}
				return
			}
			if result.Code != http.StatusOK {
				t.Fatalf("topology unavailable: %d %s", result.Code, result.Body.String())
			}
			if result.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("topology response must not be browser cached")
			}
			var topology struct {
				Hosts json.RawMessage `json:"hosts"`
				Nodes []struct {
					Roles []string `json:"roles"`
					Pods  struct {
						Count *int64 `json:"count"`
						State string `json:"state"`
					} `json:"pods"`
				} `json:"nodes"`
			}
			if err := json.Unmarshal(result.Body.Bytes(), &topology); err != nil {
				t.Fatal(err)
			}
			if len(topology.Nodes) != 1 || strings.Join(topology.Nodes[0].Roles, ",") != "control-plane" {
				t.Fatalf("actual node role missing: %s", result.Body.String())
			}
			if scenario.pods {
				if podReads.Load() == beforePods || topology.Nodes[0].Pods.Count == nil || *topology.Nodes[0].Pods.Count != 1 {
					t.Fatal("authorized Pod occupancy missing")
				}
			} else if podReads.Load() != beforePods || topology.Nodes[0].Pods.Count != nil || topology.Nodes[0].Pods.State != "no_permission" {
				t.Fatal("node role alone must not read Pod data")
			}
			if scenario.hosts {
				if !strings.Contains(string(topology.Hosts), host.Name) || !strings.Contains(string(topology.Hosts), `"state":"unavailable"`) {
					t.Fatal("authorized unknown host role must remain unknown")
				}
			} else if string(topology.Hosts) != "null" || strings.Contains(result.Body.String(), host.Address) || strings.Contains(result.Body.String(), host.Name) {
				t.Fatal("host metadata leaked through node access")
			}
			for _, private := range []string{"private-username", "private-ciphertext", "private-machine-id", "private-system-id", "private-boot-id"} {
				if strings.Contains(result.Body.String(), private) {
					t.Fatal("topology leaked private identity or SSH data")
				}
			}
		})
	}
	incompleteNodes.Store(true)
	incomplete := performJSONRequest(t, router, http.MethodGet, path, nil, admin)
	if incomplete.Code != http.StatusBadGateway || !strings.Contains(incomplete.Body.String(), `"code":"kubernetes_topology_incomplete"`) || !strings.Contains(incomplete.Body.String(), "无法读取完整节点拓扑，请稍后重试") {
		t.Fatalf("incomplete nodes must return a safe gateway error: %d %s", incomplete.Code, incomplete.Body.String())
	}
}
