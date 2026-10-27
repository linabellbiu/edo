package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/hostcredential"
	"edo/internal/k3s"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/sshclient"
)

type k3sHTTPTestTransport struct {
	reads, runs atomic.Int32
	statuses    map[string]string
	extraOutput string
	delay       time.Duration
}

var k3sHTTPPreflightCheckIDs = []string{
	"privilege", "os", "tools", "architecture", "supervisor", "cgroup", "cpu", "memory", "disk", "hostname", "ports", "download", "system_registry", "endpoint", "swap", "firewall", "selinux", "existing_installation",
}

func (transport *k3sHTTPTestTransport) Read(ctx context.Context, _ model.Host, _ sshclient.Bundle, script string) (string, error) {
	transport.reads.Add(1)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if transport.delay > 0 {
		timer := time.NewTimer(transport.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	if !strings.Contains(script, "EDO_K3S_REPORT_END") {
		return "", errors.New("unexpected non-preflight SSH read")
	}
	var output strings.Builder
	checks := k3sHTTPPreflightCheckIDs
	if strings.Contains(script, "edo_docker_binary=") {
		checks = []string{"privilege", "os", "architecture", "tools", "docker", "bind_address", "ports", "cpu", "memory", "disk", "download", "installation_lock", "hostname"}
	}
	for _, id := range checks {
		status := transport.statuses[id]
		if status == "" {
			status = "passed"
			if id == "system_registry" && strings.Contains(script, "edo_check system_registry warning\n") {
				status = "warning"
			}
		}
		output.WriteString("EDO_K3S_CHECK\t" + id + "\t" + status + "\n")
	}
	output.WriteString("EDO_K3S_HOSTNAME\tcandidate-node\nEDO_K3S_IDENTITY_BEGIN\naddresses\t192.0.2.241\nmachine_id\t11223344556677889900aabbccddeeff\nsystem_uuid\t\nboot_id\t\nEDO_K3S_IDENTITY_END\nEDO_K3S_REPORT_END\n")
	output.WriteString(transport.extraOutput)
	return output.String(), nil
}

func (transport *k3sHTTPTestTransport) Run(context.Context, model.Host, sshclient.Bundle, string, io.Writer) error {
	transport.runs.Add(1)
	return errors.New("unexpected SSH installation during HTTP test")
}

func TestK3sHTTPFixtureSystemRegistryReflectsProbeSelection(t *testing.T) {
	for _, scenario := range []struct {
		name, script, configured, expected string
	}{
		{name: "默认或继承不冒充已探测", script: "edo_check system_registry warning\n", expected: "warning"},
		{name: "选定仓库返回已探测夹具", script: "edo_registry_status=200\nedo_check system_registry passed\n", expected: "passed"},
		{name: "显式阻断保持失败", script: "edo_check system_registry warning\n", configured: "failed", expected: "failed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			transport := &k3sHTTPTestTransport{statuses: map[string]string{"system_registry": scenario.configured}}
			output, err := transport.Read(context.Background(), model.Host{}, sshclient.Bundle{}, scenario.script+"EDO_K3S_REPORT_END\n")
			if err != nil || !strings.Contains(output, "EDO_K3S_CHECK\tsystem_registry\t"+scenario.expected+"\n") || transport.runs.Load() != 0 {
				t.Fatalf("系统镜像检查夹具状态不准确：期望 %s，错误 %v", scenario.expected, err)
			}
		})
	}
}

func newK3sHTTPTestRouter(t *testing.T, transports ...k3s.Transport) (*gin.Engine, func()) {
	t.Helper()
	var transport k3s.Transport = &k3sHTTPTestTransport{}
	if len(transports) > 0 {
		transport = transports[0]
	}
	router, cleanup, _ := newK3sHTTPTestRouterAndDB(t, transport)
	return router, cleanup
}

func newK3sHTTPTestRouterAndDB(t *testing.T, transport k3s.Transport) (*gin.Engine, func(), *gorm.DB) {
	t.Helper()
	var fixtureDB *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, db *gorm.DB) {
		fixtureDB = db
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
		deps.K3s = k3s.NewService(db, keys, deps.Kubernetes, deps.Access, config.Runtime{}).WithTransport(transport)
	})
	return router, cleanup, fixtureDB
}

