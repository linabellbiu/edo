package k3s

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"edo/internal/hostcredential"
	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/sshclient"
)

const fixtureServerToken = "K10" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + "::server:fixture-private-server-token"

func serverInfoFixture() *kube.ServerJoinInfo {
	return &kube.ServerJoinInfo{
		Datastore: "embedded-etcd", Version: "v1.35.5+k3s1", CriticalDigest: "critical-reviewed", MembershipDigest: "membership-reviewed",
		CAData: []byte("fixture-trusted-ca"), CriticalOptions: map[string]any{
			"cluster-cidr": []string{"10.42.0.0/16"}, "service-cidr": []string{"10.43.0.0/16"}, "cluster-dns": []string{"10.43.0.10"},
			"cluster-domain": "cluster.local", "disable": []string{"traefik"}, "secrets-encryption": false,
		},
		Members: []kube.ServerJoinMember{{ID: "1", Name: "old-master-1234abcd", PeerURLs: []string{"https://192.0.2.20:2380"}, ClientURLs: []string{"https://192.0.2.20:2379"}}},
	}
}

func serverJoinFixture(t *testing.T) (fixture, *fakeClusters, Input, *kube.ServerJoinInfo) {
	t.Helper()
	f, clusters, input := joinVersionFixture(t, "v1.35.5+k3s1")
	input.JoinRole, input.JoinHostIDs, input.AgentHostIDs, input.ServerToken = "server", []string{f.host.ID}, nil, fixtureServerToken
	info := serverInfoFixture()
	clusters.inspect = func() (*kube.ServerJoinInfo, error) { return info, nil }
	f.service.WithTransport(preflightReadTransport{Transport: f.transport, read: func(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string) (string, error) {
		if strings.Contains(script, "EDO_K3S_ETCD_PEERS_OK") {
			return "EDO_K3S_ETCD_PEERS_OK\n", nil
		}
		return f.transport.Read(ctx, host, bundle, script)
	}})
	return f, clusters, input, info
}

func TestJoinRoleAndHostSelectionPreservesWorkerCompatibility(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		input Input
		valid bool
		role  string
	}{
		{"legacy worker", Input{Action: "join", AgentHostIDs: []string{"host"}}, true, "agent"},
		{"new worker", Input{Action: "join", JoinHostIDs: []string{"host"}}, true, "agent"},
		{"server", Input{Action: "join", JoinRole: "server", JoinHostIDs: []string{"host"}, ServerToken: fixtureServerToken}, true, "server"},
		{"mixed lists", Input{Action: "join", AgentHostIDs: []string{"host"}, JoinHostIDs: []string{"host"}}, false, ""},
		{"server legacy list", Input{Action: "join", JoinRole: "server", AgentHostIDs: []string{"host"}, ServerToken: fixtureServerToken}, false, ""},
		{"server batch", Input{Action: "join", JoinRole: "server", JoinHostIDs: []string{"a", "b"}, ServerToken: fixtureServerToken}, false, ""},
		{"server missing token", Input{Action: "join", JoinRole: "server", JoinHostIDs: []string{"host"}}, false, ""},
		{"worker long token", Input{Action: "join", AgentHostIDs: []string{"host"}, ServerToken: fixtureServerToken}, false, ""},
		{"unknown role", Input{Action: "join", JoinRole: "admin"}, false, ""},
		{"create with join role", Input{Action: "create", JoinRole: "server"}, false, ""},
		{"create with join hosts", Input{Action: "create", JoinHostIDs: []string{"host"}}, false, ""},
		{"create with server token", Input{Action: "create", ServerToken: fixtureServerToken}, false, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			input := scenario.input
			err := normalizeJoinSelection(&input)
			if scenario.valid {
				if err != nil || input.JoinRole != scenario.role || !slices.Equal(input.JoinHostIDs, []string{"host"}) {
					t.Fatalf("selection=%+v err=%v", input, err)
				}
			} else if !errors.Is(err, ErrInvalidInput) || strings.Contains(err.Error(), "fixture-private-server-token") {
				t.Fatalf("unsafe validation: %v", err)
			}
		})
	}
}

