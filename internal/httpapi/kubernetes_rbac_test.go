package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/kube"
	"edo/internal/model"
)

type rbacHTTPFixture struct {
	router    *gin.Engine
	db        *gorm.DB
	admin     *http.Cookie
	clusterID string
	writes    *atomic.Int32
}

func newRBACHTTPFixture(t *testing.T) rbacHTTPFixture {
	t.Helper()
	var writes atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/version" {
			_, _ = w.Write([]byte(`{"gitVersion":"v1.35.5+k3s1"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","reason":"Forbidden","message":"private-upstream-fixture-credential"}`))
	}))
	t.Cleanup(upstream.Close)
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, current *gorm.DB) { db = current })
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters", map[string]any{
		"name": "rbac-http", "mode": "kubeconfig", "default_namespace": "default", "kubeconfig": kubeconfigForHTTPTest(upstream),
	}, admin)
	var body struct {
		Cluster struct {
			ID string `json:"id"`
		} `json:"cluster"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &body) != nil || body.Cluster.ID == "" {
		t.Fatalf("cluster fixture: %d %s", created.Code, created.Body.String())
	}
	return rbacHTTPFixture{router: router, db: db, admin: admin, clusterID: body.Cluster.ID, writes: &writes}
}

func (f rbacHTTPFixture) path() string { return "/api/v1/kubernetes/clusters/" + f.clusterID + "/rbac" }

func TestKubernetesRBACHTTPDefaultAndDraftPreviewNeverWriteCluster(t *testing.T) {
	f := newRBACHTTPFixture(t)
	role := createRoleForHTTPTest(t, f.router, f.admin, "rbac-http-workload", []string{"kubernetes.workload.read", "kubernetes.workload.update"})
	response := performJSONRequest(t, f.router, http.MethodGet, f.path(), nil, f.admin)
	var state kubernetesRBACResponse
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &state) != nil || state.Enabled || state.Revision != 0 || len(state.Grants) != 0 || len(state.Roles) == 0 {
		t.Fatalf("default must be opt-in: %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("authorization state was cacheable")
	}
	for _, permission := range state.Catalog {
		if strings.HasPrefix(permission.Code, "kubernetes.rbac.") {
			t.Fatal("RBAC management must not be a data grant")
		}
		if len(permission.ResourceScopes) == 0 {
			t.Fatalf("grant catalog has no authoritative scope metadata: %s", permission.Code)
		}
		if permission.Code == "kubernetes.storage.create" && (slices.Contains(permission.ResourceScopes, "cluster") || !slices.Contains(permission.ResourceScopes, "namespace")) {
			t.Fatal("storage creation advertised unsupported cluster scope")
		}
	}
	request := map[string]any{"role_id": role, "namespace": "team-a", "cluster_scope": false, "permissions": []string{"kubernetes.workload.read"}, "expected_revision": 0}
	preview := performJSONRequest(t, f.router, http.MethodPost, f.path()+"/grants/preview", request, f.admin)
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), `"rules"`) {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
	}
	var count int64
	f.db.Model(&model.KubernetesRoleGrant{}).Count(&count)
	if count != 0 || f.writes.Load() != 0 {
		t.Fatal("preview created a grant or changed the cluster")
	}
	saved := performJSONRequest(t, f.router, http.MethodPost, f.path()+"/grants", request, f.admin)
	if saved.Code != 200 || json.Unmarshal(saved.Body.Bytes(), &state) != nil || state.Enabled || state.Revision != 1 || len(state.Grants) != 1 || f.writes.Load() != 0 {
		t.Fatalf("disabled policy must retain a draft without native writes: %d %s", saved.Code, saved.Body.String())
	}
	stale := performJSONRequest(t, f.router, http.MethodPut, f.path()+"/grants/"+state.Grants[0].ID, request, f.admin)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "kubernetes_rbac_conflict") {
		t.Fatalf("stale revision: %d %s", stale.Code, stale.Body.String())
	}
}

func TestKubernetesRBACHTTPRejectsUnsafeScopeAndMissingRevision(t *testing.T) {
	f := newRBACHTTPFixture(t)
	role := createRoleForHTTPTest(t, f.router, f.admin, "rbac-http-validation", []string{"kubernetes.workload.read", "kubernetes.rbac.update"})
	for _, request := range []map[string]any{
		{"role_id": role, "namespace": "*", "permissions": []string{"kubernetes.workload.read"}, "expected_revision": 0},
		{"role_id": role, "namespace": "team-a", "cluster_scope": true, "permissions": []string{"kubernetes.workload.read"}, "expected_revision": 0},
		{"role_id": role, "namespace": "team-a", "permissions": []string{"kubernetes.rbac.update"}, "expected_revision": 0},
		{"role_id": role, "namespace": "team-a", "permissions": []string{"kubernetes.secret.read"}, "expected_revision": 0},
		{"role_id": role, "namespace": "team-a", "permissions": []string{"kubernetes.workload.read"}},
	} {
		response := performJSONRequest(t, f.router, http.MethodPost, f.path()+"/grants", request, f.admin)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("unsafe grant accepted: %d %s", response.Code, response.Body.String())
		}
	}
	for _, request := range []map[string]any{{"expected_revision": 0}, {"enabled": true}} {
		response := performJSONRequest(t, f.router, http.MethodPut, f.path()+"/mode", request, f.admin)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("incomplete mode request accepted: %d %s", response.Code, response.Body.String())
		}
	}
	if f.writes.Load() != 0 {
		t.Fatal("invalid request reached a native cluster mutation")
	}
}

func TestKubernetesRBACHTTPEnabledPendingCannotFallBackToSharedIdentity(t *testing.T) {
	f := newRBACHTTPFixture(t)
	response := performJSONRequest(t, f.router, http.MethodPut, f.path()+"/mode", map[string]any{"enabled": true, "expected_revision": 0}, f.admin)
	var state kube.RBACState
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &state) != nil || !state.Enabled || state.Status != "pending" || f.writes.Load() != 0 {
		t.Fatalf("enable must wait for controlled sync: %d %s", response.Code, response.Body.String())
	}
	data := performJSONRequest(t, f.router, http.MethodGet, "/api/v1/kubernetes/clusters/"+f.clusterID+"/resources/pods?namespace=default", nil, f.admin)
	if data.Code != http.StatusServiceUnavailable && data.Code != http.StatusForbidden {
		t.Fatalf("pending authorization fell back: %d %s", data.Code, data.Body.String())
	}
	if strings.Contains(data.Body.String(), "private-upstream-fixture-credential") {
		t.Fatal("native error details escaped")
	}
	// Management remains reachable so an administrator can repair a policy.
	management := performJSONRequest(t, f.router, http.MethodGet, f.path(), nil, f.admin)
	if management.Code != 200 {
		t.Fatalf("management recovery path unavailable: %d %s", management.Code, management.Body.String())
	}
}

func TestKubernetesRBACHTTPManagementIsProjectScoped(t *testing.T) {
	f := newRBACHTTPFixture(t)
	role := createRoleForHTTPTest(t, f.router, f.admin, "rbac-http-read-only", []string{"kubernetes.rbac.read"})
	userID := createUserForHTTPTest(t, f.router, f.admin, "rbac-http-reader", "", []string{role})
	reader := loginForDepartmentTest(t, f.router, "rbac-http-reader")
	listed := performJSONRequest(t, f.router, http.MethodGet, "/api/v1/kubernetes/clusters", nil, reader)
	if listed.Code != 200 {
		t.Fatalf("RBAC readers cannot choose a cluster: %d %s", listed.Code, listed.Body.String())
	}
	response := performJSONRequest(t, f.router, http.MethodGet, f.path(), nil, reader)
	if response.Code != 200 {
		t.Fatalf("scoped read: %d %s", response.Code, response.Body.String())
	}
	denied := performJSONRequest(t, f.router, http.MethodPut, f.path()+"/mode", map[string]any{"enabled": true, "expected_revision": 0}, reader)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("read-only role mutated policy: %d %s", denied.Code, denied.Body.String())
	}
	// Direct DB revocation must be honored even before the Casbin watcher reloads.
	if err := f.db.Where("project_id = ? AND user_id = ?", database.DefaultProjectID(database.DefaultDepartmentID), userID).Delete(&model.ProjectMember{}).Error; err != nil {
		t.Fatal(err)
	}
	revoked := performJSONRequest(t, f.router, http.MethodGet, f.path(), nil, reader)
	if revoked.Code != http.StatusForbidden {
		t.Fatalf("stale local policy kept revoked access: %d %s", revoked.Code, revoked.Body.String())
	}
	if f.writes.Load() != 0 {
		t.Fatal("read or denied operation changed the cluster")
	}
}