func TestK3sHTTPPreviewQueuesReviewedPlanAndRejectsDuplicate(t *testing.T) {
	transport := &k3sHTTPTestTransport{}
	router, cleanup := newK3sHTTPTestRouter(t, transport)
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
	if plan.Preflight == nil || !plan.Preflight.Passed || plan.Preflight.CheckedAt.IsZero() || len(plan.Preflight.Hosts) != 1 || !plan.Preflight.Hosts[0].Passed {
		t.Fatalf("successful preview is missing its checked preflight report: %+v", plan.Preflight)
	}
	for _, id := range append(append([]string{}, k3sHTTPPreflightCheckIDs...), "identity", "machine_unique", "node_name_unique") {
		found := false
		want := "passed"
		if id == "system_registry" {
			want = "warning"
		}
		for _, check := range plan.Preflight.Hosts[0].Checks {
			if check.ID == id {
				found = check.Status == want && check.Name != "" && check.Message != ""
			}
		}
		if !found {
			t.Fatalf("successful preflight omitted check %s: %+v", id, plan.Preflight)
		}
	}
	if transport.reads.Load() == 0 || transport.runs.Load() != 0 {
		t.Fatal("preview must collect read-only SSH checks without invoking installation")
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
	var clusterWrites atomic.Int32
	version.Store("v1.35.5+k3s1")
	clusterAPI := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			clusterWrites.Add(1)
			http.Error(w, "unexpected API mutation during preview", http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/readyz":
			_, _ = io.WriteString(w, "ok")
			return
		case "/api/v1/nodes":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"existing-server","uid":"existing-server"},"status":{"nodeInfo":{"machineID":"fedcba9876543210fedcba9876543210"},"addresses":[{"type":"InternalIP","address":"192.0.2.10"}],"conditions":[{"type":"Ready","status":"True"}]}}]}`)
			return
		case "/version":
		default:
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
	if clusterWrites.Load() != 0 {
		t.Fatal("target preflight must not mint tokens or mutate cluster resources")
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
	transport := &k3sHTTPTestTransport{}
	router, cleanup := newK3sHTTPTestRouter(t, transport)
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
	if transport.reads.Load() != 0 || transport.runs.Load() != 0 {
		t.Fatal("unauthorized request executed SSH before checking installation permissions")
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

func k3sHTTPPreflightState(t *testing.T, db *gorm.DB) map[string]int64 {
	t.Helper()
	counts := map[string]int64{}
	for _, resource := range []struct {
		name  string
		model any
	}{
		{"plans", &model.K3sInstallation{}},
		{"host-locks", &model.K3sInstallationHostLock{}},
		{"jobs", &model.Job{}},
		{"outbox", &model.OutboxEvent{}},
	} {
		var count int64
		if err := db.Model(resource.model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		counts[resource.name] = count
	}
	return counts
}

func TestK3sHTTPFailedPreflightReturnsCompleteReportWithoutCreatingPlanOrTask(t *testing.T) {
	transport := &k3sHTTPTestTransport{
		statuses:    map[string]string{"memory": "failed", "ports": "failed", "existing_installation": "failed"},
		extraOutput: "EDO_PRIVATE_OUTPUT\tprivate-remote-diagnostic password=private-remote-password\n",
	}
	router, cleanup, db := newK3sHTTPTestRouterAndDB(t, transport)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	before := k3sHTTPPreflightState(t, db)
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/plans", map[string]any{
		"action": "create", "profile": "default", "name": "K3s blocked preflight", "server_host_id": "httpapi-deployment-host",
	}, admin)
	var failure struct {
		errorResponse
		Preflight *k3s.PreflightReport `json:"preflight"`
	}
	if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &failure) != nil || failure.Code != "k3s_preflight_failed" || failure.RequestID == "" || failure.Message == "" {
		t.Fatalf("preflight failure did not retain its structured HTTP contract: status=%d body=%s", response.Code, response.Body.String())
	}
	if failure.Preflight == nil || failure.Preflight.Passed || failure.Preflight.CheckedAt.IsZero() || len(failure.Preflight.Hosts) != 1 || failure.Preflight.Hosts[0].Passed || failure.Preflight.Hosts[0].Role != "server" {
		t.Fatalf("failed master preflight is incomplete: %+v", failure.Preflight)
	}
	checks := map[string]k3s.PreflightCheck{}
	for _, check := range failure.Preflight.Hosts[0].Checks {
		checks[check.ID] = check
	}
	for _, id := range k3sHTTPPreflightCheckIDs {
		check, exists := checks[id]
		want := "passed"
		if id == "system_registry" {
			want = "warning"
		}
		if status := transport.statuses[id]; status != "" {
			want = status
		}
		if !exists || check.Status != want || check.Name == "" || check.Message == "" {
			t.Fatalf("preflight failure omitted check %s or hid its result: %+v", id, checks)
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("failed preflight report must not be cached")
	}
	for _, private := range []string{"private-remote-diagnostic", "private-remote-password", "test-only-ssh-password", "11223344556677889900aabbccddeeff", "192.0.2.241"} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatalf("failed report exposed private remote data: %s", response.Body.String())
		}
	}
	if transport.reads.Load() != 1 || transport.runs.Load() != 0 {
		t.Fatalf("preview did not remain read-only: reads=%d runs=%d", transport.reads.Load(), transport.runs.Load())
	}
	for name, count := range k3sHTTPPreflightState(t, db) {
		if count != before[name] {
			t.Fatalf("failed preflight created %s: before=%d after=%d", name, before[name], count)
		}
	}
}

func TestK3sHTTPCrossProjectPreflightRejectsHostBeforeSSH(t *testing.T) {
	transport := &k3sHTTPTestTransport{}
	router, cleanup, db := newK3sHTTPTestRouterAndDB(t, transport)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	other := model.Project{ID: "k3s-other-project", DepartmentID: database.DefaultDepartmentID, Name: "Other K3s project", IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Host{}).Where("uuid = ?", "httpapi-deployment-host").Update("project_id", other.ID).Error; err != nil {
		t.Fatal(err)
	}
	before := k3sHTTPPreflightState(t, db)
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/plans", map[string]any{
		"action": "create", "profile": "default", "name": "K3s cross-project preflight", "server_host_id": "httpapi-deployment-host",
	}, admin)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"k3s_host_changed"`) {
		t.Fatalf("cross-project host was not rejected by the saved host scope: %d %s", response.Code, response.Body.String())
	}
	if transport.reads.Load() != 0 || transport.runs.Load() != 0 {
		t.Fatal("cross-project host was probed by SSH before checking project scope")
	}
	for name, count := range k3sHTTPPreflightState(t, db) {
		if count != before[name] {
			t.Fatalf("cross-project request created %s", name)
		}
	}
}