func TestServerPreviewInheritsCriticalConfigAndEncryptsToken(t *testing.T) {
	f, _, input, _ := serverJoinFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if plan.JoinRole != "server" || plan.ServerJoin == nil || plan.ServerJoin.Datastore != "embedded-etcd" || plan.ServerJoin.EtcdMembers != 1 || plan.Hosts[0].Role != "server" || plan.Version != "v1.35.5+k3s1" {
		t.Fatalf("incomplete server plan: %+v", plan)
	}
	for _, value := range []string{"server: " + plan.Endpoint, "cluster-cidr:", "10.42.0.0/16", "disable:", "traefik", "cluster-init: false", "disable-etcd: false"} {
		if !strings.Contains(plan.ServerConfig, value) {
			t.Fatalf("effective config missing %q: %s", value, plan.ServerConfig)
		}
	}
	var record model.K3sInstallation
	if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(plan)
	for _, value := range []string{string(public), string(record.Preview), string(record.Hosts), record.Logs, record.InputCiphertext} {
		if strings.Contains(value, "fixture-private-server-token") || strings.Contains(value, fixtureServerToken) {
			t.Fatal("server token escaped encrypted snapshot")
		}
	}
	snapshot, err := f.service.decode(record)
	if err != nil || snapshot.Input.ServerToken != fixtureServerToken || snapshot.Input.targetVersion != plan.Version || snapshot.ServerCriticalDigest == "" || len(snapshot.ServerMembers) != 1 {
		t.Fatalf("reviewed encrypted state missing: %v", err)
	}
	if len(f.transport.writes) != 0 || plan.Preflight == nil || !plan.Preflight.Passed {
		t.Fatal("preview performed installation")
	}
	if !slices.ContainsFunc(plan.Preflight.Checks, func(c PreflightCheck) bool { return c.ID == "etcd_quorum" && c.Status == "warning" }) {
		t.Fatal("two-member quorum warning missing")
	}
}

func TestServerJoinRejectsConflictingConfigRoleAndVersion(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		configure func(*Input)
	}{
		{"older version", func(i *Input) { i.Installer = map[string]any{"version": "v1.35.4+k3s1"} }},
		{"critical conflict", func(i *Input) { i.ServerOptions = map[string]any{"cluster-domain": "other.local"} }},
		{"yaml critical conflict", func(i *Input) { i.Overrides = "server:\n  cluster-cidr: [10.99.0.0/16]\n" }},
		{"cluster init", func(i *Input) { i.ServerOptions = map[string]any{"cluster-init": true} }},
		{"cluster reset", func(i *Input) { i.ServerOptions = map[string]any{"cluster-reset": true} }},
		{"disable agent", func(i *Input) { i.ServerOptions = map[string]any{"disable-agent": true} }},
		{"disable etcd", func(i *Input) { i.ServerOptions = map[string]any{"disable-etcd": true} }},
		{"disable api", func(i *Input) { i.ServerOptions = map[string]any{"disable-apiserver": true} }},
		{"external datastore", func(i *Input) {
			i.ServerOptions = map[string]any{"datastore-endpoint": "postgres://private:credential@example/db"}
		}},
		{"agent options", func(i *Input) { i.AgentOptions = map[string]any{"node-name": "worker-option"} }},
		{"force new etcd cluster", func(i *Input) {
			i.ServerOptions = map[string]any{"etcd-arg": []any{"force-new-cluster=true", "initial-cluster-state=new"}}
		}},
		{"yaml etcd override", func(i *Input) { i.Overrides = "server:\n  etcd-arg: [initial-cluster-state=new]\n" }},
		{"apiserver datastore override", func(i *Input) {
			i.ServerOptions = map[string]any{"kube-apiserver-arg": []any{"etcd-servers=https://other.example:2379"}}
		}},
		{"controller cidr override", func(i *Input) {
			i.ServerOptions = map[string]any{"kube-controller-manager-arg": []any{"cluster-cidr=10.99.0.0/16"}}
		}},
		{"scheduler credential override", func(i *Input) {
			i.ServerOptions = map[string]any{"kube-scheduler-arg": []any{"kubeconfig=/other-cluster.yaml"}}
		}},
		{"cloud controller credential override", func(i *Input) {
			i.ServerOptions = map[string]any{"kube-cloud-controller-manager-arg": []any{"kubeconfig=/other-cluster.yaml"}}
		}},
		{"legacy controller override", func(i *Input) { i.Overrides = "server:\n  kube-controller-arg: [cluster-cidr=10.99.0.0/16]\n" }},
		{"legacy cloud controller override", func(i *Input) {
			i.Overrides = "server:\n  kube-cloud-controller-arg: [kubeconfig=/other-cluster.yaml]\n"
		}},
		{"future cloud controller override", func(i *Input) {
			i.Overrides = "server:\n  cloud-controller-manager-arg: [kubeconfig=/other-cluster.yaml]\n"
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, _, input, _ := serverJoinFixture(t)
			input.Profile = "custom"
			scenario.configure(&input)
			_, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if !errors.Is(err, ErrInvalidInput) || strings.Contains(err.Error(), "credential@example") {
				t.Fatalf("expected safe validation: %v", err)
			}
			if len(f.transport.writes) != 0 {
				t.Fatal("invalid plan installed a node")
			}
		})
	}
}

