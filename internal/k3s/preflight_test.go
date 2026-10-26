package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"edo/internal/hostidentity"
	"edo/internal/model"
	"edo/internal/sshclient"
)

type preflightReadTransport struct {
	Transport
	read func(context.Context, model.Host, sshclient.Bundle, string) (string, error)
}

func (t preflightReadTransport) Read(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string) (string, error) {
	return t.read(ctx, host, bundle, script)
}

func TestPreflightRejectsDuplicateMachinesAndNamesAcrossRoles(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		sameID        bool
		sameName      bool
		withID        bool
		serverNoAgent bool
		wantPass      bool
	}{
		{name: "distinct machines and names", wantPass: true},
		{name: "master and worker target the same machine", sameID: true},
		{name: "duplicate node names", sameName: true},
		{name: "unique name suffix", sameName: true, withID: true, wantPass: true},
		{name: "agentless server does not reserve a node name", sameName: true, serverNoAgent: true, wantPass: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			hosts := []Host{{ID: "master", Name: "Master", Role: "server"}, {ID: "worker", Name: "Worker", Role: "agent"}}
			report := &PreflightReport{Hosts: []HostPreflight{
				{Hostname: "master", Passed: true, Identity: hostidentity.Identity{MachineID: strings.Repeat("a", 32)}},
				{Hostname: "worker", Passed: true, Identity: hostidentity.Identity{MachineID: strings.Repeat("b", 32)}},
			}}
			if scenario.sameID {
				report.Hosts[1].Identity = report.Hosts[0].Identity
			}
			if scenario.sameName {
				report.Hosts[1].Hostname = report.Hosts[0].Hostname
			}
			input := Input{ServerOptions: map[string]any{"disable-agent": scenario.serverNoAgent}, AgentOptions: map[string]any{"with-node-id": scenario.withID}}
			checkPreflightIdentities(report, hosts, input, nil)
			for index, host := range report.Hosts {
				if host.Passed != scenario.wantPass {
					t.Fatalf("host %d passed=%v; checks=%+v", index, host.Passed, host.Checks)
				}
			}
		})
	}
}

func TestPreflightReadsLiveMembershipAfterSlowHostProbe(t *testing.T) {
	f, clusters, input := joinVersionFixture(t, CatalogK3sVersion)
	var probed atomic.Bool
	identity := parsePreflightProbe(Host{ID: f.host.ID}, preflightFixtureOutput(f.host.ID), nil).Identity
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("preflight sent a mutating API request: %s", r.Method)
		}
		if r.URL.Path == "/readyz" {
			_, _ = io.WriteString(w, "ok")
			return
		}
		if r.URL.Path != "/api/v1/nodes" {
			http.NotFound(w, r)
			return
		}
		if !probed.Load() {
			t.Error("membership was read before the host probe completed")
		}
		nodes := corev1.NodeList{Items: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "installed-while-probing"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{MachineID: identity.MachineID}}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(nodes)
	}))
	defer server.Close()
	var err error
	clusters.client, err = kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	f.service.WithTransport(preflightReadTransport{Transport: f.transport, read: func(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string) (string, error) {
		result, err := f.transport.Read(ctx, host, bundle, script)
		probed.Store(true)
		return result, err
	}})
	report, err := f.service.runPreflight(f.ctx, input, []Host{{ID: f.host.ID, Name: f.host.Name, Role: "agent"}})
	if err != nil || report == nil || report.Passed || !probed.Load() {
		t.Fatalf("live duplicate was not blocked: %+v %v", report, err)
	}
	if !errors.Is(requirePreflight(report), ErrPreflightFailed) || len(f.transport.writes) != 0 || clusters.joins != 0 {
		t.Fatal("preflight must block without installation or bootstrap credentials")
	}
}

func TestWorkerRepeatsHostPreflightImmediatelyBeforeInstallation(t *testing.T) {
	f := newFixture(t)
	clusters := fakeClusterClient(t, f)
	f.service.kubernetes = clusters
	var probes atomic.Int32
	f.service.WithTransport(preflightReadTransport{Transport: f.transport, read: func(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string) (string, error) {
		output, err := f.transport.Read(ctx, host, bundle, script)
		if strings.Contains(script, "EDO_K3S_REPORT_END") && probes.Add(1) >= 3 {
			output = strings.Replace(output, "EDO_K3S_CHECK\tmemory\tpassed", "EDO_K3S_CHECK\tmemory\tfailed", 1)
		}
		return output, err
	}})
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
	if err != nil || result.Status != "failed" || probes.Load() != 3 || len(f.transport.writes) != 0 || clusters.created != 0 {
		t.Fatalf("changed host was installed: result=%+v probes=%d err=%v", result, probes.Load(), err)
	}
	if !strings.Contains(result.Error, "内存") {
		t.Fatalf("controlled failure lost the blocking reason: %q", result.Error)
	}
}

func TestExecuteRejectsLegacyPlanWithoutPreflight(t *testing.T) {
	f := newFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
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
	snapshot.PreflightVersion = 0
	encoded, _ := json.Marshal(snapshot)
	record.InputCiphertext, err = f.service.secrets.Encrypt(string(encoded), []byte("k3s-installation:"+record.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&record).Update("input_ciphertext", record.InputCiphertext).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("legacy plan accepted without a fresh preview: %v", err)
	}
	var locks int64
	f.db.Model(&model.K3sInstallationHostLock{}).Count(&locks)
	if locks != 0 || len(f.transport.writes) != 0 {
		t.Fatal("legacy plan acquired installation side effects")
	}
}

func TestPreviewCancellationStopsChecksWithoutSavingPlan(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{}, 1)
	f.service.WithTransport(preflightReadTransport{Transport: f.transport, read: func(ctx context.Context, _ model.Host, _ sshclient.Bundle, _ string) (string, error) {
		started <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	}})
	ctx, cancel := context.WithTimeout(f.ctx, 100*time.Millisecond)
	defer cancel()
	before := time.Now()
	plan, err := f.service.Preview(ctx, f.actor.ID, f.input())
	if plan != nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(before) > time.Second {
		t.Fatalf("preview exceeded parent budget: plan=%+v err=%v elapsed=%s", plan, err, time.Since(before))
	}
	select {
	case <-started:
	default:
		t.Fatal("fixture did not reach the slow host check")
	}
	var plans int64
	f.db.Model(&model.K3sInstallation{}).Count(&plans)
	if plans != 0 || len(f.transport.writes) != 0 {
		t.Fatal("cancelled preview persisted an executable plan or installed a node")
	}
}
