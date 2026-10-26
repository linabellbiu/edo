package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
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