func TestServerJoinBlocksUnknownHAAndUnreachableEtcdBeforePlanning(t *testing.T) {
	for _, scenario := range []string{"sqlite or external", "unhealthy", "token rejected", "missing config", "etcd unreachable"} {
		t.Run(scenario, func(t *testing.T) {
			f, clusters, input, _ := serverJoinFixture(t)
			if scenario == "etcd unreachable" {
				f.service.WithTransport(preflightReadTransport{Transport: f.transport, read: func(ctx context.Context, h model.Host, b sshclient.Bundle, script string) (string, error) {
					if strings.Contains(script, "EDO_K3S_ETCD_PEERS_OK") {
						return "", errors.New("private-peer-probe-error")
					}
					return f.transport.Read(ctx, h, b, script)
				}})
			} else {
				failure := map[string]error{"sqlite or external": kube.ErrServerJoinUnsupported, "unhealthy": kube.ErrServerJoinUnhealthy, "token rejected": kube.ErrServerJoinCredentials, "missing config": kube.ErrServerJoinConfig}[scenario]
				clusters.inspect = func() (*kube.ServerJoinInfo, error) { return nil, errors.Join(failure, errors.New(fixtureServerToken)) }
			}
			_, err := f.service.Preview(f.ctx, f.actor.ID, input)
			var failed *PreflightError
			if !errors.As(err, &failed) || failed.Report == nil || failed.Report.Passed {
				t.Fatalf("missing blocking report: %v", err)
			}
			encoded, _ := json.Marshal(failed.Report)
			if strings.Contains(string(encoded), "fixture-private-server-token") || strings.Contains(string(encoded), "private-peer-probe-error") {
				t.Fatal("preflight leaked remote error")
			}
			var count int64
			f.db.Model(&model.K3sInstallation{}).Count(&count)
			if count != 0 || len(f.transport.writes) != 0 || clusters.joins != 0 {
				t.Fatal("failed preflight acquired installation side effects")
			}
		})
	}
}

func TestServerExecuteRejectsChangedReviewBeforeSideEffects(t *testing.T) {
	for _, change := range []string{"critical config", "membership", "missing review"} {
		t.Run(change, func(t *testing.T) {
			f, _, input, info := serverJoinFixture(t)
			plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "critical config":
				info.CriticalDigest += "changed"
			case "membership":
				info.MembershipDigest += "changed"
			case "missing review":
				var record model.K3sInstallation
				f.db.First(&record, "uuid = ?", plan.ID)
				snapshot, _ := f.service.decode(record)
				snapshot.ServerMembers = nil
				encoded, _ := json.Marshal(snapshot)
				ciphertext, _ := f.service.secrets.Encrypt(string(encoded), []byte("k3s-installation:"+record.ID))
				f.db.Model(&record).Update("input_ciphertext", ciphertext)
			}
			_, err = f.service.Execute(f.ctx, f.actor.ID, plan.ID)
			if !errors.Is(err, ErrHostChanged) && !errors.Is(err, ErrPreflightRequired) {
				t.Fatalf("changed target accepted: %v", err)
			}
			var count int64
			f.db.Model(&model.K3sInstallationHostLock{}).Count(&count)
			if count != 0 || len(f.transport.writes) != 0 {
				t.Fatal("changed target acquired a lock or write")
			}
		})
	}
}

