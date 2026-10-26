package kube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"

	"edo/internal/model"
)

func mappedRuleAllows(rules []rbacv1.PolicyRule, group, resource, verb string) bool {
	for _, rule := range rules {
		if slices.Contains(rule.APIGroups, group) && slices.Contains(rule.Resources, resource) && slices.Contains(rule.Verbs, verb) {
			return true
		}
	}
	return false
}

func mappedOperationService(t *testing.T, permissions []string, handler http.HandlerFunc) (*Service, string, context.Context) {
	t.Helper()
	server := identityResourceTestServer(t, handler)
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	decision := &clusterAuthorization{Enforced: true, CacheKey: "operation-user", Version: "operation-policy", Principal: "edo:project:operation-user:operation-policy", Namespaces: []string{"default"}, Permissions: map[string][]string{"default": permissions}}
	service.authorizationResolver = func(context.Context, *model.KubernetesCluster, bool) (*clusterAuthorization, error) {
		return cloneClusterAuthorization(decision), nil
	}
	ctx, cancel := context.WithCancel(identityTestContext(t, service, id, "operation-user"))
	t.Cleanup(cancel)
	return service, id, ctx
}

func TestRBACMappedCronJobExecuteSupportsSuspendAndResume(t *testing.T) {
	rules := rbacRules(map[string]bool{"kubernetes.workload.execute": true}, "default", false)
	if !mappedRuleAllows(rules, "batch", "cronjobs", "get") || !mappedRuleAllows(rules, "batch", "cronjobs", "patch") {
		t.Fatal("CronJob execute lacks its precise native read/patch dependencies")
	}
	if mappedRuleAllows(rules, "batch", "cronjobs", "create") || mappedRuleAllows(rules, "batch", "cronjobs", "delete") {
		t.Fatal("CronJob execute broadened into create/delete")
	}
	var patched atomic.Int32
	service, id, ctx := mappedOperationService(t, []string{"kubernetes.workload.execute"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Impersonate-User") != "edo:project:operation-user:operation-policy" {
			t.Error("CronJob action used a management identity")
		}
		verb := "get"
		if r.Method == http.MethodPatch {
			verb = "patch"
		}
		if r.URL.Path != "/apis/batch/v1/namespaces/default/cronjobs/schedule" || !mappedRuleAllows(rules, "batch", "cronjobs", verb) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","reason":"Forbidden","code":403}`)
			return
		}
		object := map[string]any{"apiVersion": "batch/v1", "kind": "CronJob", "metadata": map[string]any{"name": "schedule", "namespace": "default", "uid": "schedule-uid", "resourceVersion": "7"}, "spec": map[string]any{"suspend": false}}
		if r.Method == http.MethodPatch {
			var patch map[string]any
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			spec, ok := patch["spec"].(map[string]any)
			if !ok {
				t.Error("action did not produce a spec patch")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			suspend, ok := spec["suspend"].(bool)
			if !ok {
				t.Error("action lacked suspend boolean")
			}
			if suspend != (patched.Load() == 0) {
				t.Errorf("CronJob action produced unexpected suspend value: %v", suspend)
			}
			object["spec"].(map[string]any)["suspend"] = suspend
			patched.Add(1)
		}
		_ = json.NewEncoder(w).Encode(object)
	})
	for _, action := range []string{"suspend", "resume"} {
		result, err := service.ResourceAction(ctx, id, "cronjobs", "default", "schedule", ResourceAction{ResourceIdentity: ResourceIdentity{UID: "schedule-uid", ResourceVersion: "7"}, Action: action})
		if err != nil {
			t.Fatalf("mapped CronJob action %s failed: %+v %v", action, result, err)
		}
	}
	if patched.Load() != 2 {
		t.Fatalf("CronJob actions did not issue both native patches: %d", patched.Load())
	}
}

