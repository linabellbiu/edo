package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/config"
	"edo/internal/hostcredential"
	"edo/internal/k3s"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/sshclient"
)

func newK3sHTTPTestRouter(t *testing.T) (*gin.Engine, func()) {
	t.Helper()
	return newAuthTestRouter(t, func(deps *Dependencies, db *gorm.DB) {
		keys, err := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
		if err != nil {
			t.Fatal(err)
		}
		credential, err := json.Marshal(sshclient.Bundle{Password: "test-only-ssh-password", UseSudo: true})
		if err != nil {
			t.Fatal(err)
		}
		ciphertext, err := keys.Encrypt(string(credential), hostcredential.AAD("httpapi-deployment-host"))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.Host{}).Where("uuid = ?", "httpapi-deployment-host").Updates(map[string]any{
			"ssh_credential_ciphertext": ciphertext,
			"ssh_host_key_fingerprint":  "SHA256:" + strings.Repeat("a", 43),
			"architecture":              model.HostArchitectureAMD64,
		}).Error; err != nil {
			t.Fatal(err)
		}
		deps.K3s = k3s.NewService(db, keys, deps.Kubernetes, deps.Access, config.Runtime{})
	})
}

func TestK3sHTTPPreviewQueuesReviewedPlanAndRejectsDuplicate(t *testing.T) {
	router, cleanup := newK3sHTTPTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/plans", map[string]any{
		"action": "create", "profile": "custom", "name": "K3s HTTP plan", "server_host_id": "httpapi-deployment-host",
		"server_options": map[string]any{"datastore-endpoint": "postgres://admin:private-db-password@db.example.invalid/k3s"},
	}, admin)
	if response.Code != http.StatusCreated {
		t.Fatalf("preview: status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "test-only-ssh-password") || strings.Contains(response.Body.String(), "private-db-password") {
		t.Fatalf("preview leaked a credential: %s", response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("installation preview must not be cached")
	}
	var plan k3s.Plan
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil || plan.ID == "" {
		t.Fatalf("invalid plan: %+v %v", plan, err)
	}
	queued := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/installations", map[string]any{"plan_id": plan.ID}, admin)
	if queued.Code != http.StatusAccepted || !strings.Contains(queued.Body.String(), `"status":"pending"`) {
		t.Fatalf("queue: status=%d body=%s", queued.Code, queued.Body.String())
	}
	repeated := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/installations", map[string]any{"plan_id": plan.ID}, admin)
	if repeated.Code != http.StatusConflict || !strings.Contains(repeated.Body.String(), "k3s_plan_consumed") {
		t.Fatalf("duplicate: status=%d body=%s", repeated.Code, repeated.Body.String())
	}
	detail := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/installations/"+plan.ID, nil, admin)
	if detail.Code != http.StatusOK || strings.Contains(detail.Body.String(), "private-db-password") {
		t.Fatalf("detail: status=%d body=%s", detail.Code, detail.Body.String())
	}
}

func TestK3sHTTPJoinOptionsAndPreviewUseTargetVersionAndRejectUpgrade(t *testing.T) {
	var version atomic.Value
	version.Store("v1.35.5+k3s1")
	clusterAPI := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"gitVersion": version.Load().(string)})
	}))
	defer clusterAPI.Close()
	router, cleanup := newK3sHTTPTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	defaultOptions := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/options", nil, admin)
	var catalog k3s.Catalog
	if defaultOptions.Code != http.StatusOK || json.Unmarshal(defaultOptions.Body.Bytes(), &catalog) != nil || catalog.Version != k3s.CatalogK3sVersion || catalog.DefaultVersion != catalog.Version || catalog.TargetVersion != "" {
		t.Fatalf("create default must retain the schema version: %d %s", defaultOptions.Code, defaultOptions.Body.String())
	}
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters", map[string]any{
		"name": "target-version-http", "mode": "kubeconfig", "default_namespace": "default", "kubeconfig": kubeconfigForHTTPTest(clusterAPI),
	}, admin)
	var cluster struct {
		Cluster struct {
			ID string `json:"id"`
		} `json:"cluster"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &cluster) != nil || cluster.Cluster.ID == "" {
		t.Fatalf("create fixture cluster: %d %s", created.Code, created.Body.String())
	}
	options := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/options?cluster_id="+cluster.Cluster.ID, nil, admin)
	if options.Code != http.StatusOK || json.Unmarshal(options.Body.Bytes(), &catalog) != nil || catalog.Version != k3s.CatalogK3sVersion || catalog.DefaultVersion != "v1.35.5+k3s1" || catalog.TargetVersion != catalog.DefaultVersion {
		t.Fatalf("join defaults must use the live target version: %d %s", options.Code, options.Body.String())
	}
	request := map[string]any{
		"action": "join", "profile": "default", "cluster_id": cluster.Cluster.ID, "agent_host_ids": []string{"httpapi-deployment-host"}, "endpoint": "https://192.0.2.10:6443",
		"installer": map[string]any{"version": k3s.CatalogK3sVersion},
	}
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/plans", request, admin)
	var plan k3s.Plan
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &plan) != nil || plan.Version != "v1.35.5+k3s1" || plan.TargetVersion != plan.Version {
		t.Fatalf("join preview: %d %s", response.Code, response.Body.String())
	}
	version.Store("v1.35.6+k3s1")
	executed := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/installations", map[string]any{"plan_id": plan.ID}, admin)
	if executed.Code != http.StatusConflict || !strings.Contains(executed.Body.String(), `"code":"k3s_target_version_changed"`) {
		t.Fatalf("upgraded target must require another review: %d %s", executed.Code, executed.Body.String())
	}
	request["profile"] = "custom"
	request["installer"] = map[string]any{"version": k3s.CatalogK3sVersion}
	rejected := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/plans", request, admin)
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), `"code":"invalid_k3s_config"`) {
		t.Fatalf("newer agents must fail during preview: %d %s", rejected.Code, rejected.Body.String())
	}
	request["installer"] = map[string]any{}
	fresh := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/plans", request, admin)
	if fresh.Code != http.StatusCreated || json.Unmarshal(fresh.Body.Bytes(), &plan) != nil || plan.Version != "v1.35.6+k3s1" {
		t.Fatalf("custom without version must follow target: %d %s", fresh.Code, fresh.Body.String())
	}
	queued := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/installations", map[string]any{"plan_id": plan.ID}, admin)
	if queued.Code != http.StatusAccepted {
		t.Fatalf("live version check must not deadlock inside the database transaction: %d %s", queued.Code, queued.Body.String())
	}
	missing := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/options?cluster_id=missing-cluster", nil, admin)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("scoped target lookup must reject an unknown cluster: %d %s", missing.Code, missing.Body.String())
	}
}

func TestK3sHTTPJoinOptionsRequireNodeInstallationPermission(t *testing.T) {
	router, cleanup := newK3sHTTPTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	role := createRoleForHTTPTest(t, router, admin, "k3s-create-options-only", []string{access.PermissionClusterCreate, access.PermissionClusterExecute, access.PermissionClusterUpdate, access.PermissionClusterRead})
	createUserForHTTPTest(t, router, admin, "k3s-options-user", "", []string{role})
	reader := loginForDepartmentTest(t, router, "k3s-options-user")
	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/options?cluster_id=private-target", nil, reader)
	if response.Code != http.StatusForbidden {
		t.Fatalf("create permission must not authorize probing a join target: %d %s", response.Code, response.Body.String())
	}
}

func TestK3sHTTPInstallRequiresExecuteAndAssociationPermissions(t *testing.T) {
	router, cleanup := newK3sHTTPTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	role := createRoleForHTTPTest(t, router, admin, "k3s-create-only", []string{access.PermissionClusterCreate})
	createUserForHTTPTest(t, router, admin, "k3s-create-only", "", []string{role})
	reader := loginForDepartmentTest(t, router, "k3s-create-only")
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/plans", map[string]any{
		"action": "create", "profile": "default", "name": "K3s forbidden", "server_host_id": "httpapi-deployment-host",
	}, reader)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "permission_denied") {
		t.Fatalf("creation permission must not grant SSH installation: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestK3sHTTPPlanAndLogsAreRestrictedToTheirOwner(t *testing.T) {
	router, cleanup := newK3sHTTPTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/plans", map[string]any{
		"action": "create", "profile": "default", "name": "K3s owner", "server_host_id": "httpapi-deployment-host",
	}, admin)
	var plan k3s.Plan
	if err := json.Unmarshal(response.Body.Bytes(), &plan); response.Code != http.StatusCreated || err != nil || plan.ID == "" {
		t.Fatalf("preview: status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
	role := createRoleForHTTPTest(t, router, admin, "k3s-another-owner", []string{access.PermissionClusterRead, access.PermissionClusterCreate, access.PermissionClusterUpdate, access.PermissionClusterExecute})
	createUserForHTTPTest(t, router, admin, "k3s-another-owner", "", []string{role})
	other := loginForDepartmentTest(t, router, "k3s-another-owner")
	for _, request := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/kubernetes/k3s/installations/" + plan.ID, nil},
		{http.MethodPost, "/api/v1/kubernetes/k3s/installations", map[string]any{"plan_id": plan.ID}},
	} {
		result := performJSONRequest(t, router, request.method, request.path, request.body, other)
		if result.Code != http.StatusNotFound {
			t.Fatalf("another user's installation must not be visible: status=%d body=%s", result.Code, result.Body.String())
		}
	}
}