func TestServerWorkerRechecksMembershipAfterLastHostProbe(t *testing.T) {
	f, clusters, input, info := serverJoinFixture(t)
	var probes atomic.Int32
	base := f.service.transport
	f.service.WithTransport(preflightReadTransport{Transport: base, read: func(ctx context.Context, h model.Host, b sshclient.Bundle, script string) (string, error) {
		result, err := base.Read(ctx, h, b, script)
		if strings.Contains(script, "EDO_K3S_ETCD_PEERS_OK") && probes.Add(1) >= 3 {
			info.MembershipDigest = "external-membership-change"
		}
		return result, err
	}})
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(context.Background())
	if err != nil || record == nil {
		t.Fatal(err)
	}
	f.service.runInstallation(context.Background(), *record)
	result, err := f.service.Get(f.ctx, plan.ID)
	if err != nil || result.Status != "failed" || result.Error != ErrHostChanged.Error() || len(f.transport.writes) != 0 || len(clusters.linked) != 0 || clusters.joins != 0 {
		t.Fatalf("stale membership allowed installation: %+v %v", result, err)
	}
}

func TestSameClusterSerializesWorkerAndMasterOnDifferentHosts(t *testing.T) {
	for _, firstRole := range []string{"agent", "server"} {
		t.Run(firstRole, func(t *testing.T) {
			f, _, input, _ := serverJoinFixture(t)
			first := input
			if firstRole == "agent" {
				first.JoinRole = "agent"
				first.ServerToken = ""
			}
			plan, err := f.service.Preview(f.ctx, f.actor.ID, first)
			if err != nil {
				t.Fatal(err)
			}
			other := f.host
			other.RowID = 0
			other.ID = uuid.NewString()
			other.Name = "另一台目标主机"
			other.Address = "192.0.2.11"
			plain, err := f.service.secrets.Decrypt(f.host.SSHCredentialCiphertext, hostcredential.AAD(f.host.ID))
			if err != nil {
				t.Fatal(err)
			}
			other.SSHCredentialCiphertext, err = f.service.secrets.Encrypt(plain, hostcredential.AAD(other.ID))
			if err != nil {
				t.Fatal(err)
			}
			if err = f.db.WithContext(f.ctx).Create(&other).Error; err != nil {
				t.Fatal(err)
			}
			if err = f.db.Create(&model.HostCapability{HostID: other.ID, Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady}).Error; err != nil {
				t.Fatal(err)
			}
			second := input
			second.JoinHostIDs = []string{other.ID}
			if firstRole == "server" {
				second.JoinRole = "agent"
				second.ServerToken = ""
			}
			otherPlan, err := f.service.Preview(f.ctx, f.actor.ID, second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.Execute(f.ctx, f.actor.ID, otherPlan.ID); !errors.Is(err, ErrHostBusy) {
				t.Fatalf("parallel same-cluster join accepted: %v", err)
			}
			var locks int64
			f.db.Model(&model.K3sInstallationHostLock{}).Count(&locks)
			if locks != 1 || len(f.transport.writes) != 0 {
				t.Fatal("queued second cluster operation")
			}
		})
	}
}

func TestWorkerNewJoinHostsFieldUsesShortLivedToken(t *testing.T) {
	f, clusters, input := joinVersionFixture(t, "v1.35.5+k3s1")
	input.JoinRole = "agent"
	input.JoinHostIDs = input.AgentHostIDs
	input.AgentHostIDs = nil
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if plan.JoinRole != "agent" || plan.Hosts[0].Role != "agent" || plan.ServerJoin != nil {
		t.Fatal("new host field changed worker role")
	}
	if _, err = f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(context.Background())
	if err != nil || record == nil {
		t.Fatal(err)
	}
	f.service.runInstallation(context.Background(), *record)
	result, err := f.service.Get(f.ctx, plan.ID)
	if err != nil || result.Status != "succeeded" || clusters.joins != 1 || clusters.revoked != 1 || !slices.Contains(clusters.linked, f.host.ID) {
		t.Fatalf("new API worker join failed: %+v %v", result, err)
	}
}

type serverInstallTransport struct {
	Transport
	afterRun func(string)
}

func (t serverInstallTransport) Run(ctx context.Context, h model.Host, b sshclient.Bundle, script string, out io.Writer) error {
	if err := t.Transport.Run(ctx, h, b, script, out); err != nil {
		return err
	}
	t.afterRun(script)
	return nil
}

func TestServerWorkerJoinsExistingClusterWithoutBootstrapToken(t *testing.T) {
	f, clusters, input, before := serverJoinFixture(t)
	clusters.linked = []string{"old-associated-host"}
	var installed atomic.Bool
	after := *before
	after.Members = append(append([]kube.ServerJoinMember{}, before.Members...), kube.ServerJoinMember{ID: "2", Name: "new-master-5678abcd", PeerURLs: []string{"https://192.0.2.10:2380"}, ClientURLs: []string{"https://192.0.2.10:2379"}})
	after.MembershipDigest = "membership-after-own-join"
	clusters.inspect = func() (*kube.ServerJoinInfo, error) {
		if installed.Load() {
			return &after, nil
		}
		return before, nil
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			_, _ = io.WriteString(w, "ok")
			return
		}
		if r.URL.Path != "/api/v1/nodes" {
			http.NotFound(w, r)
			return
		}
		nodes := []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "old-master"}}}
		if installed.Load() {
			nodes = append(nodes, corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "new-master", Labels: map[string]string{"node-role.kubernetes.io/control-plane": "true", "node-role.kubernetes.io/etcd": "true", "edo.dev/host": f.host.ID}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(corev1.NodeList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"}, Items: nodes})
	}))
	defer api.Close()
	var err error
	clusters.client, err = kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	f.service.WithTransport(serverInstallTransport{Transport: f.service.transport, afterRun: func(script string) {
		if strings.Contains(script, " | base64 -d > /etc/rancher/k3s/config.yaml") {
			installed.Store(true)
		}
	}})
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		var failed *PreflightError
		if errors.As(err, &failed) {
			t.Fatalf("preview: %+v", failed.Report)
		}
		t.Fatal(err)
	}
	if _, err = f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(context.Background())
	if err != nil || record == nil {
		t.Fatal(err)
	}
	f.service.runInstallation(context.Background(), *record)
	result, err := f.service.Get(f.ctx, plan.ID)
	if err != nil || result.Status != "succeeded" || clusters.created != 0 || clusters.joins != 0 || clusters.revoked != 0 || !slices.Equal(clusters.linked, []string{"old-associated-host", f.host.ID}) {
		t.Fatalf("incomplete server join: %+v err=%v", result, err)
	}
	if len(f.transport.writes) != 3 || !strings.Contains(f.transport.writes[0], "get.k3s.io") || strings.Contains(f.transport.writes[0], "config.yaml") || !strings.Contains(f.transport.writes[2], "rmdir") {
		t.Fatal("server must download, install once, then clean its staging directory")
	}
	script := f.transport.writes[1]
	if !strings.Contains(script, "INSTALL_K3S_SKIP_DOWNLOAD=true") || !strings.Contains(script, "INSTALL_K3S_SKIP_SELINUX_RPM=true") {
		t.Fatal("final installer may download unreviewed artifacts")
	}
	if strings.Contains(script, " -o \"$work/install.sh\"") {
		t.Fatal("final install must not contain slow installer download")
	}
	writeAt := strings.Index(script, " | base64 -d > /etc/rancher/k3s/config.yaml")
	if writeAt < 0 {
		t.Fatal("no config destination in installer")
	}
	start := strings.LastIndex(script[:writeAt], "printf '%s' '")
	if start < 0 {
		t.Fatal("no config write in installer")
	}
	encoded := strings.SplitN(script[start+len("printf '%s' '"):], "'", 2)[0]
	config, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !strings.Contains(string(config), "server: "+plan.Endpoint) || !strings.Contains(string(config), fixtureServerToken) || !strings.Contains(script, " sh "+"\"$work/install.sh\" server") {
		t.Fatalf("server join command missing reviewed target: %v", err)
	}
	if strings.Contains(result.Logs, "fixture-private-server-token") || strings.Contains(result.Hosts[0].Config, "fixture-private-server-token") {
		t.Fatal("installation result leaked Server Token")
	}
}

