package k3s

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/hostcredential"
	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/sshclient"
	"edo/internal/testutil"
)

type testAuthorizer struct{ denied bool }

func (a *testAuthorizer) HasPermission(context.Context, *model.User, string) (bool, error) {
	return !a.denied, nil
}

type fakeTransport struct {
	mu         sync.Mutex
	writes     []string
	reads      int
	existing   bool
	kubeconfig string
}

func (f *fakeTransport) Run(_ context.Context, _ model.Host, _ sshclient.Bundle, script string, _ io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, script)
	return nil
}
func (f *fakeTransport) Read(_ context.Context, host model.Host, _ sshclient.Bundle, script string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if strings.Contains(script, "cat /etc/rancher/k3s/k3s.yaml") {
		return f.kubeconfig, nil
	}
	if f.existing {
		return strings.Replace(preflightFixtureOutput(host.ID), "EDO_K3S_CHECK\texisting_installation\tpassed", "EDO_K3S_CHECK\texisting_installation\tfailed", 1), nil
	}
	return preflightFixtureOutput(host.ID), nil
}

func preflightFixtureOutput(id string) string {
	var result strings.Builder
	for _, definition := range hostPreflightDefinitions {
		fmt.Fprintf(&result, "EDO_K3S_CHECK\t%s\tpassed\n", definition.id)
	}
	identity := sha256.Sum256([]byte(id))
	fmt.Fprintf(&result, "EDO_K3S_HOSTNAME\thost-%x\nEDO_K3S_IDENTITY_BEGIN\naddresses\t192.0.2.10\nmachine_id\t%x\nsystem_uuid\t\nboot_id\t\nEDO_K3S_IDENTITY_END\nEDO_K3S_REPORT_END\n", identity[:4], identity[:16])
	return result.String()
}

type fixture struct {
	db        *gorm.DB
	service   *Service
	ctx       context.Context
	actor     model.User
	host      model.Host
	auth      *testAuthorizer
	transport *fakeTransport
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db := testutil.NewDatabase(t)
	secrets, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	actor := model.User{ID: uuid.NewString(), Username: "k3s-" + uuid.NewString()[:8], Nickname: "安装测试", DepartmentID: database.DefaultDepartmentID, PasswordHash: "fixture-only", IsActive: true, IsSuperuser: true, AuthVersion: 1, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	project := model.Project{ID: uuid.NewString(), Name: "K3s-" + uuid.NewString()[:8], DepartmentID: actor.DepartmentID, IsActive: true, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: project.ID, DepartmentID: actor.DepartmentID, UserID: actor.ID, AllDepartments: true})
	host := model.Host{ID: uuid.NewString(), Name: "目标主机", Mode: model.HostModeSSH, Address: "192.0.2.10", SSHPort: 22, SSHUsername: "root", SSHAuthType: model.SSHAuthPassword, SSHHostKeyFingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32)), ProjectID: project.ID, DepartmentID: actor.DepartmentID, IsActive: true, CreatedBy: actor.ID, CreatedAt: now, UpdatedAt: now}
	bundle, _ := json.Marshal(sshclient.Bundle{Password: "ssh-fixture-password"})
	host.SSHCredentialCiphertext, err = secrets.Encrypt(string(bundle), hostcredential.AAD(host.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.HostCapability{HostID: host.ID, Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	auth := &testAuthorizer{}
	transport := &fakeTransport{kubeconfig: "apiVersion: v1\nkind: Config\nclusters:\n- name: default\n  cluster:\n    server: https://127.0.0.1:6443\ncontexts:\n- name: default\n  context:\n    cluster: default\n    user: default\ncurrent-context: default\nusers:\n- name: default\n  user:\n    token: fixture-admin-token\n"}
	service := NewService(db, secrets, nil, auth, config.Runtime{}).WithTransport(transport)
	return fixture{db, service, ctx, actor, host, auth, transport}
}
func (f fixture) input() Input {
	return Input{Action: "create", Profile: "default", Name: "新建测试集群", ServerHostID: f.host.ID, AgentHostIDs: []string{}, Installer: map[string]any{}}
}

func TestPreviewUsesPinnedVersionFinalRedactedConfigAndEncryptedSnapshot(t *testing.T) {
	f := newFixture(t)
	input := f.input()
	input.Profile = "custom"
	input.ServerOptions = map[string]any{"disable": []any{"traefik"}, "datastore-endpoint": "postgres://user:secret-db-password@db.example/test"}
	input.Overrides = "server:\n  disable: []\n  secrets-encryption: false\n  future-feature: enabled\ninstaller:\n  INSTALL_K3S_VERSION: v1.36.0+k3s1\n"
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.ServerConfig, "disable: []") || !strings.Contains(plan.ServerConfig, "secrets-encryption: false") || !strings.Contains(plan.ServerConfig, "tls-san:") || !strings.Contains(plan.ServerConfig, "edo.dev/host="+f.host.ID) || !strings.Contains(plan.ServerConfig, "write-kubeconfig-mode: \"0600\"") {
		t.Fatalf("effective configuration missing: %s", plan.ServerConfig)
	}
	encoded, _ := json.Marshal(plan)
	for _, secret := range []string{"ssh-fixture-password", "secret-db-password"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("preview leaked credential")
		}
	}
	if strings.Contains(plan.ServerConfig, "node-name:") {
		t.Fatal("default hostname behavior must remain unchanged")
	}
	if len(plan.Warnings) < 2 {
		t.Fatal("unknown/version options must be explained")
	}
	var record model.K3sInstallation
	if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(record.InputCiphertext, "secret-db-password") || !strings.HasPrefix(record.InputCiphertext, "v1:") {
		t.Fatal("input must be encrypted")
	}
	snapshot, err := f.service.decode(record)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Input.Installer["INSTALL_K3S_VERSION"] != "v1.36.0+k3s1" {
		t.Fatal("overrides must win")
	}
	if f.transport.reads != 1 || len(f.transport.writes) != 0 || plan.Preflight == nil || !plan.Preflight.Passed {
		t.Fatal("preview must run read-only checks without installing")
	}
	defaultPlan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(defaultPlan.Commands[0], CatalogK3sVersion) {
		t.Fatal("default installation must pin catalog version")
	}
}

