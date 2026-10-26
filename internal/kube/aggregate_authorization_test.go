package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/hostidentity"
	"edo/internal/model"
)

func TestRBACTopologyRechecksAccessAfterHostIdentityLookup(t *testing.T) {
	for _, change := range []string{"unchanged", "native-revoked", "policy-revoked"} {
		t.Run(change, func(t *testing.T) {
			var nativeRevoked atomic.Bool
			var nodeReads, reviews atomic.Int32
			server := resourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
				if request.Header.Get("Impersonate-User") != "edo:project:alice:version-1" {
					t.Error("topology used a different principal or management credentials")
				}
				switch request.URL.Path {
				case "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
					check := decodeIdentityReview(t, request)
					attributes := check.Spec.ResourceAttributes
					if attributes == nil || attributes.Resource != "nodes" || attributes.Verb != "list" || attributes.Namespace != "" {
						t.Errorf("host mapping fence checked a different operation: %+v", check.Spec)
					}
					reviews.Add(1)
					writeIdentityReview(w, !nativeRevoked.Load())
				case "/api/v1/nodes":
					nodeReads.Add(1)
					_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"worker","uid":"worker-uid"},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.241"}]}}]}`)
				default:
					http.NotFound(w, request)
				}
			})
			service := newKubeTestService(t, 3*time.Second)
			id := createKubeTestCluster(t, service, server)
			policies := newIdentityTestPolicies()
			policies.grant("alice", map[string][]string{"": {"kubernetes.node.read"}})
			service.authorizationResolver = policies.resolve
			host := linkedHostFixture(t, service, "topology-rbac-host", "")
			if err := service.db.Create(&model.HostCapability{HostID: host.ID, Kind: model.HostCapabilityKubernetes, RuntimeID: id, Status: model.HostCapabilityReady}).Error; err != nil {
				t.Fatal(err)
			}
			lookupStarted, releaseLookup := make(chan struct{}), make(chan struct{})
			service.hostIdentityProbe = func(ctx context.Context, _ model.Host) (hostidentity.Identity, error) {
				if nodeReads.Load() != 1 {
					t.Error("host lookup started without the authorized native node snapshot")
				}
				close(lookupStarted)
				select {
				case <-releaseLookup:
					return hostidentity.Identity{Addresses: []string{host.Address}}, nil
				case <-ctx.Done():
					return hostidentity.Identity{}, ctx.Err()
				}
			}
			type topologyResult struct {
				value *NodeTopology
				err   error
			}
			completed := make(chan topologyResult, 1)
			go func() {
				value, err := service.NodeTopology(identityTestContext(t, service, id, "alice"), id, map[string]bool{"kubernetes.node.read": true}, true)
				completed <- topologyResult{value: value, err: err}
			}()
			select {
			case <-lookupStarted:
			case <-time.After(3 * time.Second):
				t.Fatal("topology never reached the delayed host identity lookup")
			}
			switch change {
			case "native-revoked":
				nativeRevoked.Store(true)
			case "policy-revoked":
				policies.mu.Lock()
				policies.revoked["alice"] = true
				policies.mu.Unlock()
			}
			close(releaseLookup)
			result := <-completed
			if change == "unchanged" {
				if result.err != nil || result.value == nil || len(result.value.Hosts) != 1 || result.value.Hosts[0].State != "matched" {
					t.Fatalf("current authorization lost a legitimate host mapping: %+v %v", result.value, result.err)
				}
			} else if result.value != nil || !errors.Is(result.err, ErrRBACForbidden) {
				t.Fatalf("revocation during SSH lookup returned old topology or host data: %+v %v", result.value, result.err)
			}
			expectedReviews := int32(1)
			if change == "policy-revoked" {
				expectedReviews = 0
			}
			if reviews.Load() != expectedReviews {
				t.Fatalf("host mapping did not use exactly its own final native fence: %d", reviews.Load())
			}
		})
	}
}

func TestRBACOverviewRejectsStatisticsFromAChangedPolicySnapshot(t *testing.T) {
	for _, change := range []string{"middle-resource", "last-metric", "version-change"} {
		t.Run(change, func(t *testing.T) {
			policies := newIdentityTestPolicies()
			permissions := map[string][]string{"": {"kubernetes.node.read"}}
			if change == "middle-resource" {
				permissions["default"] = []string{"kubernetes.workload.read"}
			}
			policies.grant("alice", permissions)
			var nodesRead, changed atomic.Bool
			server := resourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
				if request.Header.Get("Impersonate-User") != "edo:project:alice:version-1" {
					t.Error("statistics used a different principal or management credentials")
				}
				mutatePolicy := func() {
					if !nodesRead.Load() {
						t.Error("policy changed before any private statistics were collected")
					}
					policies.mu.Lock()
					defer policies.mu.Unlock()
					if change == "version-change" {
						policies.policies["alice"].Version = "version-2"
						policies.policies["alice"].Principal = "edo:project:alice:version-2"
					} else {
						policies.revoked["alice"] = true
					}
					changed.Store(true)
				}
				switch request.URL.Path {
				case "/api/v1/nodes":
					nodesRead.Store(true)
					_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"private-worker","uid":"private-uid"}}]}`)
				case "/apis/apps/v1/namespaces/default/deployments":
					if change != "middle-resource" {
						t.Error("node-only overview queried a workload")
					}
					mutatePolicy()
					_, _ = io.WriteString(w, `{"apiVersion":"apps/v1","kind":"DeploymentList","items":[{"metadata":{"name":"private-deployment","namespace":"default"}}]}`)
				case "/apis/metrics.k8s.io/v1/nodes":
					if change == "middle-resource" {
						t.Error("revoked identity reached another native API")
					}
					mutatePolicy()
					_, _ = fmt.Fprintf(w, `{"apiVersion":"metrics.k8s.io/v1","kind":"NodeMetricsList","items":[{"metadata":{"name":"private-worker"},"timestamp":%q,"usage":{"cpu":"100m","memory":"128Mi"}}]}`, time.Now().UTC().Format(time.RFC3339))
				default:
					t.Errorf("unexpected native API request: %s", request.URL.Path)
					http.NotFound(w, request)
				}
			})
			service := newKubeTestService(t, time.Second)
			id := createKubeTestCluster(t, service, server)
			service.authorizationResolver = policies.resolve
			allowed := map[string]bool{"kubernetes.node.read": true, "kubernetes.workload.read": change == "middle-resource"}
			result, err := service.Overview(identityTestContext(t, service, id, "alice"), id, "*", allowed)
			if !changed.Load() {
				t.Fatal("the overview did not reach the policy change point")
			}
			if result != nil || !errors.Is(err, ErrRBACForbidden) {
				t.Fatalf("changed policy returned old counts or sampled usage as a partial success: %+v %v", result, err)
			}
		})
	}
}