func TestServerDownloadMustFinishBeforeFinalSnapshotValidation(t *testing.T) {
	for _, change := range []string{"version", "membership", "critical", "permission"} {
		t.Run(change, func(t *testing.T) {
			f, clusters, input, info := serverJoinFixture(t)
			f.service.WithTransport(serverInstallTransport{Transport: f.service.transport, afterRun: func(script string) {
				if !strings.Contains(script, "edo_stage_ready=true") {
					return
				}
				switch change {
				case "version":
					clusters.version = "v1.35.6+k3s1"
				case "membership":
					info.MembershipDigest = "changed"
				case "critical":
					info.CriticalDigest = "changed"
				case "permission":
					f.auth.denied = true
				}
			}})
			plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
				t.Fatal(err)
			}
			record, err := f.service.claim(context.Background())
			if err != nil || record == nil {
				t.Fatal(err)
			}
			f.service.runInstallation(context.Background(), *record)
			var actual model.K3sInstallation
			if err = f.db.First(&actual, "uuid = ?", plan.ID).Error; err != nil {
				t.Fatal(err)
			}
			if actual.Status != "failed" || len(clusters.linked) != 0 || len(f.transport.writes) != 2 {
				t.Fatalf("changed state installed Master: status=%s writes=%d", actual.Status, len(f.transport.writes))
			}
			for _, script := range f.transport.writes {
				if strings.Contains(script, "base64 -d > /etc/rancher/k3s/config.yaml") {
					t.Fatal("Master configured after snapshot changed during download")
				}
			}
		})
	}
}