func TestSafeFailureDoesNotExposeUnknownOrWrappedErrorDetails(t *testing.T) {
	private := errors.New("host association validation failed: password=private-fixture-password")
	for _, scenario := range []struct {
		name string
		err  error
		want string
	}{
		{"unknown association error", private, "安装未完成，请检查主机服务、集群连接和安装配置。"},
		{"wrapped known error", errors.Join(ErrReadiness, private), ErrReadiness.Error()},
		{"wrapped cancellation", errors.Join(context.Canceled, private), "安装连接已中断，请检查主机上的实际状态。"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if actual := safeFailure(scenario.err); actual != scenario.want || strings.Contains(actual, "private-fixture-password") {
				t.Fatalf("unexpected public failure: %q", actual)
			}
		})
	}
}

func TestDefaultPreviewIgnoresRetainedCustomOptions(t *testing.T) {
	for _, overrides := range []string{
		"server:\n  node-name: retained-server\nagent:\n  node-name: retained-agent\ninstaller:\n  INSTALL_K3S_VERSION: v1.36.0+k3s1\n",
		"server: [invalid retained YAML",
	} {
		t.Run(overrides, func(t *testing.T) {
			f := newFixture(t)
			input := f.input()
			input.ServerOptions = map[string]any{"node-name": "retained-server"}
			input.AgentOptions = map[string]any{"node-name": "retained-agent"}
			input.Installer = map[string]any{"version": "v1.36.0+k3s1", "channel": "testing", "bin-dir": "/opt/retained-k3s"}
			input.Overrides = overrides
			plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			var record model.K3sInstallation
			if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.service.decode(record)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Input.ServerOptions) != 0 || len(snapshot.Input.AgentOptions) != 0 || snapshot.Input.Overrides != "" {
				t.Fatal("default installation retained custom runtime options")
			}
			if len(snapshot.Input.Installer) != 1 || snapshot.Input.Installer["INSTALL_K3S_VERSION"] != CatalogK3sVersion {
				t.Fatalf("default installation must use only the pinned version: %#v", snapshot.Input.Installer)
			}
			preview, _ := json.Marshal(plan)
			for _, retained := range []string{"retained-server", "retained-agent", "/opt/retained-k3s", "v1.36.0+k3s1", "testing"} {
				if strings.Contains(string(preview), retained) {
					t.Fatalf("default preview retained custom value %q", retained)
				}
			}
			if !strings.Contains(plan.Commands[0], CatalogK3sVersion) {
				t.Fatal("default installation command must pin the catalog version")
			}
			if f.transport.reads != 1 || len(f.transport.writes) != 0 || plan.Preflight == nil || !plan.Preflight.Passed {
				t.Fatal("preview must run read-only checks without installing")
			}
		})
	}
}