func TestRBACMappedNetworkReadSupportsEndpointSliceDiagnosticsWithoutPodRead(t *testing.T) {
	rules := rbacRules(map[string]bool{"kubernetes.network.read": true}, "default", false)
	if !mappedRuleAllows(rules, "discovery.k8s.io", "endpointslices", "list") {
		t.Fatal("Service diagnostics lacks EndpointSlice list")
	}
	for _, resource := range []string{"pods", "secrets"} {
		for _, verb := range []string{"get", "list", "watch"} {
			if mappedRuleAllows(rules, "", resource, verb) {
				t.Fatalf("network read expanded into %s %s", verb, resource)
			}
		}
	}
	var serviceReads, sliceReads, podRequests atomic.Int32
	service, id, ctx := mappedOperationService(t, []string{"kubernetes.network.read"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Impersonate-User") != "edo:project:operation-user:operation-policy" {
			t.Error("Service diagnostics used a management identity")
		}
		group, resource, verb := "", "", "get"
		switch r.URL.Path {
		case "/api/v1/namespaces/default/services/api":
			resource = "services"
			serviceReads.Add(1)
		case "/apis/discovery.k8s.io/v1/namespaces/default/endpointslices":
			group, resource, verb = "discovery.k8s.io", "endpointslices", "list"
			sliceReads.Add(1)
		case "/api/v1/namespaces/default/pods/backend":
			resource = "pods"
			podRequests.Add(1)
		default:
			http.NotFound(w, r)
			return
		}
		if !mappedRuleAllows(rules, group, resource, verb) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","reason":"Forbidden","code":403}`)
			return
		}
		if resource == "services" {
			_, _ = io.WriteString(w, `{"kind":"Service","apiVersion":"v1","metadata":{"name":"api","namespace":"default","uid":"service-uid","resourceVersion":"3"},"spec":{"clusterIP":"10.43.0.10"}}`)
			return
		}
		if r.URL.Query().Get("labelSelector") != "kubernetes.io/service-name=api" || r.URL.Query().Get("limit") != "500" {
			t.Errorf("EndpointSlice lookup lost its bounded service selector: %s", r.URL)
		}
		_, _ = io.WriteString(w, `{"kind":"EndpointSliceList","apiVersion":"discovery.k8s.io/v1","items":[{"metadata":{"name":"api-endpoints","namespace":"default","ownerReferences":[{"kind":"Service","uid":"service-uid","name":"api"}]},"addressType":"IPv4","endpoints":[{"addresses":["10.42.0.8"],"conditions":{"ready":true},"targetRef":{"kind":"Pod","namespace":"default","name":"backend"}}]}]}`)
	})
	result, err := service.ResourceDiagnostics(ctx, id, "services", "default", "api", "service-uid")
	if err != nil || len(result.Endpoints) != 1 || result.Endpoints[0].Addresses[0] != "10.42.0.8" || serviceReads.Load() != 2 || sliceReads.Load() != 1 || podRequests.Load() != 0 {
		t.Fatalf("mapped Service diagnostics failed: %+v serviceReads=%d slices=%d podReads=%d err=%v", result, serviceReads.Load(), sliceReads.Load(), podRequests.Load(), err)
	}
	if _, err := service.GetResource(ctx, id, "pods", "default", "backend", false); !errors.Is(err, ErrResourceForbidden) && !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("network-only identity could read Pod data: %v", err)
	}
}

func TestRBACInactiveClusterSynchronizationDoesNotConnect(t *testing.T) {
	f := newRBACFixture(t)
	state := f.enable(t)
	if err := f.service.db.WithContext(f.adminCtx).Model(&model.KubernetesCluster{}).Where("uuid = ?", f.cluster.ID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	inactive, err := f.service.Find(f.adminCtx, f.cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := f.peer.requests.Load()
	if err := f.service.syncRBACCluster(f.adminCtx, inactive, state.Revision); !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("inactive cluster synchronization was accepted: %v", err)
	}
	if f.peer.requests.Load() != before || f.peer.writeCount() != 0 {
		t.Fatal("inactive synchronization contacted native management API")
	}
}

func TestRBACInactiveConnectionRecoveryRequiresFreshManagementPermission(t *testing.T) {
	f := newRBACFixture(t)
	f.enable(t)
	if err := f.service.db.WithContext(f.adminCtx).Model(&model.KubernetesCluster{}).Where("uuid = ?", f.cluster.ID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	inactive, err := f.service.Find(f.adminCtx, f.cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := f.peer.requests.Load()
	if err := f.service.RequireRBACManagementForConnectionChange(f.adminCtx, inactive); err != nil {
		t.Fatalf("superuser cannot recover inactive connection: %v", err)
	}
	if err := f.service.RequireRBACManagementForConnectionChange(f.userCtx, inactive); err != nil {
		t.Fatalf("fresh project manager cannot recover inactive connection: %v", err)
	}
	if _, err := f.service.ListClusterRBAC(f.userCtx, inactive.ID); !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("ordinary RBAC list accepted inactive cluster: %v", err)
	}
	if err := f.service.db.WithContext(f.userCtx).Where("role_id = ? AND permission = ?", f.roleID, "kubernetes.rbac.update").Delete(&model.RolePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.service.RequireRBACManagementForConnectionChange(f.userCtx, inactive); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("revoked manager retained inactive recovery permission: %v", err)
	}
	if f.peer.requests.Load() != before {
		t.Fatal("inactive connection management authorization contacted cluster")
	}
}