func TestJoinedServerMembershipAcceptsExactlyTheReviewedAddition(t *testing.T) {
	before := serverInfoFixture().Members
	newMember := kube.ServerJoinMember{ID: "2", Name: "new-master-1234abcd"}
	for _, scenario := range []struct {
		name    string
		members []kube.ServerJoinMember
		valid   bool
	}{
		{"exact addition", append(append([]kube.ServerJoinMember{}, before...), newMember), true},
		{"no addition", before, false},
		{"wrong node", append(append([]kube.ServerJoinMember{}, before...), kube.ServerJoinMember{ID: "2", Name: "other-master-1234abcd"}), false},
		{"learner", append(append([]kube.ServerJoinMember{}, before...), kube.ServerJoinMember{ID: "2", Name: "new-master-1234abcd", Learner: true}), false},
		{"duplicate id", append(append([]kube.ServerJoinMember{}, before...), kube.ServerJoinMember{ID: "1", Name: "new-master"}), false},
		{"two additions", append(append([]kube.ServerJoinMember{}, before...), newMember, kube.ServerJoinMember{ID: "3", Name: "other-master"}), false},
		{"removed original", []kube.ServerJoinMember{newMember, {ID: "3", Name: "other-master"}}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if allowedJoinedMembership(before, scenario.members, "new-master") != scenario.valid {
				t.Fatal("unexpected membership result")
			}
		})
	}
}

func TestServerPeerProbeIsReadOnlyAndRequiresExpectedPorts(t *testing.T) {
	script, err := serverPeerProbeScript(serverInfoFixture())
	if err != nil || !strings.Contains(script, "'192.0.2.20' '2379'") || !strings.Contains(script, "'192.0.2.20' '2380'") || strings.Contains(script, "curl") || strings.Contains(script, "install ") {
		t.Fatalf("unexpected peer probe: %s %v", script, err)
	}
	for _, invalid := range []string{"http://192.0.2.20:2380", "https://192.0.2.20:1234", "https://192.0.2.20:2380/path", "https://user:password@192.0.2.20:2380"} {
		info := serverInfoFixture()
		info.Members[0].PeerURLs = []string{invalid}
		if _, err := serverPeerProbeScript(info); err == nil {
			t.Fatalf("unsafe peer accepted: %s", invalid)
		}
	}
	input := Input{Action: "join", JoinRole: "server"}
	probe := preflightProbeScript(Host{Role: "server"}, input, true)
	if !strings.Contains(probe, "2379") || !strings.Contains(probe, "2380") {
		t.Fatal("local etcd ports were not checked")
	}
}

func TestServerWaitDoesNotAssociateWorkerOrUnreadyNode(t *testing.T) {
	for _, role := range []string{"worker", "unready"} {
		t.Run(role, func(t *testing.T) {
			f, clusters, input, _ := serverJoinFixture(t)
			plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			var record model.K3sInstallation
			f.db.First(&record, "uuid = ?", plan.ID)
			snapshot, _ := f.service.decode(record)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				labels := map[string]string{}
				ready := corev1.ConditionTrue
				if role == "unready" {
					labels["node-role.kubernetes.io/control-plane"], labels["node-role.kubernetes.io/etcd"] = "true", "true"
					ready = corev1.ConditionFalse
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(corev1.NodeList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"}, Items: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "new-master", Labels: labels}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}}}}})
			}))
			defer api.Close()
			clusters.client, _ = kubernetes.NewForConfig(&rest.Config{Host: api.URL})
			ctx, cancel := context.WithTimeout(f.ctx, 50*time.Millisecond)
			defer cancel()
			err = f.service.waitForJoinedServer(ctx, &record, snapshot, plan.Hosts[0])
			if !errors.Is(err, ErrReadiness) || len(clusters.linked) != 0 {
				t.Fatalf("invalid Master was associated: %v", err)
			}
		})
	}
}