func TestExecuteConsumesPlanAtomicallyAndLocksHosts(t *testing.T) {
	f := newFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != "pending" {
		t.Fatal(operation.Status)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); !errors.Is(err, ErrPlanConsumed) {
		t.Fatalf("duplicate execution: %v", err)
	}
	otherInput := f.input()
	otherInput.Name = "第二个测试集群"
	other, err := f.service.Preview(f.ctx, f.actor.ID, otherInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, other.ID); !errors.Is(err, ErrHostBusy) {
		t.Fatalf("concurrent host installation allowed: %v", err)
	}
	var count int64
	f.db.Model(&model.K3sInstallationHostLock{}).Count(&count)
	if count != 1 {
		t.Fatal(count)
	}
	listed, err := f.service.List(f.ctx, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != plan.ID {
		t.Fatal("planned previews must not appear in installation history")
	}
}

func TestConcurrentExecuteOnlyQueuesOneInstallation(t *testing.T) {
	f := newFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() { defer wait.Done(); _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); results <- err }()
	}
	wait.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrPlanConsumed) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("queued %d copies of one plan", success)
	}
}

func TestCurrentProjectMembershipIsRecheckedBeforePlanning(t *testing.T) {
	f := newFixture(t)
	if err := f.db.Model(&model.User{}).Where("uuid = ?", f.actor.ID).Update("is_superuser", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Preview(f.ctx, f.actor.ID, f.input()); !errors.Is(err, ErrPermission) {
		t.Fatalf("non-member planned privileged install: %v", err)
	}
}

func TestEncryptionPolicyValuesRemainVisibleWhileCredentialsAreRedacted(t *testing.T) {
	redacted := RedactOptions(map[string]any{"secrets-encryption": false, "secrets-encryption-provider": "aescbc", "token": "private-token"})
	if redacted["secrets-encryption"] != false || redacted["secrets-encryption-provider"] != "aescbc" || redacted["token"] == "private-token" {
		t.Fatalf("incorrect policy/credential redaction: %#v", redacted)
	}
}

func TestReviewedHostChangesExpiredPlanAndOwnerIsolation(t *testing.T) {
	f := newFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	otherCtx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: f.host.ProjectID, DepartmentID: f.host.DepartmentID, UserID: uuid.NewString(), AllDepartments: true})
	if _, err := f.service.Get(otherCtx, plan.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another owner can read plan: %v", err)
	}
	if err := f.db.Model(&model.Host{}).Where("uuid = ?", f.host.ID).Update("address", "192.0.2.11").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); !errors.Is(err, ErrHostChanged) {
		t.Fatalf("changed connection executed: %v", err)
	}
	if err := f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", plan.ID).Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); !errors.Is(err, ErrPlanExpired) {
		t.Fatalf("expired plan executed: %v", err)
	}
}

func TestWorkerRechecksRevokedPermissionAndDoesNotInstallExistingKubernetes(t *testing.T) {
	for _, scenario := range []string{"permission", "existing"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
				t.Fatal(err)
			}
			if scenario == "permission" {
				f.auth.denied = true
			} else {
				f.transport.existing = true
			}
			record, err := f.service.claim(context.Background())
			if err != nil || record == nil {
				t.Fatal(err)
			}
			f.service.runInstallation(context.Background(), *record)
			var finished model.K3sInstallation
			if err := f.db.First(&finished, "uuid = ?", plan.ID).Error; err != nil {
				t.Fatal(err)
			}
			if finished.Status != "failed" || len(f.transport.writes) != 0 {
				t.Fatalf("unsafe execution: %s, %d writes", finished.Status, len(f.transport.writes))
			}
			if scenario == "permission" && f.transport.reads != 1 {
				t.Fatal("revoked actor reached SSH")
			}
			if scenario == "existing" && !strings.Contains(finished.ErrorMessage, "现有 Kubernetes") {
				t.Fatal(finished.ErrorMessage)
			}
			var locks int64
			f.db.Model(&model.K3sInstallationHostLock{}).Count(&locks)
			if locks != 0 {
				t.Fatal("failed operation retained host lock")
			}
		})
	}
}