func TestK3sHTTPPreflightExtendsRealServerWriteDeadline(t *testing.T) {
	transport := &k3sHTTPTestTransport{delay: 100 * time.Millisecond}
	router, cleanup := newK3sHTTPTestRouter(t, transport)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	server := httptest.NewUnstartedServer(router)
	server.Config.WriteTimeout = 50 * time.Millisecond
	server.Start()
	defer server.Close()
	payload, err := json.Marshal(map[string]any{
		"action": "create", "profile": "default", "name": "K3s real HTTP deadline", "server_host_id": "httpapi-deployment-host",
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/kubernetes/k3s/plans", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-EDO-Project-ID", database.DefaultProjectID(database.DefaultDepartmentID))
	request.AddCookie(admin)
	client := server.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("real HTTP connection expired before read-only preflight completed: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("preflight response was truncated by the server write deadline: %v", err)
	}
	var plan k3s.Plan
	if response.StatusCode != http.StatusCreated || json.Unmarshal(body, &plan) != nil || plan.ID == "" || plan.Preflight == nil || !plan.Preflight.Passed || len(plan.Preflight.Hosts) != 1 || !plan.Preflight.Hosts[0].Passed {
		t.Fatalf("delayed preflight did not produce a complete real HTTP plan: status=%d body=%s", response.StatusCode, body)
	}
	checks := map[string]k3s.PreflightCheck{}
	for _, check := range plan.Preflight.Hosts[0].Checks {
		checks[check.ID] = check
	}
	for _, id := range append(append([]string{}, k3sHTTPPreflightCheckIDs...), "identity", "machine_unique", "node_name_unique") {
		want := "passed"
		if id == "system_registry" {
			want = "warning"
		}
		if check, ok := checks[id]; !ok || check.Status != want || check.Name == "" || check.Message == "" {
			t.Fatalf("delayed response omitted check %s: %+v", id, checks)
		}
	}
	if transport.reads.Load() != 1 || transport.runs.Load() != 0 {
		t.Fatalf("deadline regression did not use one read-only preflight: reads=%d runs=%d", transport.reads.Load(), transport.runs.Load())
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("real HTTP preflight response must not be cached")
	}
}