func TestLeaseRecoveryDoesNotInterruptAnotherLiveWorker(t *testing.T) {
	f := newFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(context.Background())
	if err != nil || record == nil {
		t.Fatal(err)
	}
	second := NewService(f.db, f.service.secrets, nil, f.auth, config.Runtime{})
	if err := second.recoverExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	var current model.K3sInstallation
	f.db.First(&current, "uuid = ?", plan.ID)
	if current.Status != "running" {
		t.Fatal("another active worker was interrupted")
	}
	f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", plan.ID).Update("lease_expires_at", time.Now().UTC().Add(-time.Minute))
	var expired int64
	if err := f.db.Model(&model.K3sInstallation{}).Where("status = ? AND lease_expires_at < ?", "running", time.Now().UTC()).Count(&expired).Error; err != nil {
		t.Fatal(err)
	}
	if expired != 1 {
		t.Fatalf("expected expired fixture lease, got %d", expired)
	}
	if err := second.recoverExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	current = model.K3sInstallation{}
	if err := f.db.First(&current, "uuid = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != "failed" {
		t.Fatal("interrupted install must not be replayed")
	}
	var locks int64
	f.db.Model(&model.K3sInstallationHostLock{}).Count(&locks)
	if locks != 0 {
		t.Fatal("recovery retained host lock")
	}
}

type fakeClusters struct {
	cluster model.KubernetesCluster
	client  *kubernetes.Clientset
	config  string
	linked  []string
	created int
	joins   int
	revoked int
	version string
	onJoin  func()
	inspect func() (*kube.ServerJoinInfo, error)
}

func (k *fakeClusters) Find(context.Context, string) (*model.KubernetesCluster, error) {
	copy := k.cluster
	return &copy, nil
}
func (k *fakeClusters) Create(_ context.Context, _ string, input kube.Input) (*model.KubernetesCluster, error) {
	k.created++
	k.config = *input.Kubeconfig
	copy := k.cluster
	copy.Name = input.Name
	return &copy, nil
}
func (k *fakeClusters) AppendHosts(_ context.Context, _ string, ids []string) error {
	for _, id := range ids {
		if !slices.Contains(k.linked, id) {
			k.linked = append(k.linked, id)
		}
	}
	return nil
}
func (k *fakeClusters) Ping(context.Context, string) (string, error) {
	if k.version != "" {
		return k.version, nil
	}
	return CatalogK3sVersion, nil
}
func (k *fakeClusters) CreateNodeJoin(context.Context, string, kube.NodeJoinInput) (*kube.NodeJoin, error) {
	k.joins++
	if k.onJoin != nil {
		k.onJoin()
	}
	return &kube.NodeJoin{ID: "fixture-token-id", Token: "K10fixture-ca::short-lived-token", ResourceIdentity: kube.ResourceIdentity{UID: "fixture-secret", ResourceVersion: "1"}}, nil
}
func (k *fakeClusters) RevokeNodeJoin(context.Context, string, string, kube.ResourceIdentity) error {
	k.revoked++
	return nil
}
func (k *fakeClusters) Clientset(context.Context, string) (*kubernetes.Clientset, error) {
	return k.client, nil
}

func (k *fakeClusters) RESTConfig(context.Context, string) (*rest.Config, error) {
	return &rest.Config{Host: k.cluster.APIServer}, nil
}

func (k *fakeClusters) RequireClusterPermission(context.Context, string, string, string) error {
	return nil
}

func (k *fakeClusters) InspectServerJoin(context.Context, string, string, string) (*kube.ServerJoinInfo, error) {
	if k.inspect != nil {
		return k.inspect()
	}
	return nil, kube.ErrServerJoinUnsupported
}

func fakeClusterClient(t *testing.T, f fixture) *fakeClusters {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			_, _ = io.WriteString(w, "ok")
			return
		}
		if r.URL.Path != "/api/v1/nodes" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"fixture-node"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`)
	}))
	t.Cleanup(server.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return &fakeClusters{cluster: model.KubernetesCluster{ID: uuid.NewString(), Name: "已接入集群", ProjectID: f.host.ProjectID, DepartmentID: f.host.DepartmentID, APIServer: "https://192.0.2.10:6443", IsActive: true, UpdatedAt: time.Now().UTC()}, client: client}
}

func TestWorkerCreatesImportsAndAssociatesReadyServer(t *testing.T) {
	f := newFixture(t)
	clusters := fakeClusterClient(t, f)
	f.service.kubernetes = clusters
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(context.Background())
	if err != nil || record == nil {
		t.Fatal(err)
	}
	f.service.runInstallation(context.Background(), *record)
	result, err := f.service.Get(f.ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.ClusterID != clusters.cluster.ID || clusters.created != 1 || len(clusters.linked) != 1 || clusters.linked[0] != f.host.ID {
		t.Fatalf("incomplete create: %#v", result)
	}
	config, err := clientcmd.Load([]byte(clusters.config))
	if err != nil {
		t.Fatal(err)
	}
	if config.Clusters["default"].Server != plan.Endpoint {
		t.Fatal("loopback kubeconfig endpoint was not replaced")
	}
	if len(f.transport.writes) != 1 || !strings.Contains(f.transport.writes[0], "EDO_K3S_EXISTS") || !strings.Contains(f.transport.writes[0], CatalogK3sVersion) {
		t.Fatal("real reviewed installer script was not generated")
	}
	if strings.Contains(result.Logs, "fixture-admin-token") || strings.Contains(result.Logs, "ssh-fixture-password") {
		t.Fatal("runtime logs leaked credentials")
	}
}

func TestWorkerJoinsUsingShortLivedTokenAndRevokesIt(t *testing.T) {
	f := newFixture(t)
	clusters := fakeClusterClient(t, f)
	clusters.linked = []string{"already-linked-offline-host"}
	f.service.kubernetes = clusters
	if err := f.db.WithContext(f.ctx).Create(&clusters.cluster).Error; err != nil {
		t.Fatal(err)
	}
	// 测试替身读取落库后的值，保留数据库实际使用的时间精度。
	if err := f.db.WithContext(f.ctx).First(&clusters.cluster, "uuid = ?", clusters.cluster.ID).Error; err != nil {
		t.Fatal(err)
	}
	input := Input{Action: "join", Profile: "default", ClusterID: clusters.cluster.ID, AgentHostIDs: []string{f.host.ID}}
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(context.Background())
	if err != nil || record == nil {
		t.Fatal(err)
	}
	f.service.runInstallation(context.Background(), *record)
	result, err := f.service.Get(f.ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || clusters.joins != 1 || clusters.revoked != 1 || clusters.created != 0 || len(clusters.linked) != 2 || !slices.Contains(clusters.linked, "already-linked-offline-host") || !slices.Contains(clusters.linked, f.host.ID) {
		t.Fatalf("incomplete join: %#v", result)
	}
	if strings.Contains(result.Logs, "short-lived-token") {
		t.Fatal("bootstrap token leaked into logs")
	}
}

func TestPreloadedBinaryRequiresExactVersionAndNeverAllowsExistingServices(t *testing.T) {
	installer := map[string]any{"INSTALL_K3S_VERSION": CatalogK3sVersion, "INSTALL_K3S_SKIP_DOWNLOAD": true, "INSTALL_K3S_BIN_DIR": "/opt/k3s/bin"}
	script := preflightScript(installer, map[string]any{})
	if !strings.Contains(script, "'/opt/k3s/bin/k3s' --version") || !strings.Contains(script, CatalogK3sVersion) || !strings.Contains(script, "/etc/rancher/k3s") || !strings.Contains(script, "/k3s*") {
		t.Fatal("preloaded executable guard incomplete")
	}
	if strings.Contains(script, "if command -v k3s") {
		t.Fatal("preloaded binary rejected as an existing cluster")
	}
}
